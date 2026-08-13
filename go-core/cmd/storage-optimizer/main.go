package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/dedup"
	"storage-optimizer/go-core/internal/models"
	"storage-optimizer/go-core/internal/scanner"
	"storage-optimizer/go-core/internal/stale"
)

// ============================================================================
// CLI ENTRY POINT & PIPELINE ORCHESTRATION
//
// DATA & CALL FLOW:
// [CLI Command Router]
//       ├── "scan"       -> [Scanner Worker Pool] -> [metaChan] -> [DB BatchWriter] -> [Prune Deleted Files]
//       ├── "duplicates" -> [DB Size Buckets (Pass 1)] -> [SHA-256 Workers (Pass 2)] -> [Dedup Report]
//       ├── "stale"      -> [Staleness Scoring Engine] -> [mtime/atime Decay Matrix] -> [Stale Report]
//       ├── "snapshots"  -> [DB Snapshot History] -> [Time-Series Metric Log] (Phase 4)
//       └── "delete"     -> [Action & Audit Engine] (Phase 6)
// ============================================================================

// ANSI color escape codes for terminal feedback
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Green   = "\033[32m"
	Cyan    = "\033[36m"
	Yellow  = "\033[33m"
	Red     = "\033[31m"
	Magenta = "\033[35m"
	Gray    = "\033[90m"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "scan":
		handleScan(os.Args[2:])
	case "duplicates":
		handleDuplicates(os.Args[2:])
	case "stale":
		handleStale(os.Args[2:])
	case "snapshots":
		handleSnapshots(os.Args[2:])
	case "delete":
		fmt.Println(Yellow + "[Phase 6] Action execution CLI will be activated in Phase 6." + Reset)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Printf(Red+"Unknown command: %q\n"+Reset, command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(Bold + "Intelligent Storage Optimizer (Linux)" + Reset)
	fmt.Println("Usage: storage-optimizer <command> [options]")
	fmt.Println("\nCommands:")
	fmt.Println("  scan <path>          Scan directory tree, prune deleted rows, and update metadata")
	fmt.Println("  duplicates           Identify duplicate files and calculate wasted disk space")
	fmt.Println("  stale --days N       Identify stale files, forgotten logs, temp junk & crash snapshots")
	fmt.Println("  snapshots            View historical scan snapshots for time-series growth tracking")
	fmt.Println("  delete --ids ...     Execute user-confirmed file cleanup (Phase 6)")
	fmt.Println("\nGlobal Flags:")
	fmt.Println("  --workers N          Number of concurrent workers (default: NumCPU)")
	fmt.Println("  --db <path>          Path to SQLite database (default: ../data/optimizer.db)")
	fmt.Println("  --schema <path>      Path to shared/schema.sql (default: ../shared/schema.sql)")
	fmt.Println("  --full               Force full re-hash / full re-scan")
}

