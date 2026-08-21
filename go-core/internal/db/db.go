package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"storage-optimizer/go-core/internal/models"
)

// ============================================================================
// SQLITE SINGLE-WRITER STORAGE ENGINE (OPTIMIZED WAL MODE & INDEXED QUERIES)
//
// SYSTEMS & CONCURRENCY OPTIMIZATIONS:
// 1. WAL Mode & Zero Reader Blocking:
//    - SQLite WAL (Write-Ahead Logging) enables concurrent readers without waiting for writers.
//    - Busy timeout is set to 5000ms to eliminate locking contention.
//
// 2. Hash & Duplicate Cluster Precomputation:
//    - Dedup groups are indexed via `duplicate_group_id` column.
//    - Duplicate queries are single-query indexed lookups, completely eliminating N+1 overhead.
//
// 3. Fast Stale and Directory Browse Indexing:
//    - Staleness queries use LIMIT/OFFSET server-side pagination with composite index filtering.
//    - `parent_path` allows instantaneous lazy loading of directory hierarchies.
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

	db := &DB{
		Conn: conn,
		path: dbPath,
	}

	if err := db.applyPragmas(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply database pragmas: %w", err)
	}

	if err := db.applySchema(schemaPath); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to apply database schema: %w", err)
	}

	db.backfillMissingColumns(context.Background())

	return db, nil
}

