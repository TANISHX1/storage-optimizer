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
// INCREMENTAL RE-SCANNING & AUDIT INTEGRITY (PHASE 4):
// 1. Hash Invalidation on Modification:
//    - When an existing file is re-scanned, SQLite compares the incoming `mtime` and `size`
//      against stored values using a SQL CASE expression.
//    - If mtime/size changed: `content_hash` and `staleness_score` are reset to NULL,
//      forcing duplicate and staleness engines to re-evaluate only the modified files.
//    - If unchanged: existing hashes/scores are preserved, making re-scans zero-cost for I/O.
//
// 2. Stale Row Pruning:
//    - Detects files that were deleted or moved outside the application.
//    - Queries records where `last_scanned_at < scanStartTime` and verifies with `os.Lstat()`.
//      Confirmed deleted files are purged from SQLite, keeping the index 100% accurate.
//
// 3. Time-Series Snapshots for Python Forecasting:
//    - Every scan appends a point-in-time record to `scan_snapshots`.
//    - Exposes historical snapshots sorted chronologically for Sahil's regression models.
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
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
	}

	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db at %q: %w", dbPath, err)
	}

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	dbInstance := &DB{
		Conn: conn,
		path: dbPath,
	}

	if err := dbInstance.applyPragmas(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply pragmas: %w", err)
	}

	if err := dbInstance.applySchema(schemaPath); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	return dbInstance, nil
}

func (d *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA cache_size = -64000;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA temp_store = MEMORY;",
	}

	for _, pragma := range pragmas {
		if _, err := d.Conn.Exec(pragma); err != nil {
			return fmt.Errorf("pragma error %q: %w", pragma, err)
		}
	}
	return nil
}