func handleScan(args []string) {
	scanCmd := flag.NewFlagSet("scan", flag.ExitOnError)
	workersFlag := scanCmd.Int("workers", runtime.NumCPU(), "Number of scanner worker goroutines")
	dbFlag := scanCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := scanCmd.String("schema", "", "Path to schema.sql file")
	noPruneFlag := scanCmd.Bool("no-prune", false, "Disable pruning of deleted files")
	_ = scanCmd.Bool("full", false, "Force full re-scan")

	if err := scanCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	targetPath := scanCmd.Arg(0)
	if targetPath == "" {
		fmt.Println(Red + "Error: target path to scan is required. Example: storage-optimizer scan /path/to/scan" + Reset)
		os.Exit(1)
	}

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = findDefaultPath("data/optimizer.db")
	}

	schemaPath := *schemaFlag
	if schemaPath == "" {
		schemaPath = findDefaultPath("shared/schema.sql")
	}

	ctx, cancel := setupSignalContext()
	defer cancel()

	fmt.Printf(Bold+Cyan+"==> Initializing SQLite Database:"+Reset+" %s\n", dbPath)
	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		fmt.Printf(Red+"[FATAL] Failed to initialize SQLite database: %v\n"+Reset, err)
		os.Exit(1)
	}
	defer database.Close()

	metaChan := make(chan models.FileMetadata, 4096)
	errChan := make(chan error, 16)

	writerDone := make(chan struct{})
	go func() {
		database.BatchWriter(ctx, metaChan, 500, 50*time.Millisecond, errChan)
		close(writerDone)
	}()

	fmt.Printf(Bold+Cyan+"==> Starting Concurrent Scan:"+Reset+" %s (Workers: %d)\n", targetPath, *workersFlag)

	s := scanner.New(scanner.ScanConfig{
		RootPath:      targetPath,
		NumWorkers:    *workersFlag,
		ChannelBuffer: 4096,
	})

	stats, scanErr := s.Scan(ctx, metaChan)
	close(metaChan)
	<-writerDone

	if scanErr != nil && scanErr != context.Canceled {
		fmt.Printf(Red+"[ERROR] Scan error: %v\n"+Reset, scanErr)
	}

	absTarget, _ := filepath.Abs(targetPath)

	// Prune dead files from SQLite that were deleted on disk
	var prunedCount int64
	if !*noPruneFlag {
		pCount, pruneErr := database.PruneDeletedFiles(ctx, absTarget, stats.ScanStart)
		if pruneErr != nil {
			fmt.Printf(Yellow+"[WARN] Error pruning deleted records: %v\n"+Reset, pruneErr)
		} else {
			prunedCount = pCount
		}
	}

	snapshotID, err := database.RecordSnapshot(ctx, absTarget, stats.FilesScanned, stats.TotalBytes)
	if err != nil {
		fmt.Printf(Yellow+"[WARN] Failed to record scan snapshot: %v\n"+Reset, err)
	}

	elapsed := stats.Duration
	filesPerSec := float64(stats.FilesScanned) / elapsed.Seconds()
	mbPerSec := (float64(stats.TotalBytes) / (1024 * 1024)) / elapsed.Seconds()

	fmt.Println("\n" + Bold + Green + "=== Scan & Incremental Sync Completed ===" + Reset)
	fmt.Printf("• Root Directory:   %s\n", absTarget)
	fmt.Printf("• Files Indexed:    %d\n", stats.FilesScanned)
	fmt.Printf("• Dirs Traversed:   %d\n", stats.DirsScanned)
	fmt.Printf("• Total Size:       %s (%d bytes)\n", formatBytes(stats.TotalBytes), stats.TotalBytes)
	if prunedCount > 0 {
		fmt.Printf("• Stale Rows Pruned:%s %d deleted files purged from index%s\n", Bold+Yellow, prunedCount, Reset)
	}
	fmt.Printf("• Permission Skips: %d\n", stats.ErrorsCount)
	fmt.Printf("• Time Elapsed:     %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("• Throughput:       %.1f files/sec | %.2f MB/sec\n", filesPerSec, mbPerSec)
	if snapshotID > 0 {
		fmt.Printf("• Snapshot ID:      #%d (recorded for Python forecasting)\n", snapshotID)
	}
}

func handleDuplicates(args []string) {
	dupCmd := flag.NewFlagSet("duplicates", flag.ExitOnError)
	workersFlag := dupCmd.Int("workers", runtime.NumCPU(), "Number of hashing worker goroutines")
	dbFlag := dupCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := dupCmd.String("schema", "", "Path to schema.sql file")
	fullFlag := dupCmd.Bool("full", false, "Force re-hashing all candidate files")

	if err := dupCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = findDefaultPath("data/optimizer.db")
	}

	schemaPath := *schemaFlag
	if schemaPath == "" {
		schemaPath = findDefaultPath("shared/schema.sql")
	}

	ctx, cancel := setupSignalContext()
	defer cancel()

	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		fmt.Printf(Red+"[FATAL] Failed to open SQLite database: %v\n"+Reset, err)
		os.Exit(1)
	}
	defer database.Close()

	fmt.Printf(Bold+Cyan+"==> Executing Two-Pass Duplicate Detection..."+Reset+" (Workers: %d)\n", *workersFlag)

	engine := dedup.New(database, dedup.Config{
		NumWorkers:  *workersFlag,
		ForceRehash: *fullFlag,
	})

	report, err := engine.Execute(ctx)
	if err != nil {
		fmt.Printf(Red+"[ERROR] Duplicate detection failed: %v\n"+Reset, err)
		os.Exit(1)
	}

	printDuplicateReport(report)
}

