package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/models"
)

// Critical Linux system directory prefixes strictly protected against mutation.
var protectedRoots = []string{
	"/bin",
	"/sbin",
	"/usr",
	"/lib",
	"/lib64",
	"/etc",
	"/boot",
	"/opt",
	"/sys",
	"/proc",
	"/dev",
}

// Engine executes validated file deletion, XDG trash relocation, and restoration.
type Engine struct {
	db       *db.DB
	trashDir string
}

// New creates an Engine adhering to FreeDesktop.org XDG Trash specifications.
func New(database *db.DB) *Engine {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	xdgTrash := filepath.Join(home, ".local", "share", "Trash")
	return &Engine{
		db:       database,
		trashDir: xdgTrash,
	}
}

// NewWithCustomTrash creates an Engine with a specific trash directory (for testing/sandboxing).
func NewWithCustomTrash(database *db.DB, customTrashDir string) *Engine {
	return &Engine{
		db:       database,
		trashDir: customTrashDir,
	}
}

// Execute orchestrates user-confirmed file cleanup (trash or permanent delete).
func (e *Engine) Execute(ctx context.Context, req models.ActionRequest) (*models.ActionResponse, error) {
	if len(req.IDs) == 0 {
		return &models.ActionResponse{
			Success:        true,
			Mode:           req.Mode,
			ProcessedCount: 0,
			FreedBytes:     0,
		}, nil
	}

	if req.Mode != models.ActionModeTrash && req.Mode != models.ActionModePermanent {
		return nil, fmt.Errorf("invalid action mode %q: must be 'trash' or 'permanent'", req.Mode)
	}

	// Fetch candidate file records
	files, err := e.db.GetFilesByIDs(ctx, req.IDs)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve target files from database: %w", err)
	}

	resp := &models.ActionResponse{
		Success: true,
		Mode:    req.Mode,
	}

	for _, file := range files {
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		default:
		}

		// 1. OS Safety Verification
		if err := e.verifySafety(&file); err != nil {
			resp.Error = fmt.Sprintf("safety violation on %s: %v", file.Path, err)
			resp.Success = false
			return resp, err
		}

		// 2. Execute requested mutation mode
		var actionLog *models.ActionLog
		var actionErr error

		if req.Mode == models.ActionModeTrash {
			actionLog, actionErr = e.trashFile(ctx, file)
		} else {
			actionLog, actionErr = e.permanentDeleteFile(ctx, file)
		}

		if actionErr != nil {
			resp.Error = fmt.Sprintf("action failed on %s: %v", file.Path, actionErr)
			resp.Success = false
			return resp, actionErr
		}

		if actionLog != nil {
			resp.ProcessedCount++
			resp.FreedBytes += actionLog.FileSize
			resp.Actions = append(resp.Actions, *actionLog)
		}
	}

	return resp, nil
}

// verifySafety enforces hard OS protection gates before any filesystem mutation.
func (e *Engine) verifySafety(file *models.FileMetadata) error {
	// A. Block Protected Categories
	if file.Category == models.CategorySystemProtected {
		return fmt.Errorf("file %q is classified as system_protected (OS core component)", file.Path)
	}

	// B. Block Root System Directories
	cleanPath := filepath.Clean(file.Path)
	for _, root := range protectedRoots {
		if cleanPath == root || strings.HasPrefix(cleanPath, root+"/") {
			return fmt.Errorf("path %q resides within protected system hierarchy %q", cleanPath, root)
		}
	}

	// C. Pre-Mutation Stat & Inode Verification
	stat, err := os.Lstat(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("file no longer exists on disk: %s", cleanPath)
		}
		return fmt.Errorf("cannot stat file: %w", err)
	}

	if stat.IsDir() {
		return fmt.Errorf("path %q is a directory, not a regular file", cleanPath)
	}

	if stat.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink; mutating symlinks directly is prohibited", cleanPath)
	}

	// Verify Inode & Size Match to prevent race condition or swap attacks
	if sysStat, ok := stat.Sys().(*syscall.Stat_t); ok {
		if uint64(sysStat.Ino) != file.Inode && file.Inode != 0 {
			return fmt.Errorf("disk inode (%d) does not match database record (%d): file was swapped", sysStat.Ino, file.Inode)
		}
	}

	if stat.Size() != file.Size {
		return fmt.Errorf("disk file size (%d B) does not match database record (%d B): file was modified", stat.Size(), file.Size)
	}

	return nil
}