func (d *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA cache_size = -64000;", // 64 MB page cache in memory
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
		duplicate_group_id TEXT,
		parent_path TEXT,
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
	_, _ = d.Conn.Exec("ALTER TABLE files ADD COLUMN duplicate_group_id TEXT;")
	_, _ = d.Conn.Exec("ALTER TABLE files ADD COLUMN parent_path TEXT;")

	indexSQL := `
	CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
	CREATE INDEX IF NOT EXISTS idx_files_hash ON files(content_hash);
	CREATE INDEX IF NOT EXISTS idx_files_dup_group ON files(duplicate_group_id);
	CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent_path);
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
// Automatically invalidates content_hash, duplicate_group_id, and staleness_score when mtime/size changes.
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
		path, size, mtime, atime, inode, extension, content_hash, duplicate_group_id, parent_path, staleness_score, is_system, category, last_scanned_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		content_hash = CASE 
			WHEN files.mtime != excluded.mtime OR files.size != excluded.size THEN NULL 
			ELSE files.content_hash 
		END,
		duplicate_group_id = CASE
			WHEN files.mtime != excluded.mtime OR files.size != excluded.size THEN NULL
			ELSE files.duplicate_group_id
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
		parent_path = excluded.parent_path,
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
		var contentHash, dupGroupID sql.NullString
		if f.ContentHash != "" {
			contentHash.String = f.ContentHash
			contentHash.Valid = true
		}
		if f.DuplicateGroupID != "" {
			dupGroupID.String = f.DuplicateGroupID
			dupGroupID.Valid = true
		}

		parentDir := f.ParentPath
		if parentDir == "" {
			parentDir = filepath.Dir(filepath.Clean(f.Path))
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
			dupGroupID,
			parentDir,
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
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(parent_path, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
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
		var contentHash, dupGroupID, parentPath, category string
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
			&dupGroupID,
			&parentPath,
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
		f.DuplicateGroupID = dupGroupID
		f.ParentPath = parentPath
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
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(parent_path, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
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
		var contentHash, dupGroupID, parentPath, category string
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
			&dupGroupID,
			&parentPath,
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
		f.DuplicateGroupID = dupGroupID
		f.ParentPath = parentPath
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

// AssignDuplicateGroups updates duplicate_group_id for all duplicate clusters in SQLite using fast temporary table indexing.
func (d *DB) AssignDuplicateGroups(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	fastAssignSQL := `
	CREATE TEMP TABLE IF NOT EXISTS temp_active_dup_hashes AS
	SELECT content_hash 
	FROM files 
	WHERE content_hash IS NOT NULL AND content_hash != '' 
	GROUP BY content_hash, size 
	HAVING COUNT(*) > 1;

	CREATE INDEX IF NOT EXISTS temp_active_dup_idx ON temp_active_dup_hashes(content_hash);

	UPDATE files SET duplicate_group_id = NULL
	WHERE duplicate_group_id IS NOT NULL 
	  AND duplicate_group_id NOT IN (SELECT content_hash FROM temp_active_dup_hashes);

	UPDATE files SET duplicate_group_id = content_hash
	WHERE content_hash IS NOT NULL AND content_hash != ''
	  AND content_hash IN (SELECT content_hash FROM temp_active_dup_hashes);

	DROP TABLE IF EXISTS temp_active_dup_hashes;
	`
	if _, err := d.Conn.ExecContext(ctx, fastAssignSQL); err != nil {
		return fmt.Errorf("failed to assign duplicate group IDs: %w", err)
	}

	return nil
}

func (d *DB) backfillMissingColumns(ctx context.Context) {
	var unassignedDups int
	_ = d.Conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM files WHERE duplicate_group_id IS NULL AND content_hash IS NOT NULL AND content_hash != ''").Scan(&unassignedDups)
	if unassignedDups > 0 {
		_ = d.AssignDuplicateGroups(ctx)
	}

	var missingParents int
	_ = d.Conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM files WHERE parent_path IS NULL OR parent_path = ''").Scan(&missingParents)
	if missingParents > 0 {
		_ = d.syncBackfillParentPaths(ctx)
	}
}

func (d *DB) syncBackfillParentPaths(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.Conn.QueryContext(ctx, "SELECT id, path FROM files WHERE parent_path IS NULL OR parent_path = ''")
	if err != nil {
		return err
	}
	defer rows.Close()

	type Item struct {
		id   int64
		path string
	}
	var items []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.id, &it.path); err == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	if len(items) == 0 {
		return nil
	}

	chunkSize := 2000
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]

		tx, err := d.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		stmt, err := tx.PrepareContext(ctx, "UPDATE files SET parent_path = ? WHERE id = ?")
		if err != nil {
			tx.Rollback()
			return err
		}

		for _, it := range chunk {
			parent := filepath.Dir(it.path)
			_, _ = stmt.ExecContext(ctx, parent, it.id)
		}
		stmt.Close()
		_ = tx.Commit()
	}

	return nil
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

// GetStaleFilesPaginated queries SQLite for stale files with SQL LIMIT and OFFSET (Fix 5).
func (d *DB) GetStaleFilesPaginated(ctx context.Context, minDays int, minScore float64, page int, limit int) (files []models.FileMetadata, totalFiles int, totalBytes int64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	offset := (page - 1) * limit

	cutoffSec := time.Now().AddDate(0, 0, -minDays).Unix()

	// Query aggregate totals for stale files
	countQuery := `
	SELECT COUNT(*), COALESCE(SUM(size), 0)
	FROM files
	WHERE mtime <= ? AND staleness_score >= ?;
	`
	row := d.Conn.QueryRowContext(ctx, countQuery, cutoffSec, minScore)
	if err := row.Scan(&totalFiles, &totalBytes); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to count stale files: %w", err)
	}

	if totalFiles == 0 {
		return []models.FileMetadata{}, 0, 0, nil
	}

	query := `
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(parent_path, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	WHERE mtime <= ? AND staleness_score >= ?
	ORDER BY staleness_score DESC, size DESC
	LIMIT ? OFFSET ?;
	`

	rows, err := d.Conn.QueryContext(ctx, query, cutoffSec, minScore, limit, offset)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to query stale files page: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var f models.FileMetadata
		var mtimeSec, atimeSec, scannedSec int64
		var hash, dupGroup, parentPath, category string
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
			&dupGroup,
			&parentPath,
			&f.StalenessScore,
			&isSysInt,
			&category,
			&scannedSec,
		)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("failed to scan stale file row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = hash
		f.DuplicateGroupID = dupGroup
		f.ParentPath = parentPath
		f.IsSystem = isSysInt == 1
		f.Category = models.FileCategory(category)

		files = append(files, f)
	}

	return files, totalFiles, totalBytes, rows.Err()
}

// GetStaleFiles queries SQLite for files untouched for at least minDays, filtered and sorted by staleness_score.
func (d *DB) GetStaleFiles(ctx context.Context, minDays int, minScore float64, limit int) ([]models.FileMetadata, error) {
	files, _, _, err := d.GetStaleFilesPaginated(ctx, minDays, minScore, 1, limit)
	return files, err
}

// GetDuplicateGroupsPaginated queries SQLite for duplicate clusters with pagination, avoiding N+1 queries (Fix 3 & Fix 5).
func (d *DB) GetDuplicateGroupsPaginated(ctx context.Context, page int, limit int) (groups []models.DuplicateGroup, totalGroups int, totalDupFiles int, totalWastedBytes int64, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	offset := (page - 1) * limit

	// Step 1: Query aggregate stats for all duplicate clusters
	statsQuery := `
	SELECT COUNT(*), COALESCE(SUM(wasted), 0), COALESCE(SUM(copies), 0) FROM (
		SELECT (COUNT(*) - 1) * size as wasted, COUNT(*) as copies
		FROM files
		WHERE duplicate_group_id IS NOT NULL AND duplicate_group_id != ''
		GROUP BY duplicate_group_id, size
	);
	`
	row := d.Conn.QueryRowContext(ctx, statsQuery)
	if err := row.Scan(&totalGroups, &totalWastedBytes, &totalDupFiles); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("failed to count duplicate groups: %w", err)
	}

	if totalGroups == 0 {
		return []models.DuplicateGroup{}, 0, 0, 0, nil
	}

	// Step 2: Fetch the page of duplicate group identifiers (limit & offset)
	pageQuery := `
	SELECT duplicate_group_id, size, COUNT(*) as count, (COUNT(*) - 1) * size as wasted
	FROM files
	WHERE duplicate_group_id IS NOT NULL AND duplicate_group_id != ''
	GROUP BY duplicate_group_id, size
	ORDER BY wasted DESC, size DESC
	LIMIT ? OFFSET ?;
	`
	rows, err := d.Conn.QueryContext(ctx, pageQuery, limit, offset)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("failed to query page of duplicate groups: %w", err)
	}
	defer rows.Close()

	var groupIDs []string
	groupMap := make(map[string]*models.DuplicateGroup)

	for rows.Next() {
		var g models.DuplicateGroup
		if err := rows.Scan(&g.ContentHash, &g.FileSize, &g.DuplicateCount, &g.WastedBytes); err != nil {
			return nil, 0, 0, 0, fmt.Errorf("failed to scan duplicate group row: %w", err)
		}
		g.Files = make([]models.FileMetadata, 0, g.DuplicateCount)
		groupIDs = append(groupIDs, g.ContentHash)
		groupMap[g.ContentHash] = &g
	}
	rows.Close()

	if len(groupIDs) == 0 {
		return []models.DuplicateGroup{}, totalGroups, totalDupFiles, totalWastedBytes, nil
	}

	// Step 3: Fetch all files belonging to these group IDs in a SINGLE indexed query (NO N+1)
	placeholders := make([]string, len(groupIDs))
	args := make([]interface{}, len(groupIDs))
	for i, id := range groupIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	filesQuery := fmt.Sprintf(`
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(parent_path, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	WHERE duplicate_group_id IN (%s)
	ORDER BY size DESC, duplicate_group_id, id ASC;
	`, strings.Join(placeholders, ","))

	fRows, err := d.Conn.QueryContext(ctx, filesQuery, args...)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("failed to query duplicate files batch: %w", err)
	}
	defer fRows.Close()

	for fRows.Next() {
		var f models.FileMetadata
		var mtimeSec, atimeSec, scannedSec int64
		var hash, dupGroup, parentPath, category string
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
			&dupGroup,
			&parentPath,
			&f.StalenessScore,
			&isSysInt,
			&category,
			&scannedSec,
		); err != nil {
			return nil, 0, 0, 0, fmt.Errorf("failed to scan duplicate file row: %w", err)
		}

		f.Mtime = time.Unix(mtimeSec, 0)
		f.Atime = time.Unix(atimeSec, 0)
		f.LastScannedAt = time.Unix(scannedSec, 0)
		f.ContentHash = hash
		f.DuplicateGroupID = dupGroup
		f.ParentPath = parentPath
		f.IsSystem = isSysInt == 1
		f.Category = models.FileCategory(category)

		if g, exists := groupMap[dupGroup]; exists {
			g.Files = append(g.Files, f)
		}
	}

	// Reconstruct ordered slice matching groupIDs order
	groups = make([]models.DuplicateGroup, 0, len(groupIDs))
	for _, id := range groupIDs {
		if g, ok := groupMap[id]; ok {
			groups = append(groups, *g)
		}
	}

	return groups, totalGroups, totalDupFiles, totalWastedBytes, nil
}

// GetDuplicateGroups queries SQLite for all duplicate clusters without N+1 query loop.
func (d *DB) GetDuplicateGroups(ctx context.Context) ([]models.DuplicateGroup, error) {
	groups, _, _, _, err := d.GetDuplicateGroupsPaginated(ctx, 1, 100000)
	return groups, err
}

// BrowseDirectory lists direct child files and aggregated subdirectories of a directory (Fix 6).
func (d *DB) BrowseDirectory(ctx context.Context, dirPath string) (*models.DirectoryBrowseResponse, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	cleanDir := filepath.Clean(dirPath)
	parentDir := filepath.Dir(cleanDir)
	if cleanDir == parentDir {
		parentDir = ""
	}

	resp := &models.DirectoryBrowseResponse{
		CurrentPath: cleanDir,
		ParentPath:  parentDir,
		Items:       []models.DirectoryBrowseItem{},
	}

	// 1. Query direct child files
	filesQuery := `
	SELECT id, path, size, mtime, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(staleness_score, 0.0), is_system, category
	FROM files
	WHERE parent_path = ?
	ORDER BY size DESC;
	`
	fRows, err := d.Conn.QueryContext(ctx, filesQuery, cleanDir)
	if err != nil {
		return nil, fmt.Errorf("failed to browse files in %q: %w", cleanDir, err)
	}
	defer fRows.Close()

	for fRows.Next() {
		var id int64
		var path, hash, dupGroup, category string
		var size, mtimeSec int64
		var score float64
		var isSysInt int

		if err := fRows.Scan(&id, &path, &size, &mtimeSec, &hash, &dupGroup, &score, &isSysInt, &category); err != nil {
			return nil, fmt.Errorf("failed to scan browse file: %w", err)
		}

		resp.Items = append(resp.Items, models.DirectoryBrowseItem{
			Path:           path,
			Name:           filepath.Base(path),
			IsDir:          false,
			IsScanned:      true,
			Size:           size,
			Mtime:          time.Unix(mtimeSec, 0),
			Category:       models.FileCategory(category),
			IsSystem:       isSysInt == 1,
			StalenessScore: score,
			IsDuplicate:    dupGroup != "",
		})
		resp.TotalBytes += size
	}
	fRows.Close()

	// 2. Query direct subdirectories under cleanDir from database
	prefix := cleanDir
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	dirsQuery := `
	SELECT parent_path, COUNT(*), COALESCE(SUM(size), 0), MAX(mtime)
	FROM files
	WHERE parent_path LIKE ? || '%'
	GROUP BY parent_path;
	`
	dRows, err := d.Conn.QueryContext(ctx, dirsQuery, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to browse subdirectories in %q: %w", cleanDir, err)
	}
	defer dRows.Close()

	type SubDirAgg struct {
		Name      string
		FullPath  string
		Size      int64
		ItemCount int64
		MaxMtime  int64
	}
	subDirMap := make(map[string]*SubDirAgg)

	for dRows.Next() {
		var subParent string
		var count, bytes, maxMtime int64
		if err := dRows.Scan(&subParent, &count, &bytes, &maxMtime); err != nil {
			return nil, fmt.Errorf("failed to scan sub dir aggregation: %w", err)
		}

		rel := strings.TrimPrefix(subParent, prefix)
		parts := strings.Split(rel, "/")
		if len(parts) > 0 && parts[0] != "" {
			dirName := parts[0]
			fullSubPath := filepath.Join(cleanDir, dirName)
			if agg, exists := subDirMap[dirName]; exists {
				agg.Size += bytes
				agg.ItemCount += count
				if maxMtime > agg.MaxMtime {
					agg.MaxMtime = maxMtime
				}
			} else {
				subDirMap[dirName] = &SubDirAgg{
					Name:      dirName,
					FullPath:  fullSubPath,
					Size:      bytes,
					ItemCount: count,
					MaxMtime:  maxMtime,
				}
			}
		}
	}
	dRows.Close()

	var dirItems []models.DirectoryBrowseItem
	for _, agg := range subDirMap {
		dirItems = append(dirItems, models.DirectoryBrowseItem{
			Path:      agg.FullPath,
			Name:      agg.Name,
			IsDir:     true,
			IsScanned: true,
			Size:      agg.Size,
			ItemCount: agg.ItemCount,
			Mtime:     time.Unix(agg.MaxMtime, 0),
		})
		resp.TotalBytes += agg.Size
	}

	// 3. Lazy Physical Directory Read: Read physical subdirectories from OS disk
	// To support full physical hierarchy browsing with faded unscanned folders.
	if entries, err := os.ReadDir(cleanDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				dirName := entry.Name()
				if _, exists := subDirMap[dirName]; !exists {
					fullSubPath := filepath.Join(cleanDir, dirName)
					modTime := time.Now()
					if info, err := entry.Info(); err == nil {
						modTime = info.ModTime()
					}
					dirItems = append(dirItems, models.DirectoryBrowseItem{
						Path:      fullSubPath,
						Name:      dirName,
						IsDir:     true,
						IsScanned: false, // Unscanned physical folder
						Size:      0,
						ItemCount: 0,
						Mtime:     modTime,
					})
				}
			}
		}
	}

	resp.Items = append(dirItems, resp.Items...)
	resp.TotalItems = len(resp.Items)

	return resp, nil
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

	// Total duplicate groups, copies & wasted bytes using precomputed duplicate_group_id index
	dupRow := d.Conn.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(wasted), 0), COALESCE(SUM(copies), 0) FROM (
			SELECT (COUNT(*) - 1) * size as wasted, COUNT(*) as copies
			FROM files
			WHERE duplicate_group_id IS NOT NULL AND duplicate_group_id != ''
			GROUP BY duplicate_group_id, size
		);
	`)
	_ = dupRow.Scan(&stats.TotalDuplicates, &stats.TotalWastedBytes, &stats.TotalDuplicateFiles)

	// Total inactive stale files (30+ days threshold)
	cutoff30 := time.Now().AddDate(0, 0, -30).Unix()
	staleRow := d.Conn.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size), 0)
		FROM files
		WHERE mtime <= ? AND staleness_score >= 0.01;
	`, cutoff30)
	_ = staleRow.Scan(&stats.TotalStaleFiles, &stats.TotalStaleBytes)

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
	SELECT id, path, size, mtime, atime, inode, extension, COALESCE(content_hash, ''), COALESCE(duplicate_group_id, ''), COALESCE(parent_path, ''), COALESCE(staleness_score, 0.0), is_system, category, last_scanned_at
	FROM files
	WHERE id = ?;
	`
	row := d.Conn.QueryRowContext(ctx, query, id)

	var f models.FileMetadata
	var mtimeSec, atimeSec, scannedSec int64
	var hash, dupGroup, parentPath, category string
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
		&dupGroup,
		&parentPath,
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
	f.DuplicateGroupID = dupGroup
	f.ParentPath = parentPath
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
