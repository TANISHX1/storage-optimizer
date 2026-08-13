package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"storage-optimizer/go-core/internal/models"
)

// ============================================================================
// SQLITE SINGLE-WRITER STORAGE ENGINE
//
// SYSTEMS & CONCURRENCY CONCEPTS:
// 1. Why SQLite Locks on Concurrent Writes:
//    Unlike PostgreSQL or MySQL, SQLite is an in-process, serverless database.
//    Locking happens at the file/database level. Even with WAL (Write-Ahead Logging),
//    only ONE OS thread or process can hold an EXCLUSIVE write lock at any time.
//    If 8 scanner worker goroutines write simultaneously, you get `sqlite3: database is locked`.
//
// 2. The Funnel-to-Single-Writer Pattern:
//    Instead of workers writing to SQLite directly:
//    [Worker 1] --\
//    [Worker 2] ---> [Buffered Channel: chan FileMetadata] ---> [Dedicated DB Writer Goroutine] ---> [SQLite WAL]
//    [Worker N] --/
//    This guarantees serialized write operations without ANY mutex contention on DB handles.
//
// 3. Batching & fsync() Performance:
//    Each individual `tx.Commit()` or un-transactioned `INSERT` requires an OS `fsync()`
//    to persist WAL pages to disk (typically taking 1ms-15ms on SSDs/HDDs).
//    10,000 single inserts = 10,000 * 5ms = 50 seconds.
//    10,000 inserts in batches of 500 = 20 commits = 20 * 5ms = 100 milliseconds (500x speedup!).
// ============================================================================

// DB wraps the SQL connection pool and lifecycle controls.
type DB struct {
	Conn *sql.DB
	path string
	mu   sync.RWMutex
}

// Open initializes the SQLite database, applies performance PRAGMAs,
// and ensures the schema from schema.sql is executed.
func Open(dbPath string, schemaPath string) (*DB, error) {
	// Ensure parent directory exists (e.g. data/)
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
	}

	// Open connection with URI flags
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db at %q: %w", dbPath, err)
	}

	// For SQLite, having 1 open connection for writes and bounded pool for reads prevents driver lock starvation
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	dbInstance := &DB{
		Conn: conn,
		path: dbPath,
	}

	// Optimize SQLite performance pragmas for high-throughput scanning
	if err := dbInstance.applyPragmas(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply pragmas: %w", err)
	}

	// Apply schema migrations
	if err := dbInstance.applySchema(schemaPath); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	return dbInstance, nil
}

// applyPragmas configures the SQLite connection for maximum read/write performance.
func (d *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",       // Write-Ahead Logging: readers do not block writers; writers do not block readers
		"PRAGMA synchronous = NORMAL;",     // Safe with WAL; syncs at checkpoint rather than every single write transaction
		"PRAGMA busy_timeout = 5000;",      // Wait up to 5000ms if busy instead of failing immediately
		"PRAGMA cache_size = -64000;",      // 64MB in-memory page cache (negative number indicates kibibytes)
		"PRAGMA foreign_keys = ON;",        // Enforce referential integrity
		"PRAGMA temp_store = MEMORY;",      // Keep temporary tables/indexes in RAM
	}

	for _, pragma := range pragmas {
		if _, err := d.Conn.Exec(pragma); err != nil {
			return fmt.Errorf("pragma error %q: %w", pragma, err)
		}
	}
	return nil
}

// applySchema reads and executes the canonical schema file (or default fallback).
func (d *DB) applySchema(schemaPath string) error {
	var schemaSQL string

	if schemaPath != "" {
		content, err := os.ReadFile(schemaPath)
		if err == nil {
			schemaSQL = string(content)
		}
	}

	// Built-in fallback matching shared/schema.sql exactly
	if schemaSQL == "" {
		schemaSQL = `
		CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL UNIQUE,
			size INTEGER NOT NULL,
			mtime INTEGER NOT NULL,
			atime INTEGER,
			inode INTEGER,
			extension TEXT,
			content_hash TEXT,
			staleness_score REAL,
			last_scanned_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
		CREATE INDEX IF NOT EXISTS idx_files_hash ON files(content_hash);
		CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(mtime);
		CREATE INDEX IF NOT EXISTS idx_files_staleness ON files(staleness_score);

		CREATE TABLE IF NOT EXISTS scan_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scanned_at INTEGER NOT NULL,
			root_path TEXT NOT NULL,
			total_files INTEGER,
			total_bytes INTEGER
		);

		CREATE INDEX IF NOT EXISTS idx_snapshots_scanned_at ON scan_snapshots(scanned_at);

		CREATE TABLE IF NOT EXISTS actions_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			action_mode TEXT NOT NULL,
			trashed_to_path TEXT,
			file_size INTEGER,
			performed_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_actions_performed_at ON actions_log(performed_at);
		`
	}

	_, err := d.Conn.Exec(schemaSQL)
	return err
}