// trashFile relocates the file into the XDG FreeDesktop.org Trash directory.
func (e *Engine) trashFile(ctx context.Context, file models.FileMetadata) (*models.ActionLog, error) {
	trashFilesDir := filepath.Join(e.trashDir, "files")
	trashInfoDir := filepath.Join(e.trashDir, "info")

	if err := os.MkdirAll(trashFilesDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create trash files dir: %w", err)
	}
	if err := os.MkdirAll(trashInfoDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create trash info dir: %w", err)
	}

	baseName := filepath.Base(file.Path)
	destFileName := baseName
	destFilePath := filepath.Join(trashFilesDir, destFileName)

	// Resolve trash file name collisions
	counter := 1
	for {
		if _, err := os.Lstat(destFilePath); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(baseName)
		nameWithoutExt := strings.TrimSuffix(baseName, ext)
		destFileName = fmt.Sprintf("%s.%d%s", nameWithoutExt, counter, ext)
		destFilePath = filepath.Join(trashFilesDir, destFileName)
		counter++
	}

	trashInfoPath := filepath.Join(trashInfoDir, destFileName+".trashinfo")

	// Write XDG FreeDesktop .trashinfo metadata
	now := time.Now()
	// Format deletion date in ISO 8601 (YYYY-MM-DDThh:mm:ss)
	delDate := now.Format("2006-01-02T15:04:05")
	// URL-escape the original path as recommended by FreeDesktop spec
	escapedPath := url.PathEscape(file.Path)
	// Some desktop environments prefer raw or escaped paths; standard XDG format:
	trashInfoContent := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n", file.Path, delDate)
	_ = escapedPath

	if err := os.WriteFile(trashInfoPath, []byte(trashInfoContent), 0600); err != nil {
		return nil, fmt.Errorf("failed to write .trashinfo metadata: %w", err)
	}

	// Move file into trash
	if err := moveFile(file.Path, destFilePath); err != nil {
		// Rollback info file
		_ = os.Remove(trashInfoPath)
		return nil, fmt.Errorf("failed to move file to trash: %w", err)
	}

	// Audit Log
	actionLogID, err := e.db.LogAction(ctx, file.Path, models.ActionModeTrash, &destFilePath, file.Size)
	if err != nil {
		// Non-fatal, but logged
		fmt.Printf("[WARN] Failed to write action log: %v\n", err)
	}

	// Purge file record from database
	_ = e.db.DeleteFileByID(ctx, file.ID)

	return &models.ActionLog{
		ID:            actionLogID,
		FilePath:      file.Path,
		ActionMode:    models.ActionModeTrash,
		TrashedToPath: &destFilePath,
		FileSize:      file.Size,
		PerformedAt:   now,
	}, nil
}

// permanentDeleteFile physically removes the file from disk after writing audit logs.
func (e *Engine) permanentDeleteFile(ctx context.Context, file models.FileMetadata) (*models.ActionLog, error) {
	now := time.Now()

	// 1. Mandatory Audit Logging BEFORE filesystem mutation
	actionLogID, err := e.db.LogAction(ctx, file.Path, models.ActionModePermanent, nil, file.Size)
	if err != nil {
		return nil, fmt.Errorf("failed to record action log before deletion: %w", err)
	}

	// 2. Physical File Deletion
	if err := os.Remove(file.Path); err != nil {
		return nil, fmt.Errorf("os.Remove failed on %s: %w", file.Path, err)
	}

	// 3. Purge file record from database
	_ = e.db.DeleteFileByID(ctx, file.ID)

	return &models.ActionLog{
		ID:            actionLogID,
		FilePath:      file.Path,
		ActionMode:    models.ActionModePermanent,
		TrashedToPath: nil,
		FileSize:      file.Size,
		PerformedAt:   now,
	}, nil
}

