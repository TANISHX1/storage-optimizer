package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"storage-optimizer/go-core/internal/action"
	"storage-optimizer/go-core/internal/api"
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
//       ├── "serve"      -> [HTTP REST API Server] (Phase 5)
//       ├── "delete"     -> [Action & Audit Engine: XDG Trash / Perm Delete] (Phase 6)
//       └── "restore"    -> [Action Engine: Restore from XDG Trash] (Phase 6)
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
	case "serve", "api":
		handleServe(os.Args[2:])
	case "delete":
		handleDelete(os.Args[2:])
	case "restore":
		handleRestore(os.Args[2:])
	case "actions", "history":
		handleActionsLog(os.Args[2:])
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
	fmt.Println("  serve / api          Start local HTTP REST API server on 127.0.0.1:8080 (Phase 5)")
	fmt.Println("  scan <path>          Scan directory tree, prune deleted rows, and update metadata")
	fmt.Println("  duplicates           Identify duplicate files and calculate wasted disk space")
	fmt.Println("  stale --days N       Identify stale files, forgotten logs, temp junk & crash snapshots")
	fmt.Println("  snapshots            View historical scan snapshots for time-series growth tracking")
	fmt.Println("  delete --ids ...     Execute user-confirmed file cleanup (XDG Trash or Permanent)")
	fmt.Println("  restore --id N       Restore a previously trashed file back to its original path")
	fmt.Println("  actions              View immutable audit log history of past cleanup actions")
	fmt.Println("\nGlobal Flags:")
	fmt.Println("  --mode [trash|perm]  Deletion strategy ('trash' moves to OS XDG Trash, 'permanent' destroys)")
	fmt.Println("  --ids 1,2,3          Comma-separated list of file IDs to mutate")
	fmt.Println("  --id N               Action log ID to restore")
	fmt.Println("  --port N             Port for HTTP REST API server (default: 8080)")
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

	// Precompute staleness scores & duplicate clusters
	staleEngine := stale.New(database, stale.Config{NumWorkers: *workersFlag})
	_, _ = staleEngine.ComputeAndPersistScores(ctx)

	dedupEngine := dedup.New(database, dedup.Config{NumWorkers: *workersFlag})
	_, _ = dedupEngine.Execute(ctx)

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

func handleServe(args []string) {
	serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
	portFlag := serveCmd.Int("port", 8080, "Port to listen on for HTTP REST API")
	dbFlag := serveCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := serveCmd.String("schema", "", "Path to schema.sql file")

	if err := serveCmd.Parse(args); err != nil {
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

	fmt.Printf(Bold+Cyan+"==> Initializing SQLite Database:"+Reset+" %s\n", dbPath)
	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		fmt.Printf(Red+"[FATAL] Failed to initialize SQLite database: %v\n"+Reset, err)
		os.Exit(1)
	}
	defer database.Close()

	server := api.New(database, *portFlag)

	fmt.Println("\n" + Bold + Green + "=== Storage Optimizer HTTP REST API Server (Phase 5) ===" + Reset)
	fmt.Printf("• Base Endpoint:     %shttp://127.0.0.1:%d/api/v1%s\n", Bold+Cyan, *portFlag, Reset)
	fmt.Println("• Active Endpoints:")
	fmt.Println("  ├─ GET  /api/v1/health             -> Health check and service status")
	fmt.Println("  ├─ GET  /api/v1/stats              -> Storage overview & category breakdowns")
	fmt.Println("  ├─ POST /api/v1/scan               -> Start async background directory scan")
	fmt.Println("  ├─ GET  /api/v1/scan/status        -> Poll real-time / last-completed scan status")
	fmt.Println("  ├─ GET  /api/v1/files/duplicates   -> Find duplicate clusters and wasted bytes")
	fmt.Println("  ├─ GET  /api/v1/files/stale        -> Rank inactive/stale files (query: days, min_score, limit)")
	fmt.Println("  ├─ GET  /api/v1/snapshots          -> Time-series snapshots for Sahil's Python forecasting")
	fmt.Println("\n" + Gray + "Press Ctrl+C to stop the API server." + Reset)

	if err := server.Start(ctx); err != nil {
		fmt.Printf(Red+"[API ERROR] %v\n"+Reset, err)
	}
}

