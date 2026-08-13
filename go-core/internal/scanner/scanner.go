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
// SYSTEMS PROGRAMMING DEEP DIVE:
// 1. os.Lstat vs os.Stat:
//    - os.Stat() follows symbolic links (dereferencing the target). If symlinks
//      point to ancestor directories (circular links), a recursive walker would
//      spin in an infinite loop and exhaust stack memory.
//    - os.Lstat() inspects the link/inode itself WITHOUT following it. We detect
//      symlinks via (mode & os.ModeSymlink != 0) and safely skip them.
//
// 2. Extracting Linux-Specific Inodes and Timestamps via syscall.Stat_t:
//    - Go's os.FileInfo interface abstracts across Windows, macOS, and Linux.
//    - On Linux, fileInfo.Sys() exposes the underlying POSIX `struct stat` (syscall.Stat_t).
//    - stat.Ino: The unique Inode number on the filesystem. Multiple files sharing
//      the same Inode represent hard links.
//    - stat.Atim (access time) and stat.Mtim (modification time) provide nanosecond
//      resolution timestamps from the Linux VFS (Virtual File System).
//
// 3. Concurrency Model: Bounded Worker Pool with Dynamic Work Discovery:
//    - Challenge: A directory walker discovers new directory tasks dynamically as it runs.
//    - Naive approach (`go walkDir(...)` for every directory) causes Goroutine Explosion
//      and crashes with `EMFILE: too many open files` when scanning large trees.
//    - Our Solution: Fixed N workers (runtime.NumCPU()) reading from a work channel.
//    - Termination tracking: We maintain an atomic in-flight task counter. When the
//      in-flight counter drops to zero, all subdirectories have been fully traversed.
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
// It blocks until the scan completes or the context is canceled.
func (s *Scanner) Scan(ctx context.Context, outChan chan<- models.FileMetadata) (Stats, error) {
	startTime := time.Now()

	// Clean and verify root path
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

	// Work channel for distributing directory paths to workers
	// Sized with reasonable capacity to prevent worker stalls on broad directory trees
	dirChan := make(chan string, 10000)

	// activeTasks tracks total directories currently queued or being processed by workers.
	// When activeTasks hits 0, traversal is complete.
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

	// Launch bounded pool of worker goroutines
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

					// Process the directory
					s.processDirectory(
						ctx,
						currentDir,
						dirChan,
						outChan,
						&activeTasks,
						&filesScanned,
						&dirsScanned,
						&totalBytes,
						&errorsCount,
					)

					// Decrement active task counter for this directory
					remaining := atomic.AddInt64(&activeTasks, -1)
					if remaining == 0 {
						// All directories traversed! Close dirChan so workers finish cleanly.
						// Close is guarded by safe closing logic
						select {
						case <-ctx.Done():
						default:
							// Use non-blocking close trigger
							closeWorkQueue(dirChan)
						}
					}
				}
			}
		}(w)
	}

	// Wait for all worker goroutines to complete
	workerWg.Wait()

	s.stats = Stats{
		FilesScanned: atomic.LoadInt64(&filesScanned),
		DirsScanned:  atomic.LoadInt64(&dirsScanned),
		TotalBytes:   atomic.LoadInt64(&totalBytes),
		ErrorsCount:  atomic.LoadInt64(&errorsCount),
		Duration:     time.Since(startTime),
	}

	return s.stats, ctx.Err()
}

// processDirectory reads directory entries, sends discovered subdirs to dirChan,
// and streams regular file metadata to outChan.
func (s *Scanner) processDirectory(
	ctx context.Context,
	dirPath string,
	dirChan chan string,
	outChan chan<- models.FileMetadata,
	activeTasks *int64,
	filesScanned *int64,
	dirsScanned *int64,
	totalBytes *int64,
	errorsCount *int64,
) {
	atomic.AddInt64(dirsScanned, 1)

	// Open the directory descriptor
	f, err := os.Open(dirPath)
	if err != nil {
		// Gracefully handle permission denied or unreadable directories
		atomic.AddInt64(errorsCount, 1)
		return
	}
	defer f.Close()

	// ReadDirnames reads entry names without immediately stat-ing everything at once
	names, err := f.Readdirnames(-1)
	if err != nil {
		atomic.AddInt64(errorsCount, 1)
		return
	}

	scanTimestamp := time.Now()

	for _, name := range names {
		// Check context cancellation between items
		select {
		case <-ctx.Done():
			return
		default:
		}

		fullPath := filepath.Join(dirPath, name)

		// Call os.Lstat (does not follow symlinks)
		info, err := os.Lstat(fullPath)
		if err != nil {
			atomic.AddInt64(errorsCount, 1)
			continue
		}

		mode := info.Mode()

		// 1. Subdirectory handling: enqueue to worker pool
		if mode.IsDir() {
			atomic.AddInt64(activeTasks, 1)
			select {
			case dirChan <- fullPath:
			case <-ctx.Done():
				return
			}
			continue
		}

		// 2. Ignore non-regular files (symlinks, named pipes/FIFOs, sockets, unix devices)
		if !mode.IsRegular() {
			continue
		}

		// 3. Extract Linux POSIX metadata from underlying syscall.Stat_t
		meta := extractLinuxMetadata(fullPath, info, scanTimestamp)

		atomic.AddInt64(filesScanned, 1)
		atomic.AddInt64(totalBytes, meta.Size)

		// Send to SQLite single-writer funnel channel (applies backpressure if channel buffer fills)
		select {
		case outChan <- meta:
		case <-ctx.Done():
			return
		}
	}
}

// extractLinuxMetadata pulls Inode, Atime, Mtime, and size from os.FileInfo and syscall.Stat_t.
func extractLinuxMetadata(path string, info os.FileInfo, scanTime time.Time) models.FileMetadata {
	size := info.Size()
	mtime := info.ModTime()
	atime := mtime // Default fallback if syscall.Stat_t is unavailable
	var inode uint64

	// Extract Linux-specific fields from VFS stat structure
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		inode = uint64(stat.Ino)
		atime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
		mtime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
	}

	ext := strings.ToLower(filepath.Ext(path))

	return models.FileMetadata{
		Path:          path,
		Size:          size,
		Mtime:         mtime,
		Atime:         atime,
		Inode:         inode,
		Extension:     ext,
		LastScannedAt: scanTime,
	}
}

// closeWorkQueue safely drains and closes the directory work channel.
func closeWorkQueue(ch chan string) {
	defer func() {
		_ = recover() // Catch any panic if already closed
	}()
	close(ch)
}
