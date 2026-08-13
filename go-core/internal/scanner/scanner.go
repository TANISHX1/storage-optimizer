package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"storage-optimizer/go-core/internal/models"
)

// ============================================================================
// CONCURRENT FILESYSTEM SCANNER & LINUX METADATA EXTRACTOR
//
// INCREMENTAL RE-SCANNING & METADATA CAPTURE (PHASE 4):
// 1. Safe Symlink Handling (os.Lstat):
//    - Avoids circular symlink loops by inspecting inodes without following links.
//
// 2. Linux Inode & Timestamp Resolution (syscall.Stat_t):
//    - Captures nanosecond mtime, atime, and inode numbers directly from the VFS.
//
// 3. Scan Timestamp Tagging:
//    - Every discovered file is tagged with ScanStartTime.
//    - Unchanged files retain this timestamp during SQLite upsert.
//    - Stale/deleted files have last_scanned_at < ScanStartTime, allowing precise pruning.
// ============================================================================

// ScanConfig holds operational parameters for the scanner.
type ScanConfig struct {
	RootPath      string        // Root directory to traverse
	NumWorkers    int           // Concurrency level (defaults to runtime.NumCPU())
	ChannelBuffer int           // Buffer capacity for file output channel
	ProgressFunc  func(p Stats) // Optional progress callback for CLI/GUI
}

// Stats tracks real-time progress and aggregated metrics during a scan.
type Stats struct {
	FilesScanned int64         // Count of regular files processed
	DirsScanned  int64         // Count of directories traversed
	TotalBytes   int64         // Aggregated size of all scanned regular files
	ErrorsCount  int64         // Non-fatal permission or access errors encountered
	PrunedCount  int64         // Stale file rows pruned from DB because they were deleted on disk
	ScanStart    time.Time     // Timestamp when scan initiated
	Duration     time.Duration // Time elapsed since scan started
}

// Scanner orchestrates the concurrent directory traversal.
type Scanner struct {
	config ScanConfig
	stats  Stats
}

// New creates a new Scanner instance with validated configurations.
func New(cfg ScanConfig) *Scanner {
	if cfg.NumWorkers <= 0 {
		cfg.NumWorkers = runtime.NumCPU()
	}
	if cfg.ChannelBuffer <= 0 {
		cfg.ChannelBuffer = 2048
	}
	return &Scanner{config: cfg}
}

// Scan traverses the configured root path, streaming discovered FileMetadata into outChan.
func (s *Scanner) Scan(ctx context.Context, outChan chan<- models.FileMetadata) (Stats, error) {
	startTime := time.Now()

	absRoot, err := filepath.Abs(s.config.RootPath)
	if err != nil {
		return s.stats, fmt.Errorf("invalid root path %q: %w", s.config.RootPath, err)
	}

	rootInfo, err := os.Lstat(absRoot)
	if err != nil {
		return s.stats, fmt.Errorf("cannot access root path %q: %w", absRoot, err)
	}
	if !rootInfo.IsDir() {
		return s.stats, fmt.Errorf("specified root path %q is not a directory", absRoot)
	}

	dirChan := make(chan string, 10000)

	var activeTasks int64
	atomic.AddInt64(&activeTasks, 1)
	dirChan <- absRoot

	var (
		filesScanned int64
		dirsScanned  int64
		totalBytes   int64
		errorsCount  int64
		workerWg     sync.WaitGroup
	)

	for w := 0; w < s.config.NumWorkers; w++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()

			for {
				select {
				case <-ctx.Done():
					return

				case currentDir, ok := <-dirChan:
					if !ok {
						return
					}

					s.processDirectory(
						ctx,
						currentDir,
						startTime,
						dirChan,
						outChan,
						&activeTasks,
						&filesScanned,
						&dirsScanned,
						&totalBytes,
						&errorsCount,
					)

					remaining := atomic.AddInt64(&activeTasks, -1)
					if remaining == 0 {
						select {
						case <-ctx.Done():
						default:
							closeWorkQueue(dirChan)
						}
					}
				}
			}
		}(w)
	}

	workerWg.Wait()

	s.stats = Stats{
		FilesScanned: atomic.LoadInt64(&filesScanned),
		DirsScanned:  atomic.LoadInt64(&dirsScanned),
		TotalBytes:   atomic.LoadInt64(&totalBytes),
		ErrorsCount:  atomic.LoadInt64(&errorsCount),
		ScanStart:    startTime,
		Duration:     time.Since(startTime),
	}

	return s.stats, ctx.Err()
}