func handleDelete(args []string) {
	delCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	idsFlag := delCmd.String("ids", "", "Comma-separated file record IDs to delete (e.g. --ids 42,98)")
	modeFlag := delCmd.String("mode", "trash", "Deletion strategy: 'trash' (moves to OS XDG Trash) or 'permanent' (os.Remove)")
	dbFlag := delCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := delCmd.String("schema", "", "Path to schema.sql file")

	if err := delCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	if *idsFlag == "" {
		fmt.Println(Red + "Error: Missing required --ids flag. Example: storage-optimizer delete --ids 12,15 --mode trash" + Reset)
		os.Exit(1)
	}

	rawIDs := strings.Split(*idsFlag, ",")
	var ids []int64
	for _, raw := range rawIDs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			fmt.Printf(Red+"Error: invalid ID %q: must be integer\n"+Reset, trimmed)
			os.Exit(1)
		}
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		fmt.Println(Yellow + "No valid IDs provided." + Reset)
		return
	}

	mode := models.ActionMode(*modeFlag)
	if mode != models.ActionModeTrash && mode != models.ActionModePermanent {
		fmt.Printf(Red+"Error: invalid mode %q. Choose 'trash' or 'permanent'.\n"+Reset, *modeFlag)
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

	engine := action.New(database)
	fmt.Printf(Bold+Cyan+"==> Executing Action:"+Reset+" %d files -> Mode: %s%s%s\n", len(ids), Bold+Yellow, mode, Reset)

	resp, err := engine.Execute(ctx, models.ActionRequest{
		IDs:  ids,
		Mode: mode,
	})
	if err != nil {
		fmt.Printf(Red+"[ERROR] Cleanup action failed: %v\n"+Reset, err)
		os.Exit(1)
	}

	fmt.Println("\n" + Bold + Green + "=== Cleanup Action Execution Summary ===" + Reset)
	fmt.Printf("• Mode:            %s\n", resp.Mode)
	fmt.Printf("• Processed Files: %d / %d\n", resp.ProcessedCount, len(ids))
	fmt.Printf("• Reclaimed Space: %s%s%s (%d bytes)\n", Bold+Green, formatBytes(resp.FreedBytes), Reset, resp.FreedBytes)

	if len(resp.Actions) > 0 {
		fmt.Println("\n" + Bold + "Action Log Entries:" + Reset)
		fmt.Println(Gray + "----------------------------------------------------------------------------------------" + Reset)
		for _, a := range resp.Actions {
			if a.ActionMode == models.ActionModeTrash && a.TrashedToPath != nil {
				fmt.Printf("• [Action #%d] %s -> %s (Size: %s)\n", a.ID, a.FilePath, *a.TrashedToPath, formatBytes(a.FileSize))
			} else {
				fmt.Printf("• [Action #%d] %s -> PERMANENTLY REMOVED (Size: %s)\n", a.ID, a.FilePath, formatBytes(a.FileSize))
			}
		}
		fmt.Println(Gray + "----------------------------------------------------------------------------------------" + Reset)
	}
}

func handleRestore(args []string) {
	restoreCmd := flag.NewFlagSet("restore", flag.ExitOnError)
	idFlag := restoreCmd.Int64("id", 0, "Action log ID to restore from XDG Trash")
	dbFlag := restoreCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := restoreCmd.String("schema", "", "Path to schema.sql file")

	if err := restoreCmd.Parse(args); err != nil {
		fmt.Println(Red + "Error parsing flags: " + err.Error() + Reset)
		os.Exit(1)
	}

	if *idFlag <= 0 {
		fmt.Println(Red + "Error: Missing or invalid --id flag. Example: storage-optimizer restore --id 12" + Reset)
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

	engine := action.New(database)
	fmt.Printf(Bold+Cyan+"==> Restoring File from XDG Trash:"+Reset+" Action Log #%d\n", *idFlag)

	restoredLog, err := engine.Restore(ctx, *idFlag)
	if err != nil {
		fmt.Printf(Red+"[ERROR] Restoration failed: %v\n"+Reset, err)
		os.Exit(1)
	}

	fmt.Println("\n" + Bold + Green + "=== File Restored Successfully ===" + Reset)
	fmt.Printf("• Original Path: %s%s%s\n", Bold+Cyan, restoredLog.FilePath, Reset)
	fmt.Printf("• File Size:     %s (%d bytes)\n", formatBytes(restoredLog.FileSize), restoredLog.FileSize)
	fmt.Printf("• Restored At:   %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println(Green + "✨ The file has been restored to disk and re-indexed in the database index." + Reset)
}

func handleActionsLog(args []string) {
	logCmd := flag.NewFlagSet("actions", flag.ExitOnError)
	limitFlag := logCmd.Int("limit", 25, "Maximum number of audit logs to display")
	dbFlag := logCmd.String("db", "", "Path to SQLite database file")
	schemaFlag := logCmd.String("schema", "", "Path to schema.sql file")

	if err := logCmd.Parse(args); err != nil {
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

	logs, err := database.GetActionLogs(ctx, *limitFlag)
	if err != nil {
		fmt.Printf(Red+"[ERROR] Failed to query action logs: %v\n"+Reset, err)
		os.Exit(1)
	}

	fmt.Println("\n" + Bold + Green + "=== Immutable Cleanup Audit Logs ===" + Reset)
	fmt.Printf("• Total Action Records: %d\n\n", len(logs))

	if len(logs) == 0 {
		fmt.Println(Yellow + "No file cleanup actions executed yet." + Reset)
		return
	}

	fmt.Printf(Bold+"%-6s  %-10s  %-12s  %-20s  %s\n"+Reset, "ID", "MODE", "SIZE", "TIMESTAMP", "FILE PATH")
	fmt.Println(Gray + "----------------------------------------------------------------------------------------------------" + Reset)

	for _, a := range logs {
		modeBadge := Green + "[TRASH]" + Reset
		if a.ActionMode == models.ActionModePermanent {
			modeBadge = Red + "[PERM]" + Reset
		}

		fmt.Printf("#%-5d  %-10s  %-12s  %-20s  %s\n",
			a.ID,
			modeBadge,
			formatBytes(a.FileSize),
			a.PerformedAt.Format("2006-01-02 15:04:05"),
			a.FilePath,
		)
	}
	fmt.Println(Gray + "----------------------------------------------------------------------------------------------------" + Reset)
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