func handleStale(args []string) {
	staleCmd := flag.NewFlagSet("stale", flag.ExitOnError)
	daysFlag := staleCmd.Int("days", 30, "Minimum days untouched (mtime/atime threshold)")
	minScoreFlag := staleCmd.Float64("min-score", 0.05, "Minimum staleness score [0.0 to 1.0]")
	limitFlag := staleCmd.Int("limit", 50, "Maximum number of stale files to display")
	workersFlag := staleCmd.Int("workers", runtime.NumCPU(), "Number of worker goroutines")
	dbFlag := staleCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := staleCmd.String("schema", "", "Path to schema.sql file")

	if err := staleCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = findDefaultPath("data/optimizer.db")
	}

	schemaPath := *schemaFlag
	if schemaPath == "" {
		schemaPath = findDefaultPath("shared/schema.sql")
	}

	ctx, cancel := setupSignalContext()
	defer cancel()

	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		fmt.Printf(Red+"[FATAL] Failed to open SQLite database: %v\n"+Reset, err)
		os.Exit(1)
	}
	defer database.Close()

	fmt.Printf(Bold+Cyan+"==> Computing Staleness Scores & Categorizing System Storage..."+Reset+" (Threshold: %d+ days)\n", *daysFlag)

	engine := stale.New(database, stale.Config{
		NumWorkers: *workersFlag,
	})

	report, err := engine.FindStaleFiles(ctx, *daysFlag, *minScoreFlag, *limitFlag)
	if err != nil {
		fmt.Printf(Red+"[ERROR] Staleness analysis failed: %v\n"+Reset, err)
		os.Exit(1)
	}

	printStaleReport(report)
}

func handleSnapshots(args []string) {
	snapCmd := flag.NewFlagSet("snapshots", flag.ExitOnError)
	limitFlag := snapCmd.Int("limit", 20, "Number of snapshots to display")
	dbFlag := snapCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := snapCmd.String("schema", "", "Path to schema.sql file")

	if err := snapCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = findDefaultPath("data/optimizer.db")
	}

	schemaPath := *schemaFlag
	if schemaPath == "" {
		schemaPath = findDefaultPath("shared/schema.sql")
	}

	ctx, cancel := setupSignalContext()
	defer cancel()

	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		fmt.Printf(Red+"[FATAL] Failed to open SQLite database: %v\n"+Reset, err)
		os.Exit(1)
	}
	defer database.Close()

	snapshots, err := database.GetSnapshots(ctx, *limitFlag)
	if err != nil {
		fmt.Printf(Red+"[ERROR] Failed to query snapshots: %v\n"+Reset, err)
		os.Exit(1)
	}

	fmt.Println("\n" + Bold + Green + "=== Historical Scan Snapshots (Time-Series for Python Layer) ===" + Reset)
	fmt.Printf("• Total Snapshots Recorded: %d\n\n", len(snapshots))

	if len(snapshots) == 0 {
		fmt.Println(Yellow + "No scan snapshots recorded yet. Run 'storage-optimizer scan <path>' first." + Reset)
		return
	}

	fmt.Printf(Bold+"%-6s  %-20s  %-12s  %-14s  %s\n"+Reset, "ID", "TIMESTAMP", "FILES", "STORAGE", "ROOT PATH")
	fmt.Println(Gray + "----------------------------------------------------------------------------------------" + Reset)

	for _, s := range snapshots {
		fmt.Printf("#%-5d  %-20s  %-12d  %-14s  %s%s%s\n",
			s.ID,
			s.ScannedAt.Format("2006-01-02 15:04:05"),
			s.TotalFiles,
			formatBytes(s.TotalBytes),
			Cyan,
			s.RootPath,
			Reset,
		)
	}
	fmt.Println(Gray + "----------------------------------------------------------------------------------------" + Reset)
}