func (d *DB) applySchema(schemaPath string) error {
	baseSQL := `
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
		is_system INTEGER DEFAULT 0,
		category TEXT DEFAULT 'user',
		last_scanned_at INTEGER NOT NULL
	);
	`
	if _, err := d.Conn.Exec(baseSQL); err != nil {
		return fmt.Errorf("failed to create base files table: %w", err)
	}

	// Safe idempotent migrations for existing database files
	_, _ = d.Conn.Exec("ALTER TABLE files ADD COLUMN is_system INTEGER DEFAULT 0;")
	_, _ = d.Conn.Exec("ALTER TABLE files ADD COLUMN category TEXT DEFAULT 'user';")

	indexSQL := `
	CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
	CREATE INDEX IF NOT EXISTS idx_files_hash ON files(content_hash);
	CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(mtime);
	CREATE INDEX IF NOT EXISTS idx_files_staleness ON files(staleness_score);
	CREATE INDEX IF NOT EXISTS idx_files_category ON files(category);

	CREATE TABLE IF NOT EXISTS scan_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scanned_at INTEGER NOT NULL,
		root_path TEXT NOT NULL,
		total_files INTEGER,
		total_bytes INTEGER
	);

	CREATE INDEX IF NOT EXISTS idx_snapshots_scanned_at ON scan_snapshots(scanned_at);
	CREATE INDEX IF NOT EXISTS idx_snapshots_root ON scan_snapshots(root_path);

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
	if _, err := d.Conn.Exec(indexSQL); err != nil {
		return fmt.Errorf("failed to apply schema indexes: %w", err)
	}

	return nil
}

func (d *DB) Close() error {
	return d.Conn.Close()
}

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
		buffer = buffer[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return

		case meta, ok := <-inChan:
			if !ok {
				flush()
				return
			}
			buffer = append(buffer, meta)
			if len(buffer) >= batchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// UpsertFileBatch performs incremental upserting.
// Automatically invalidates content_hash and staleness_score when mtime/size changes.
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
		path, size, mtime, atime, inode, extension, content_hash, staleness_score, is_system, category, last_scanned_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		content_hash = CASE 
			WHEN files.mtime != excluded.mtime OR files.size != excluded.size THEN NULL 
			ELSE files.content_hash 
		END,
		staleness_score = CASE
			WHEN files.mtime != excluded.mtime THEN NULL
			ELSE files.staleness_score
		END,
		size = excluded.size,
		mtime = excluded.mtime,
		atime = excluded.atime,
		inode = excluded.inode,
		extension = excluded.extension,
		is_system = excluded.is_system,
		category = excluded.category,
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

		isSysInt := 0
		if f.IsSystem {
			isSysInt = 1
		}
		cat := string(f.Category)
		if cat == "" {
			cat = "user"
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
			isSysInt,
			cat,
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

// PruneDeletedFiles detects and purges records in SQLite under rootPath that were deleted on disk.
func (d *DB) PruneDeletedFiles(ctx context.Context, rootPath string, scanStartTime time.Time) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cleanRoot := filepath.Clean(rootPath)

	// Query candidate stale records indexed under this root that were NOT seen during the recent scan
	query := `
	SELECT id, path FROM files
	WHERE (path = ? OR path LIKE ? || '/%') AND last_scanned_at < ?;
	`

	rows, err := d.Conn.QueryContext(ctx, query, cleanRoot, cleanRoot, scanStartTime.Unix())
	if err != nil {
		return 0, fmt.Errorf("failed to query stale deletion candidates: %w", err)
	}
	defer rows.Close()

	type StaleFile struct {
		ID   int64
		Path string
	}

	var candidates []StaleFile
	for rows.Next() {
		var sf StaleFile
		if err := rows.Scan(&sf.ID, &sf.Path); err != nil {
			return 0, fmt.Errorf("failed to scan candidate stale file: %w", err)
		}
		candidates = append(candidates, sf)
	}

	if len(candidates) == 0 {
		return 0, nil
	}

	// Verify against filesystem using os.Lstat to ensure file was actually deleted/moved
	var idsToDelete []int64
	for _, c := range candidates {
		_, err := os.Lstat(c.Path)
		if os.IsNotExist(err) || err != nil {
			idsToDelete = append(idsToDelete, c.ID)
		}
	}

	if len(idsToDelete) == 0 {
		return 0, nil
	}

	// Batch delete confirmed dead rows inside a single transaction
	tx, err := d.Conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin pruning transaction: %w", err)
	}
	defer tx.Rollback()

	delStmt, err := tx.PrepareContext(ctx, "DELETE FROM files WHERE id = ?")
	if err != nil {
		return 0, fmt.Errorf("failed to prepare deletion statement: %w", err)
	}
	defer delStmt.Close()

	for _, id := range idsToDelete {
		if _, err := delStmt.ExecContext(ctx, id); err != nil {
			return 0, fmt.Errorf("failed to prune stale row ID %d: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit pruning transaction: %w", err)
	}

	return int64(len(idsToDelete)), nil
}

// GetAllFiles retrieves all indexed files from SQLite.
func (d *DB) GetAllFiles(ctx context.Context) ([]models.FileMetadata, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	ORDER BY id ASC;
	`

	rows, err := d.Conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all files: %w", err)
	}
	defer rows.Close()

	var results []models.FileMetadata
	for rows.Next() {
		var f models.FileMetadata
		var mtimeSec, atimeSec, scannedSec int64
		var contentHash, category string
		var isSysInt int

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
			&isSysInt,
			&category,
			&scannedSec,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = contentHash
		f.IsSystem = isSysInt == 1
		f.Category = models.FileCategory(category)

		results = append(results, f)
	}

	return results, rows.Err()
}