// processDirectory reads directory entries, sends discovered subdirs to dirChan,
// and streams regular file metadata to outChan.
func (s *Scanner) processDirectory(
	ctx context.Context,
	dirPath string,
	scanStartTime time.Time,
	dirChan chan string,
	outChan chan<- models.FileMetadata,
	activeTasks *int64,
	filesScanned *int64,
	dirsScanned *int64,
	totalBytes *int64,
	errorsCount *int64,
) {
	atomic.AddInt64(dirsScanned, 1)

	f, err := os.Open(dirPath)
	if err != nil {
		atomic.AddInt64(errorsCount, 1)
		return
	}
	defer f.Close()

	names, err := f.Readdirnames(-1)
	if err != nil {
		atomic.AddInt64(errorsCount, 1)
		return
	}

	for _, name := range names {
		select {
		case <-ctx.Done():
			return
		default:
		}

		fullPath := filepath.Join(dirPath, name)

		info, err := os.Lstat(fullPath)
		if err != nil {
			atomic.AddInt64(errorsCount, 1)
			continue
		}

		mode := info.Mode()

		if mode.IsDir() {
			atomic.AddInt64(activeTasks, 1)
			select {
			case dirChan <- fullPath:
			case <-ctx.Done():
				return
			}
			continue
		}

		if !mode.IsRegular() {
			continue
		}

		meta := extractLinuxMetadata(fullPath, info, scanStartTime)

		atomic.AddInt64(filesScanned, 1)
		atomic.AddInt64(totalBytes, meta.Size)

		select {
		case outChan <- meta:
		case <-ctx.Done():
			return
		}
	}
}

// extractLinuxMetadata pulls Inode, Atime, Mtime, and classifies system path categories.
func extractLinuxMetadata(path string, info os.FileInfo, scanTime time.Time) models.FileMetadata {
	size := info.Size()
	mtime := info.ModTime()
	atime := mtime
	var inode uint64

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		inode = uint64(stat.Ino)
		atime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
		mtime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
	}

	ext := strings.ToLower(filepath.Ext(path))
	isSystem, category := ClassifyPath(path, ext)

	return models.FileMetadata{
		Path:          path,
		Size:          size,
		Mtime:         mtime,
		Atime:         atime,
		Inode:         inode,
		Extension:     ext,
		IsSystem:      isSystem,
		Category:      category,
		LastScannedAt: scanTime,
	}
}

// ClassifyPath categorizes files into system protected, system log, temp, crash dumps, or user files.
func ClassifyPath(path string, ext string) (isSystem bool, category models.FileCategory) {
	cleanPath := filepath.ToSlash(path)

	// 1. Crash Dumps & Application Core Snapshots
	if strings.Contains(cleanPath, "/var/crash/") ||
		strings.Contains(cleanPath, "/systemd/coredump/") ||
		strings.Contains(cleanPath, "/CrashDumps/") ||
		ext == ".core" || ext == ".dmp" || ext == ".crash" {
		return true, models.CategoryCrashDump
	}

	// 2. System and Application Logs
	if strings.Contains(cleanPath, "/var/log/") ||
		strings.Contains(cleanPath, "/logs/") ||
		strings.HasSuffix(cleanPath, ".log") ||
		strings.Contains(cleanPath, ".log.") {
		isSys := strings.Contains(cleanPath, "/var/") || strings.Contains(cleanPath, "/usr/") || !strings.Contains(cleanPath, "/home/")
		return isSys, models.CategorySystemLog
	}

	// 3. System and Package Caches
	if strings.Contains(cleanPath, "/var/cache/") || strings.Contains(cleanPath, "/var/spool/") {
		return true, models.CategorySystemCache
	}

	// 4. Critical Protected OS Files (Never allow deletion of OS core binaries/libraries)
	if strings.Contains(cleanPath, "/usr/bin/") || strings.Contains(cleanPath, "/usr/sbin/") ||
		strings.Contains(cleanPath, "/usr/lib/") || strings.Contains(cleanPath, "/usr/lib64/") ||
		strings.Contains(cleanPath, "/bin/") || strings.Contains(cleanPath, "/sbin/") ||
		strings.Contains(cleanPath, "/lib/") || strings.Contains(cleanPath, "/lib64/") ||
		strings.Contains(cleanPath, "/etc/") || strings.Contains(cleanPath, "/boot/") ||
		strings.Contains(cleanPath, "/sys/") || strings.Contains(cleanPath, "/proc/") ||
		strings.Contains(cleanPath, "/dev/") {
		return true, models.CategorySystemProtected
	}

	// 5. Temporary files (/tmp, /var/tmp)
	if strings.HasPrefix(cleanPath, "/tmp/") || strings.Contains(cleanPath, "/var/tmp/") || ext == ".tmp" || ext == ".temp" {
		return true, models.CategoryTemp
	}

	// 6. Generic system files under /usr, /opt, /var
	if strings.Contains(cleanPath, "/usr/") || strings.Contains(cleanPath, "/opt/") || strings.Contains(cleanPath, "/var/") {
		return true, models.CategorySystemProtected
	}

	// 7. Default User Files
	return false, models.CategoryUser
}

// closeWorkQueue safely drains and closes the directory work channel.
func closeWorkQueue(ch chan string) {
	defer func() {
		_ = recover()
	}()
	close(ch)
}