// Close gracefully closes the SQLite database connection pool.
func (d *DB) Close() error {
	return d.Conn.Close()
}

// BatchWriter runs as a dedicated single-writer goroutine.
// It receives items from `inChan`, collects them into batches of `batchSize`
// (or flushes every `flushInterval`), and executes them inside a single SQLite transaction.
func (d *DB) BatchWriter(
	ctx context.Context,
	inChan <-chan models.FileMetadata,
	batchSize int,
	flushInterval time.Duration,
	errChan chan<- error,
) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if flushInterval <= 0 {
		flushInterval = 50 * time.Millisecond
	}

	buffer := make([]models.FileMetadata, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	// Helper function to flush accumulated batch to SQLite
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		if err := d.UpsertFileBatch(ctx, buffer); err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
		// Reset buffer retaining allocated capacity
		buffer = buffer[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Context canceled: flush any remaining items and exit
			flush()
			return

		case meta, ok := <-inChan:
			if !ok {
				// Input channel closed: final flush and return
				flush()
				return
			}
			buffer = append(buffer, meta)
			if len(buffer) >= batchSize {
				flush()
			}

		case <-ticker.C:
			// Time threshold reached: flush pending items to maintain low latency
			flush()
		}
	}
}

// UpsertFileBatch inserts or updates a slice of file records within a single transaction.
// Using `ON CONFLICT(path) DO UPDATE` ensures incremental scans update mtime/size/inode cleanly.
func (d *DB) UpsertFileBatch(ctx context.Context, files []models.FileMetadata) error {
	if len(files) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin batch transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
	INSERT INTO files (
		path, size, mtime, atime, inode, extension, content_hash, staleness_score, last_scanned_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		size = excluded.size,
		mtime = excluded.mtime,
		atime = excluded.atime,
		inode = excluded.inode,
		extension = excluded.extension,
		last_scanned_at = excluded.last_scanned_at;
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to prepare upsert statement: %w", err)
	}
	defer stmt.Close()

	for _, f := range files {
		var contentHash sql.NullString
		if f.ContentHash != "" {
			contentHash.String = f.ContentHash
			contentHash.Valid = true
		}

		_, err := stmt.ExecContext(ctx,
			f.Path,
			f.Size,
			f.Mtime.Unix(),
			f.Atime.Unix(),
			f.Inode,
			f.Extension,
			contentHash,
			f.StalenessScore,
			f.LastScannedAt.Unix(),
		)
		if err != nil {
			return fmt.Errorf("failed to execute upsert for %q: %w", f.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit batch transaction: %w", err)
	}

	return nil
}

// GetCandidateSizeFiles retrieves all files whose size matches at least one other file (Pass 1 of dedup).
// Excludes 0-byte empty files and unique-sized files to avoid useless I/O hashing.
func (d *DB) GetCandidateSizeFiles(ctx context.Context) ([]models.FileMetadata, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), staleness_score, last_scanned_at
	FROM files
	WHERE size > 0 AND size IN (
		SELECT size FROM files WHERE size > 0 GROUP BY size HAVING COUNT(*) > 1
	)
	ORDER BY size DESC, id ASC;
	`

	rows, err := d.Conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query size bucket candidates: %w", err)
	}
	defer rows.Close()

	var results []models.FileMetadata
	for rows.Next() {
		var f models.FileMetadata
		var mtimeSec, atimeSec, scannedSec int64
		var contentHash string

		err := rows.Scan(
			&f.ID,
			&f.Path,
			&f.Size,
			&mtimeSec,
			&atimeSec,
			&f.Inode,
			&f.Extension,
			&contentHash,
			&f.StalenessScore,
			&scannedSec,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file metadata row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = contentHash

		results = append(results, f)
	}

	return results, rows.Err()
}

// BatchUpdateContentHashes updates content_hash for multiple files inside a single atomic transaction.
func (d *DB) BatchUpdateContentHashes(ctx context.Context, updates []models.FileHashUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin hash update transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, "UPDATE files SET content_hash = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("failed to prepare hash update statement: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.ContentHash, u.ID); err != nil {
			return fmt.Errorf("failed to update hash for file ID %d: %w", u.ID, err)
		}
	}

	return tx.Commit()
}

// GetDuplicateGroups queries SQLite for all duplicate clusters sharing the same content_hash and size.
// Returns groups sorted by reclaimable wasted space (descending).
func (d *DB) GetDuplicateGroups(ctx context.Context) ([]models.DuplicateGroup, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Query grouped duplicate hashes
	groupQuery := `
	SELECT content_hash, size, COUNT(*) as count, (COUNT(*) - 1) * size as wasted
	FROM files
	WHERE content_hash IS NOT NULL AND content_hash != ''
	GROUP BY content_hash, size
	HAVING COUNT(*) > 1
	ORDER BY wasted DESC, size DESC;
	`

	rows, err := d.Conn.QueryContext(ctx, groupQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query duplicate groups: %w", err)
	}
	defer rows.Close()

	var groups []models.DuplicateGroup
	for rows.Next() {
		var g models.DuplicateGroup
		if err := rows.Scan(&g.ContentHash, &g.FileSize, &g.DuplicateCount, &g.WastedBytes); err != nil {
			return nil, fmt.Errorf("failed to scan duplicate group: %w", err)
		}
		groups = append(groups, g)
	}

	// For each group, load the actual file records
	fileStmt, err := d.Conn.PrepareContext(ctx, `
		SELECT id, path, size, mtime, atime, inode, extension, content_hash, staleness_score, last_scanned_at
		FROM files
		WHERE content_hash = ?
		ORDER BY id ASC;
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare files-by-hash query: %w", err)
	}
	defer fileStmt.Close()

	for i := range groups {
		fRows, err := fileStmt.QueryContext(ctx, groups[i].ContentHash)
		if err != nil {
			return nil, fmt.Errorf("failed to query files for hash %q: %w", groups[i].ContentHash, err)
		}

		var fileList []models.FileMetadata
		for fRows.Next() {
			var f models.FileMetadata
			var mtimeSec, atimeSec, scannedSec int64
			var hash string

			if err := fRows.Scan(
				&f.ID,
				&f.Path,
				&f.Size,
				&mtimeSec,
				&atimeSec,
				&f.Inode,
				&f.Extension,
				&hash,
				&f.StalenessScore,
				&scannedSec,
			); err != nil {
				fRows.Close()
				return nil, fmt.Errorf("failed to scan file row: %w", err)
			}

			f.Mtime = time.Unix(mtimeSec, 0)
			f.Atime = time.Unix(atimeSec, 0)
			f.LastScannedAt = time.Unix(scannedSec, 0)
			f.ContentHash = hash

			fileList = append(fileList, f)
		}
		fRows.Close()
		groups[i].Files = fileList
	}

	return groups, nil
}

// RecordSnapshot writes a point-in-time summary to scan_snapshots.
func (d *DB) RecordSnapshot(ctx context.Context, rootPath string, totalFiles int64, totalBytes int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	query := `
	INSERT INTO scan_snapshots (scanned_at, root_path, total_files, total_bytes)
	VALUES (?, ?, ?, ?);
	`
	now := time.Now().Unix()
	res, err := d.Conn.ExecContext(ctx, query, now, rootPath, totalFiles, totalBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to record scan snapshot: %w", err)
	}
	return res.LastInsertId()
}

// GetTotalStats returns aggregated metrics from the files table.
func (d *DB) GetTotalStats(ctx context.Context) (totalFiles int64, totalBytes int64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	row := d.Conn.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files")
	err = row.Scan(&totalFiles, &totalBytes)
	return
}
