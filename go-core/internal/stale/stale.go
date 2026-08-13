package stale

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/models"
)

// ============================================================================
// STALENESS SCORING ENGINE
//
// SYSTEMS & ALGORITHMIC INSIGHTS:
//
// 1. Linux VFS Timestamp Dynamics (mtime vs atime):
//    - Modern Linux mounts filesystems with the `relatime` (relative access time) flag.
//      `atime` is only updated if the previous `atime` is older than `mtime`, or once every 24h.
//    - Conclusion: We treat `mtime` as the primary ground truth (weight 0.70) and `atime`
//      as secondary corroboration (weight 0.30).
//
// 2. Exponential Decay Saturation Formula:
//      BaseScore = 1.0 - exp(-EffectiveAgeDays / 180.0)
//
// 3. System Directory & File Category Weighting Matrix:
//    - Critical Protected OS Files (CategorySystemProtected): Weight 0.01 (BLOCKED from cleanup)
//    - Hidden Dotfiles & User Configs (.config, .bashrc, .ssh): Weight 0.10 - 0.15
//    - Dependency Caches (node_modules, vendor): Weight 0.25
//    - Cleanable System Junk:
//      • Crash Dumps & Core Snapshots (CategoryCrashDump): Weight 1.40 (Boosted)
//      • Old System Logs (CategorySystemLog): Weight 1.35 (Boosted)
//      • Temporary Files (CategoryTemp): Weight 1.30 (Boosted)
// ============================================================================

// Config holds operational parameters for the staleness engine.
type Config struct {
	NumWorkers int // Worker pool concurrency
}

// Engine coordinates staleness evaluation.
type Engine struct {
	db     *db.DB
	config Config
}

// New creates a new staleness Engine.
func New(database *db.DB, cfg Config) *Engine {
	if cfg.NumWorkers <= 0 {
		cfg.NumWorkers = runtime.NumCPU()
	}
	return &Engine{
		db:     database,
		config: cfg,
	}
}

