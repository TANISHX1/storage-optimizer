package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/models"
)

func setupTestActionDB(t *testing.T) (*db.DB, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	schemaPath := filepath.Join(tempDir, "schema.sql")

	schemaContent := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL UNIQUE,
		size INTEGER NOT NULL,
		mtime INTEGER NOT NULL,
		atime INTEGER NOT NULL,
		inode INTEGER NOT NULL,
		extension TEXT,
		content_hash TEXT,
		staleness_score REAL,
		is_system INTEGER DEFAULT 0,
		category TEXT DEFAULT 'user',
		last_scanned_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS actions_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path TEXT NOT NULL,
		action_mode TEXT NOT NULL,
		trashed_to_path TEXT,
		file_size INTEGER NOT NULL,
		performed_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS scan_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scanned_at INTEGER NOT NULL,
		root_path TEXT NOT NULL,
		total_files INTEGER NOT NULL,
		total_bytes INTEGER NOT NULL
	);
	`
	if err := os.WriteFile(schemaPath, []byte(schemaContent), 0644); err != nil {
		t.Fatalf("failed to write schema: %v", err)
	}

	database, err := db.Open(dbPath, schemaPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	trashDir := filepath.Join(tempDir, "fake_trash")
	return database, trashDir, tempDir
}

func TestActionTrashAndRestore(t *testing.T) {
	database, trashDir, tempDir := setupTestActionDB(t)
	defer database.Close()

	engine := NewWithCustomTrash(database, trashDir)
	ctx := context.Background()

	// Create test file on disk
	userFile := filepath.Join(tempDir, "my_document.txt")
	fileContent := []byte("Antigravity Storage Optimizer Safe Trash Test")
	if err := os.WriteFile(userFile, fileContent, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	stat, _ := os.Lstat(userFile)
	meta := models.FileMetadata{
		Path:          userFile,
		Size:          stat.Size(),
		Mtime:         stat.ModTime(),
		Atime:         stat.ModTime(),
		Inode:         0,
		Extension:     ".txt",
		Category:      models.CategoryUser,
		LastScannedAt: time.Now(),
	}

	if err := database.UpsertFileBatch(ctx, []models.FileMetadata{meta}); err != nil {
		t.Fatalf("failed to insert metadata: %v", err)
	}

	// Fetch inserted ID
	allFiles, _ := database.GetCategoryBreakdown(ctx)
	if len(allFiles) == 0 {
		t.Fatalf("expected files in DB")
	}

	row := database.Conn.QueryRow("SELECT id FROM files WHERE path = ?", userFile)
	var fileID int64
	if err := row.Scan(&fileID); err != nil {
		t.Fatalf("failed to get file ID: %v", err)
	}

	// 1. Execute Trash Action
	resp, err := engine.Execute(ctx, models.ActionRequest{
		IDs:  []int64{fileID},
		Mode: models.ActionModeTrash,
	})
	if err != nil {
		t.Fatalf("trash execution failed: %v", err)
	}

	if !resp.Success || resp.ProcessedCount != 1 {
		t.Fatalf("expected success with 1 file processed, got: %+v", resp)
	}

	// Original file must no longer exist at original path
	if _, err := os.Lstat(userFile); !os.IsNotExist(err) {
		t.Fatalf("expected original file to be moved to trash, but it still exists at %s", userFile)
	}

	// Trashed file must exist in trash/files
	trashedPath := *resp.Actions[0].TrashedToPath
	if _, err := os.Lstat(trashedPath); err != nil {
		t.Fatalf("trashed file not found in trash dir %s: %v", trashedPath, err)
	}

	// .trashinfo must exist in trash/info
	infoPath := filepath.Join(trashDir, "info", filepath.Base(trashedPath)+".trashinfo")
	infoData, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatalf(".trashinfo file not created: %v", err)
	}
	if len(infoData) == 0 {
		t.Fatalf(".trashinfo file is empty")
	}

	// 2. Execute Restore
	actionLogID := resp.Actions[0].ID
	restoredLog, err := engine.Restore(ctx, actionLogID)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	if restoredLog.FilePath != userFile {
		t.Fatalf("restored path mismatch: got %s, expected %s", restoredLog.FilePath, userFile)
	}

	// Verify file is back at original path with exact same contents
	restoredContent, err := os.ReadFile(userFile)
	if err != nil {
		t.Fatalf("failed to read restored file: %v", err)
	}
	if string(restoredContent) != string(fileContent) {
		t.Fatalf("content mismatch after restore: got %q, expected %q", string(restoredContent), string(fileContent))
	}

	// .trashinfo must be deleted
	if _, err := os.Lstat(infoPath); !os.IsNotExist(err) {
		t.Fatalf("expected .trashinfo to be removed after restore")
	}
}

func TestSafetyGatingProtectedFiles(t *testing.T) {
	database, trashDir, _ := setupTestActionDB(t)
	defer database.Close()

	engine := NewWithCustomTrash(database, trashDir)
	ctx := context.Background()

	meta := models.FileMetadata{
		Path:          "/etc/systemd/system.conf",
		Size:          1024,
		Mtime:         time.Now(),
		Atime:         time.Now(),
		Inode:         12345,
		Extension:     ".conf",
		Category:      models.CategorySystemProtected,
		IsSystem:      true,
		LastScannedAt: time.Now(),
	}

	_ = database.UpsertFileBatch(ctx, []models.FileMetadata{meta})

	row := database.Conn.QueryRow("SELECT id FROM files WHERE path = ?", meta.Path)
	var fileID int64
	_ = row.Scan(&fileID)

	// Attempting to trash or delete a system protected file must fail
	resp, err := engine.Execute(ctx, models.ActionRequest{
		IDs:  []int64{fileID},
		Mode: models.ActionModeTrash,
	})

	if err == nil && (resp != nil && resp.Success) {
		t.Fatalf("expected safety error when trying to delete system protected file, but got success")
	}
}

func TestPermanentDelete(t *testing.T) {
	database, trashDir, tempDir := setupTestActionDB(t)
	defer database.Close()

	engine := NewWithCustomTrash(database, trashDir)
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "temp_to_delete.log")
	if err := os.WriteFile(testFile, []byte("log data to destroy"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	stat, _ := os.Lstat(testFile)
	meta := models.FileMetadata{
		Path:          testFile,
		Size:          stat.Size(),
		Mtime:         stat.ModTime(),
		Atime:         stat.ModTime(),
		Category:      models.CategoryTemp,
		LastScannedAt: time.Now(),
	}

	_ = database.UpsertFileBatch(ctx, []models.FileMetadata{meta})
	row := database.Conn.QueryRow("SELECT id FROM files WHERE path = ?", testFile)
	var fileID int64
	_ = row.Scan(&fileID)

	resp, err := engine.Execute(ctx, models.ActionRequest{
		IDs:  []int64{fileID},
		Mode: models.ActionModePermanent,
	})
	if err != nil {
		t.Fatalf("permanent delete failed: %v", err)
	}

	if !resp.Success || resp.ProcessedCount != 1 {
		t.Fatalf("expected 1 file permanently deleted, got: %+v", resp)
	}

	if _, err := os.Lstat(testFile); !os.IsNotExist(err) {
		t.Fatalf("file still exists on disk after permanent deletion")
	}

	// Verify audit log recorded
	logs, err := database.GetActionLogs(ctx, 10)
	if err != nil || len(logs) == 0 {
		t.Fatalf("expected audit log record for permanent delete")
	}
	if logs[0].ActionMode != models.ActionModePermanent {
		t.Fatalf("expected action mode permanent in audit log, got %s", logs[0].ActionMode)
	}
}