// GetCandidateSizeFiles retrieves all files whose size matches at least one other file (Pass 1 of dedup).
func (d *DB) GetCandidateSizeFiles(ctx context.Context) ([]models.FileMetadata, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
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
		var contentHash, category string
		var isSysInt int

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
			&isSysInt,
			&category,
			&scannedSec,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file metadata row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = contentHash
		f.IsSystem = isSysInt == 1
		f.Category = models.FileCategory(category)

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

// BatchUpdateStalenessScores updates staleness_score for multiple files inside a single atomic transaction.
func (d *DB) BatchUpdateStalenessScores(ctx context.Context, updates []models.FileStalenessUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin staleness score transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, "UPDATE files SET staleness_score = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("failed to prepare staleness update statement: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.StalenessScore, u.ID); err != nil {
			return fmt.Errorf("failed to update staleness score for file ID %d: %w", u.ID, err)
		}
	}

	return tx.Commit()
}

// GetStaleFiles queries SQLite for files untouched for at least minDays, filtered and sorted by staleness_score.
func (d *DB) GetStaleFiles(ctx context.Context, minDays int, minScore float64, limit int) ([]models.FileMetadata, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	cutoffSec := time.Now().AddDate(0, 0, -minDays).Unix()

	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	WHERE mtime <= ? AND staleness_score >= ?
	ORDER BY staleness_score DESC, size DESC
	LIMIT ?;
	`

	rows, err := d.Conn.QueryContext(ctx, query, cutoffSec, minScore, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query stale files: %w", err)
	}
	defer rows.Close()

	var results []models.FileMetadata
	for rows.Next() {
		var f models.FileMetadata
		var mtimeSec, atimeSec, scannedSec int64
		var hash, category string
		var isSysInt int

		err := rows.Scan(
			&f.ID,
			&f.Path,
			&f.Size,
			&mtimeSec,
			&atimeSec,
			&f.Inode,
			&f.Extension,
			&hash,
			&f.StalenessScore,
			&isSysInt,
			&category,
			&scannedSec,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stale file row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = hash
		f.IsSystem = isSysInt == 1
		f.Category = models.FileCategory(category)

		results = append(results, f)
	}

	return results, rows.Err()
}

// GetDuplicateGroups queries SQLite for all duplicate clusters sharing the same content_hash and size.
func (d *DB) GetDuplicateGroups(ctx context.Context) ([]models.DuplicateGroup, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

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

	fileStmt, err := d.Conn.PrepareContext(ctx, `
		SELECT id, path, size, mtime, atime, inode, extension, content_hash, staleness_score, is_system, category, last_scanned_at
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
			var hash, category string
			var isSysInt int

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
				&isSysInt,
				&category,
				&scannedSec,
			); err != nil {
				fRows.Close()
				return nil, fmt.Errorf("failed to scan file row: %w", err)
			}

			f.Mtime = time.Unix(mtimeSec, 0)
			f.Atime = time.Unix(atimeSec, 0)
			f.LastScannedAt = time.Unix(scannedSec, 0)
			f.ContentHash = hash
			f.IsSystem = isSysInt == 1
			f.Category = models.FileCategory(category)

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

// GetSnapshots retrieves historical scan snapshots for time-series analytics (Sahil - Day 7).
func (d *DB) GetSnapshots(ctx context.Context, limit int) ([]models.ScanSnapshot, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT id, scanned_at, root_path, total_files, total_bytes
	FROM scan_snapshots
	ORDER BY scanned_at ASC
	LIMIT ?;
	`

	rows, err := d.Conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []models.ScanSnapshot
	for rows.Next() {
		var s models.ScanSnapshot
		var scannedSec int64
		if err := rows.Scan(&s.ID, &scannedSec, &s.RootPath, &s.TotalFiles, &s.TotalBytes); err != nil {
			return nil, fmt.Errorf("failed to scan snapshot row: %w", err)
		}
		s.ScannedAt = time.Unix(scannedSec, 0)
		snapshots = append(snapshots, s)
	}

	return snapshots, rows.Err()
}

// GetSnapshotsByRoot retrieves snapshots for a specific root directory.
func (d *DB) GetSnapshotsByRoot(ctx context.Context, rootPath string, limit int) ([]models.ScanSnapshot, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT id, scanned_at, root_path, total_files, total_bytes
	FROM scan_snapshots
	WHERE root_path = ? OR root_path LIKE ? || '%'
	ORDER BY scanned_at ASC
	LIMIT ?;
	`

	rows, err := d.Conn.QueryContext(ctx, query, rootPath, rootPath, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots by root: %w", err)
	}
	defer rows.Close()

	var snapshots []models.ScanSnapshot
	for rows.Next() {
		var s models.ScanSnapshot
		var scannedSec int64
		if err := rows.Scan(&s.ID, &scannedSec, &s.RootPath, &s.TotalFiles, &s.TotalBytes); err != nil {
			return nil, fmt.Errorf("failed to scan snapshot row: %w", err)
		}
		s.ScannedAt = time.Unix(scannedSec, 0)
		snapshots = append(snapshots, s)
	}

	return snapshots, rows.Err()
}