// Restore restores a previously trashed file back to its original location.
func (e *Engine) Restore(ctx context.Context, actionLogID int64) (*models.ActionLog, error) {
	logEntry, err := e.db.GetActionLogByID(ctx, actionLogID)
	if err != nil {
		return nil, fmt.Errorf("failed to query action log #%d: %w", actionLogID, err)
	}
	if logEntry == nil {
		return nil, fmt.Errorf("action log #%d not found", actionLogID)
	}

	if logEntry.ActionMode != models.ActionModeTrash {
		return nil, fmt.Errorf("cannot restore action #%d: action mode is %q (only 'trash' can be restored)", actionLogID, logEntry.ActionMode)
	}

	if logEntry.TrashedToPath == nil || *logEntry.TrashedToPath == "" {
		return nil, fmt.Errorf("action log #%d has no trashed path recorded", actionLogID)
	}

	trashedPath := *logEntry.TrashedToPath
	origPath := logEntry.FilePath

	// Verify trashed file exists
	stat, err := os.Lstat(trashedPath)
	if err != nil {
		return nil, fmt.Errorf("trashed file %q not found on disk: %w", trashedPath, err)
	}

	// Ensure destination directory exists
	targetDir := filepath.Dir(origPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory %q: %w", targetDir, err)
	}

	// Verify destination path doesn't already exist
	if _, err := os.Lstat(origPath); err == nil {
		return nil, fmt.Errorf("cannot restore: destination %q already exists on disk", origPath)
	}

	// Move file back to original location
	if err := moveFile(trashedPath, origPath); err != nil {
		return nil, fmt.Errorf("failed to restore file to %s: %w", origPath, err)
	}

	// Remove .trashinfo file
	baseName := filepath.Base(trashedPath)
	trashInfoPath := filepath.Join(e.trashDir, "info", baseName+".trashinfo")
	_ = os.Remove(trashInfoPath)

	// Clean up action log entry
	_ = e.db.DeleteActionLog(ctx, actionLogID)

	// Re-stat the restored file and re-insert into database
	if restoredStat, err := os.Lstat(origPath); err == nil {
		var inode uint64
		var atime, mtime time.Time
		mtime = restoredStat.ModTime()
		atime = mtime
		if sysStat, ok := restoredStat.Sys().(*syscall.Stat_t); ok {
			inode = uint64(sysStat.Ino)
			atime = time.Unix(sysStat.Atim.Sec, sysStat.Atim.Nsec)
			mtime = time.Unix(sysStat.Mtim.Sec, sysStat.Mtim.Nsec)
		}

		// Insert back into files table
		meta := models.FileMetadata{
			Path:          origPath,
			Size:          restoredStat.Size(),
			Mtime:         mtime,
			Atime:         atime,
			Inode:         inode,
			Extension:     strings.ToLower(filepath.Ext(origPath)),
			IsSystem:      false,
			Category:      models.CategoryUser,
			LastScannedAt: time.Now(),
		}
		_ = e.db.UpsertFileBatch(ctx, []models.FileMetadata{meta})
	}

	_ = stat
	return logEntry, nil
}

// moveFile moves a file across filesystems safely (falls back to copy+delete on cross-device link).
func moveFile(src, dst string) error {
	// Attempt fast atomic rename first
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// If cross-device link error (EXDEV), fall back to copy and delete
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) || isCrossDeviceError(err) {
		return copyAndDelete(src, dst)
	}

	return err
}

func isCrossDeviceError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EXDEV) || strings.Contains(err.Error(), "cross-device link") || strings.Contains(err.Error(), "invalid cross-device link")
}

func copyAndDelete(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source for copy: %w", err)
	}
	defer srcFile.Close()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcStat.Mode().Perm())
	if err != nil {
		return fmt.Errorf("failed to create destination for copy: %w", err)
	}
	defer dstFile.Close()

	// 64 KB buffer
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(dstFile, srcFile, buf); err != nil {
		return fmt.Errorf("failed to copy data: %w", err)
	}

	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination: %w", err)
	}

	srcFile.Close()
	dstFile.Close()

	// Delete source after successful copy
	return os.Remove(src)
}