// ComputeAndPersistScores evaluates staleness for all indexed files and saves scores in SQLite.
func (e *Engine) ComputeAndPersistScores(ctx context.Context) (int, error) {
	files, err := e.db.GetAllFiles(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch files for staleness evaluation: %w", err)
	}

	if len(files) == 0 {
		return 0, nil
	}

	jobsChan := make(chan models.FileMetadata, 2048)
	resultsChan := make(chan models.FileStalenessUpdate, 2048)

	var (
		wg        sync.WaitGroup
		evaluated int64
	)

	now := time.Now()

	for w := 0; w < e.config.NumWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case file, ok := <-jobsChan:
					if !ok {
						return
					}
					score := CalculateScore(file, now)
					atomic.AddInt64(&evaluated, 1)

					select {
					case resultsChan <- models.FileStalenessUpdate{ID: file.ID, StalenessScore: score}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	doneBatcher := make(chan struct{})
	var batchErr error

	go func() {
		defer close(doneBatcher)
		var updates []models.FileStalenessUpdate
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		flush := func() {
			if len(updates) == 0 {
				return
			}
			if err := e.db.BatchUpdateStalenessScores(ctx, updates); err != nil && batchErr == nil {
				batchErr = err
			}
			updates = updates[:0]
		}

		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case res, ok := <-resultsChan:
				if !ok {
					flush()
					return
				}
				updates = append(updates, res)
				if len(updates) >= 500 {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()

	go func() {
		for _, f := range files {
			select {
			case <-ctx.Done():
				break
			case jobsChan <- f:
			}
		}
		close(jobsChan)
	}()

	wg.Wait()
	close(resultsChan)
	<-doneBatcher

	return int(atomic.LoadInt64(&evaluated)), batchErr
}

// FindStaleFiles calculates scores and returns candidates untouched for minDays.
func (e *Engine) FindStaleFiles(ctx context.Context, minDays int, minScore float64, limit int) (*models.StaleReport, error) {
	startTime := time.Now()

	if _, err := e.ComputeAndPersistScores(ctx); err != nil {
		return nil, fmt.Errorf("failed to update staleness scores: %w", err)
	}

	files, err := e.db.GetStaleFiles(ctx, minDays, minScore, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query stale files: %w", err)
	}

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	return &models.StaleReport{
		ThresholdDays: minDays,
		TotalFiles:    len(files),
		TotalBytes:    totalBytes,
		Files:         files,
		Duration:      time.Since(startTime),
	}, nil
}

// CalculateScore computes the composite staleness score [0.0 to 1.0] for a single file.
func CalculateScore(meta models.FileMetadata, now time.Time) float64 {
	mtimeAgeDays := now.Sub(meta.Mtime).Hours() / 24.0
	if mtimeAgeDays < 0 {
		mtimeAgeDays = 0
	}

	atimeAgeDays := mtimeAgeDays
	if !meta.Atime.IsZero() {
		diff := now.Sub(meta.Atime).Hours() / 24.0
		if diff >= 0 {
			atimeAgeDays = diff
		}
	}

	effectiveAgeDays := (0.70 * mtimeAgeDays) + (0.30 * atimeAgeDays)
	baseScore := 1.0 - math.Exp(-effectiveAgeDays/180.0)

	weight := getCategoryAndPathWeight(meta)

	finalScore := baseScore * weight
	if finalScore > 1.0 {
		finalScore = 1.0
	} else if finalScore < 0.0 {
		finalScore = 0.0
	}

	return math.Round(finalScore*10000) / 10000
}

// getCategoryAndPathWeight computes the priority multiplier [0.01 to 1.40]
func getCategoryAndPathWeight(meta models.FileMetadata) float64 {
	// Rule 1: Protected OS Files (Never recommend deletion)
	if meta.Category == models.CategorySystemProtected {
		return 0.01
	}

	// Rule 2: Cleanable System Junk
	switch meta.Category {
	case models.CategoryCrashDump:
		return 1.40 // Old crash snapshots & coredumps
	case models.CategorySystemLog:
		return 1.35 // Unrotated / old logs
	case models.CategoryTemp:
		return 1.30 // Stale temp files
	case models.CategorySystemCache:
		return 1.10
	}

	cleanPath := filepath.ToSlash(meta.Path)
	base := filepath.Base(cleanPath)

	// Rule 3: Version Control Repositories
	if strings.Contains(cleanPath, "/.git/") || strings.Contains(cleanPath, "/.svn/") || strings.Contains(cleanPath, "/.hg/") {
		return 0.05
	}

	// Rule 4: System & User Configurations
	if strings.Contains(cleanPath, "/.config/") || strings.Contains(cleanPath, "/.local/") ||
		strings.Contains(cleanPath, "/.ssh/") || strings.Contains(cleanPath, "/.gnupg/") {
		return 0.10
	}

	// Rule 5: Hidden Dotfiles
	if strings.HasPrefix(base, ".") {
		return 0.15
	}

	// Rule 6: Dependency Caches
	if strings.Contains(cleanPath, "/node_modules/") || strings.Contains(cleanPath, "/vendor/") ||
		strings.Contains(cleanPath, "/.cargo/") || strings.Contains(cleanPath, "/.npm/") ||
		strings.Contains(cleanPath, "/site-packages/") {
		return 0.25
	}

	// Rule 7: Source Code Files
	switch meta.Extension {
	case ".go", ".c", ".cpp", ".h", ".hpp", ".rs", ".py", ".ts", ".js", ".java", ".html", ".css", ".sql":
		return 0.50
	}

	// Rule 8: Known Temporary Extensions
	switch meta.Extension {
	case ".log", ".tmp", ".temp", ".bak", ".old", ".swp", ".cache", ".pyc", ".o", ".a", ".dmp", ".core":
		return 1.30
	}

	return 1.0
}