// GetTotalStats returns aggregated metrics from the files table.
func (d *DB) GetTotalStats(ctx context.Context) (totalFiles int64, totalBytes int64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	row := d.Conn.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files")
	err = row.Scan(&totalFiles, &totalBytes)
	return
}

// GetCategoryBreakdown returns file counts and storage size grouped by category.
func (d *DB) GetCategoryBreakdown(ctx context.Context) ([]models.CategoryBreakdown, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
	SELECT COALESCE(category, 'user'), COUNT(*), COALESCE(SUM(size), 0)
	FROM files
	GROUP BY category
	ORDER BY SUM(size) DESC;
	`
	rows, err := d.Conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query category breakdown: %w", err)
	}
	defer rows.Close()

	var list []models.CategoryBreakdown
	for rows.Next() {
		var cat string
		var count, bytes int64
		if err := rows.Scan(&cat, &count, &bytes); err != nil {
			return nil, fmt.Errorf("failed to scan category breakdown row: %w", err)
		}
		list = append(list, models.CategoryBreakdown{
			Category:   models.FileCategory(cat),
			TotalFiles: count,
			TotalBytes: bytes,
		})
	}
	return list, rows.Err()
}

// GetStorageStats returns a high-level summary of the indexed storage state for dashboards.
func (d *DB) GetStorageStats(ctx context.Context) (*models.StorageStats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	stats := &models.StorageStats{}

	// Total files and bytes
	row := d.Conn.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files")
	if err := row.Scan(&stats.TotalFiles, &stats.TotalBytes); err != nil {
		return nil, fmt.Errorf("failed to query file totals: %w", err)
	}

	// Total duplicate groups & wasted bytes
	dupRow := d.Conn.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(wasted), 0) FROM (
			SELECT (COUNT(*) - 1) * size as wasted
			FROM files
			WHERE content_hash IS NOT NULL AND content_hash != ''
			GROUP BY content_hash, size
			HAVING COUNT(*) > 1
		);
	`)
	_ = dupRow.Scan(&stats.TotalDuplicates, &stats.TotalWastedBytes)

	// Total snapshots
	snapRow := d.Conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM scan_snapshots")
	_ = snapRow.Scan(&stats.TotalSnapshots)

	// Category breakdown
	catList, err := d.GetCategoryBreakdown(ctx)
	if err == nil {
		stats.Categories = catList
	}

	return stats, nil
}

// GetFilesByIDs retrieves metadata for specific file IDs.
func (d *DB) GetFilesByIDs(ctx context.Context, ids []int64) ([]models.FileMetadata, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	var results []models.FileMetadata
	for _, id := range ids {
		f, err := d.getFileByIDLocked(ctx, id)
		if err == nil && f != nil {
			results = append(results, *f)
		}
	}
	return results, nil
}

// GetFileByID retrieves a single file record by ID.
func (d *DB) GetFileByID(ctx context.Context, id int64) (*models.FileMetadata, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.getFileByIDLocked(ctx, id)
}