func printDuplicateReport(report *models.DedupReport) {
	fmt.Println("\n" + Bold + Green + "=== Duplicate Detection Summary ===" + Reset)
	fmt.Printf("• Total Duplicate Groups: %d\n", report.TotalGroups)
	fmt.Printf("• Redundant Copies Found: %d\n", report.TotalDuplicateFiles)
	fmt.Printf("• Wasted Storage Space:   %s%s%s (%d bytes)\n", Bold+Red, formatBytes(report.TotalWastedBytes), Reset, report.TotalWastedBytes)
	fmt.Printf("• Analysis Duration:      %s\n", report.Duration.Round(time.Millisecond))

	if len(report.Groups) == 0 {
		fmt.Println(Green + "\n✨ No duplicate files found on the scanned filesystem!" + Reset)
		return
	}

	fmt.Println("\n" + Bold + "Duplicate Clusters (Sorted by Reclaimable Wasted Storage):" + Reset)
	fmt.Println(Gray + "--------------------------------------------------------------------------------" + Reset)

	for i, group := range report.Groups {
		shortHash := group.ContentHash
		if len(shortHash) > 16 {
			shortHash = shortHash[:16] + "..."
		}

		fmt.Printf(Bold+Magenta+"[Group #%d]"+Reset+" SHA256: %s | Size: %s | Copies: %d | Wasted: %s%s%s\n",
			i+1,
			shortHash,
			formatBytes(group.FileSize),
			group.DuplicateCount,
			Bold+Yellow,
			formatBytes(group.WastedBytes),
			Reset,
		)

		for _, f := range group.Files {
			catTag := formatCategoryBadge(f.Category, f.IsSystem)
			fmt.Printf("   ├─ [ID: %4d] %s %s (Inode: %d, Mtime: %s)\n",
				f.ID,
				catTag,
				f.Path,
				f.Inode,
				f.Mtime.Format("2006-01-02 15:04:05"),
			)
		}
		fmt.Println(Gray + "--------------------------------------------------------------------------------" + Reset)
	}
}

func printStaleReport(report *models.StaleReport) {
	fmt.Println("\n" + Bold + Green + "=== Staleness & Inactive Storage Summary ===" + Reset)
	fmt.Printf("• Age Threshold:        %d+ days untouched\n", report.ThresholdDays)
	fmt.Printf("• Stale Files Found:    %d\n", report.TotalFiles)
	fmt.Printf("• Total Stale Storage:  %s%s%s (%d bytes)\n", Bold+Yellow, formatBytes(report.TotalBytes), Reset, report.TotalBytes)
	fmt.Printf("• Computation Time:     %s\n", report.Duration.Round(time.Millisecond))

	if len(report.Files) == 0 {
		fmt.Printf(Green+"\n✨ No stale files found untouched for %d+ days.\n"+Reset, report.ThresholdDays)
		return
	}

	fmt.Println("\n" + Bold + "Top Stale Candidates (Ranked by Staleness Score & Size):" + Reset)
	fmt.Println(Gray + "--------------------------------------------------------------------------------" + Reset)

	now := time.Now()
	for i, f := range report.Files {
		ageDays := int(now.Sub(f.Mtime).Hours() / 24)

		scoreColor := Cyan
		if f.StalenessScore >= 0.70 {
			scoreColor = Red
		} else if f.StalenessScore >= 0.40 {
			scoreColor = Yellow
		}

		catTag := formatCategoryBadge(f.Category, f.IsSystem)

		fmt.Printf(Bold+"[%2d]"+Reset+" Score: %s%.2f%s | Size: %8s | Age: %4d days | %s\n     Path: %s%s%s\n",
			i+1,
			scoreColor,
			f.StalenessScore,
			Reset,
			formatBytes(f.Size),
			ageDays,
			catTag,
			Gray,
			f.Path,
			Reset,
		)
	}
	fmt.Println(Gray + "--------------------------------------------------------------------------------" + Reset)
}

func formatCategoryBadge(cat models.FileCategory, isSystem bool) string {
	switch cat {
	case models.CategoryCrashDump:
		return Red + "[CRASH DUMP]" + Reset
	case models.CategorySystemLog:
		return Yellow + "[SYS LOG]" + Reset
	case models.CategoryTemp:
		return Magenta + "[TEMP JUNK]" + Reset
	case models.CategorySystemCache:
		return Cyan + "[SYS CACHE]" + Reset
	case models.CategorySystemProtected:
		return Bold + Red + "[PROTECTED OS]" + Reset
	default:
		if isSystem {
			return Gray + "[SYSTEM]" + Reset
		}
		return Gray + "[USER]" + Reset
	}
}

func setupSignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println(Yellow + "\n[!] Interrupt signal received. Gracefully stopping..." + Reset)
		cancel()
	}()
	return ctx, cancel
}

func findDefaultPath(relPath string) string {
	candidates := []string{
		relPath,
		filepath.Join("..", relPath),
		filepath.Join("../..", relPath),
		filepath.Join("/home/blazex/Documents/git/storage-optimizer", relPath),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Dir(c)); err == nil {
			return c
		}
	}
	return relPath
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
