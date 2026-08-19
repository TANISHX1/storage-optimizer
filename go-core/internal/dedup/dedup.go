package dedup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/models"
)

// ============================================================================
// TWO-PASS DUPLICATE DETECTION ENGINE
//
// SYSTEMS & ALGORITHMIC OPTIMIZATIONS:
//
// 1. Pass 1: Cheap Size-Bucket Filter (Zero File-Open I/O beyond stat):
//    - Real-world disk trees contain millions of files, but 95%+ have unique byte sizes.
//    - If File A is 10,485,760 bytes and File B is 10,485,761 bytes, they CANNOT be identical.
//    - We query SQLite: `WHERE size IN (SELECT size FROM files GROUP BY size HAVING COUNT(*) > 1)`
//    - Result: Only files sharing an EXACT byte size with at least one other file proceed to Pass 2.
//      Unique-sized files are NEVER hashed, eliminating unnecessary disk reads.
//
// 2. Pass 2: Streaming SHA-256 Hashing:
//    - Never use `os.ReadFile()` on large files! Loading a 4 GB video into memory causes heap thrashing.
//    - We allocate a fixed 64 KB buffer per worker (`io.CopyBuffer`) and stream disk blocks directly
//      into the hardware-accelerated SHA-256 state machine.
//    - Peak memory overhead: O(workers * 64 KB) = under 1 MB of RAM regardless of scanned data size.
//
// 3. Concurrency Model:
//    - Worker pool scales to `runtime.NumCPU()`.
//    - Bounded channels prevent memory accumulation during hash dispatching.
//    - Computed hashes are batched and committed to SQLite atomically.
// ============================================================================

// Config configures duplicate detection execution.
type Config struct {
	NumWorkers  int  // Concurrency for hashing (defaults to runtime.NumCPU())
	ChunkSize   int  // Buffer size for streaming I/O (default: 64 KB)
	ForceRehash bool // Force re-hashing even if content_hash already exists in DB
}

// Engine coordinates size grouping and content hashing.
type Engine struct {
	db     *db.DB
	config Config
}

// New creates a new duplicate detection Engine.
func New(database *db.DB, cfg Config) *Engine {
	if cfg.NumWorkers <= 0 {
		cfg.NumWorkers = runtime.NumCPU()
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 64 * 1024 // 64 KB chunk buffer
	}
	return &Engine{
		db:     database,
		config: cfg,
	}
}

// Execute runs the full two-pass duplicate detection pipeline and persists group IDs.
func (e *Engine) Execute(ctx context.Context) (*models.DedupReport, error) {
	startTime := time.Now()

	// ------------------------------------------------------------------------
	// PASS 1: Fetch Candidate Files from Size Buckets
	// ------------------------------------------------------------------------
	candidates, err := e.db.GetCandidateSizeFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("pass 1 size bucket query failed: %w", err)
	}

	if len(candidates) == 0 {
		_ = e.db.AssignDuplicateGroups(ctx)
		return &models.DedupReport{
			Duration: time.Since(startTime),
		}, nil
	}

	// Filter files needing hash computation
	var filesToHash []models.FileMetadata
	for _, f := range candidates {
		if e.config.ForceRehash || f.ContentHash == "" {
			filesToHash = append(filesToHash, f)
		}
	}

	// ------------------------------------------------------------------------
	// PASS 2: Parallel Streaming SHA-256 Hashing
	// ------------------------------------------------------------------------
	if len(filesToHash) > 0 {
		if err := e.hashCandidates(ctx, filesToHash); err != nil {
			return nil, fmt.Errorf("pass 2 hashing failed: %w", err)
		}
	}

	// ------------------------------------------------------------------------
	// PASS 3: Precompute & Index duplicate_group_id Clusters in SQLite
	// ------------------------------------------------------------------------
	if err := e.db.AssignDuplicateGroups(ctx); err != nil {
		return nil, fmt.Errorf("failed to precompute duplicate group IDs: %w", err)
	}

	// ------------------------------------------------------------------------
	// AGGREGATION: Query & Format Duplicate Clusters
	// ------------------------------------------------------------------------
	groups, totalGroups, totalDupFiles, totalWasted, err := e.db.GetDuplicateGroupsPaginated(ctx, 1, 10000)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve duplicate groups: %w", err)
	}

	report := &models.DedupReport{
		TotalGroups:         totalGroups,
		TotalDuplicateFiles: totalDupFiles,
		TotalWastedBytes:    totalWasted,
		Page:                1,
		Limit:               len(groups),
		TotalPages:          1,
		Groups:              groups,
		Duration:            time.Since(startTime),
	}

	return report, nil
}

// GetDuplicatesReportPaginated queries SQLite for duplicate clusters without running re-hashing (Pure Read).
func (e *Engine) GetDuplicatesReportPaginated(ctx context.Context, page int, limit int) (*models.DedupReport, error) {
	startTime := time.Now()

	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}

	groups, totalGroups, totalDupFiles, totalWasted, err := e.db.GetDuplicateGroupsPaginated(ctx, page, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query duplicate groups: %w", err)
	}

	totalPages := 0
	if totalGroups > 0 {
		totalPages = int(math.Ceil(float64(totalGroups) / float64(limit)))
	}

	return &models.DedupReport{
		TotalGroups:         totalGroups,
		TotalDuplicateFiles: totalDupFiles,
		TotalWastedBytes:    totalWasted,
		Page:                page,
		Limit:               limit,
		TotalPages:          totalPages,
		Groups:              groups,
		Duration:            time.Since(startTime),
	}, nil
}

// hashCandidates distributes candidate files across a worker pool to compute SHA-256 hashes.
func (e *Engine) hashCandidates(ctx context.Context, files []models.FileMetadata) error {
	jobsChan := make(chan models.FileMetadata, 1024)
	resultsChan := make(chan models.FileHashUpdate, 1024)

	var (
		wg         sync.WaitGroup
		hashedDocs int64
	)

	// Launch hashing worker goroutines
	for w := 0; w < e.config.NumWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker maintains its own reusable 64 KB buffer to minimize GC allocations
			buf := make([]byte, e.config.ChunkSize)

			for {
				select {
				case <-ctx.Done():
					return
				case file, ok := <-jobsChan:
					if !ok {
						return
					}

					hashStr, err := computeStreamingSHA256(file.Path, buf)
					if err != nil {
						// Non-fatal: file might have been deleted or permission changed
						continue
					}

					atomic.AddInt64(&hashedDocs, 1)

					select {
					case resultsChan <- models.FileHashUpdate{ID: file.ID, ContentHash: hashStr}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	// Single writer / collector to batch SQLite updates
	doneBatcher := make(chan struct{})
	var batchErr error

	go func() {
		defer close(doneBatcher)
		var updates []models.FileHashUpdate
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		flush := func() {
			if len(updates) == 0 {
				return
			}
			if err := e.db.BatchUpdateContentHashes(ctx, updates); err != nil && batchErr == nil {
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

	// Feed files into jobs channel
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

	// Wait for workers to finish, then close resultsChan
	wg.Wait()
	close(resultsChan)

	// Wait for batch updater to persist all hashes
	<-doneBatcher

	return batchErr
}

// computeStreamingSHA256 opens a file and streams its content through SHA-256 using a provided buffer.
func computeStreamingSHA256(filePath string, buffer []byte) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, f, buffer); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