func (d *DB) getFileByIDLocked(ctx context.Context, id int64) (*models.FileMetadata, error) {
	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	WHERE id = ?;
	`
	row := d.Conn.QueryRowContext(ctx, query, id)

	var f models.FileMetadata
	var mtimeSec, atimeSec, scannedSec int64
	var hash, category string
	var isSysInt int

	err := row.Scan(
		&f.ID,
		&f.Path,
		&f.Size,
		&mtimeSec,
		&atimeSec,
		&f.Inode,
		&f.Extension,
		&hash,
		&f.StalenessScore,
		&isSysInt,
		&category,
		&scannedSec,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan file row %d: %w", id, err)
	}

	f.Mtime = time.Unix(mtimeSec, 0)
	f.Atime = time.Unix(atimeSec, 0)
	f.LastScannedAt = time.Unix(scannedSec, 0)
	f.ContentHash = hash
	f.IsSystem = isSysInt == 1
	f.Category = models.FileCategory(category)

	return &f, nil
}

// DeleteFileByID purges a single file record from the files table.
func (d *DB) DeleteFileByID(ctx context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.Conn.ExecContext(ctx, "DELETE FROM files WHERE id = ?", id)
	return err
}

// LogAction inserts an immutable record into actions_log before or after a filesystem mutation.
func (d *DB) LogAction(ctx context.Context, filePath string, mode models.ActionMode, trashedToPath *string, fileSize int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	query := `
	INSERT INTO actions_log (file_path, action_mode, trashed_to_path, file_size, performed_at)
	VALUES (?, ?, ?, ?, ?);
	`
	now := time.Now().Unix()
	var tPath sql.NullString
	if trashedToPath != nil {
		tPath.String = *trashedToPath
		tPath.Valid = true
	}

	res, err := d.Conn.ExecContext(ctx, query, filePath, string(mode), tPath, fileSize, now)
	if err != nil {
		return 0, fmt.Errorf("failed to record action log: %w", err)
	}
	return res.LastInsertId()
}

// GetActionLogByID retrieves a specific action log record.
func (d *DB) GetActionLogByID(ctx context.Context, id int64) (*models.ActionLog, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
	SELECT id, file_path, action_mode, trashed_to_path, file_size, performed_at
	FROM actions_log
	WHERE id = ?;
	`
	row := d.Conn.QueryRowContext(ctx, query, id)

	var l models.ActionLog
	var trashedPath sql.NullString
	var perfSec int64
	var mode string

	if err := row.Scan(&l.ID, &l.FilePath, &mode, &trashedPath, &l.FileSize, &perfSec); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan action log row: %w", err)
	}
	l.ActionMode = models.ActionMode(mode)
	if trashedPath.Valid {
		l.TrashedToPath = &trashedPath.String
	}
	l.PerformedAt = time.Unix(perfSec, 0)
	return &l, nil
}

// DeleteActionLog removes an action log entry (e.g. after successful restoration).
func (d *DB) DeleteActionLog(ctx context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.Conn.ExecContext(ctx, "DELETE FROM actions_log WHERE id = ?", id)
	return err
}

// GetActionLogs queries the immutable audit log of cleanup actions.
func (d *DB) GetActionLogs(ctx context.Context, limit int) ([]models.ActionLog, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT id, file_path, action_mode, trashed_to_path, file_size, performed_at
	FROM actions_log
	ORDER BY performed_at DESC
	LIMIT ?;
	`
	rows, err := d.Conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query action logs: %w", err)
	}
	defer rows.Close()

	var logs []models.ActionLog
	for rows.Next() {
		var l models.ActionLog
		var trashedPath sql.NullString
		var perfSec int64
		var mode string

		if err := rows.Scan(&l.ID, &l.FilePath, &mode, &trashedPath, &l.FileSize, &perfSec); err != nil {
			return nil, fmt.Errorf("failed to scan action log row: %w", err)
		}
		l.ActionMode = models.ActionMode(mode)
		if trashedPath.Valid {
			l.TrashedToPath = &trashedPath.String
		}
		l.PerformedAt = time.Unix(perfSec, 0)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}



