package models

import "time"

// ============================================================================
// SYSTEM MODEL DEFINITIONS
//
// DATA FLOW CONTRACT:
// 1. Filesystem Scanner -> FileMetadata (Raw Linux inode/stat + OS System Category classification)
// 2. Writer Channel Funnel -> SQLite "files" table
// 3. Duplicate Engine -> ContentHash (SHA-256) updates & DuplicateGroup reports
// 4. Staleness Engine -> StalenessScore computation & StaleReport summaries
// 5. Action Layer -> ActionLog audit trail & deletion execution (System Protection Gating)
// ============================================================================

// FileCategory defines classification for safe filtering and system protection.
type FileCategory string

const (
	CategoryUser            FileCategory = "user"             // Standard user files, documents, media, code
	CategorySystemProtected FileCategory = "system_protected" // Critical OS binaries/libraries (/bin, /usr/lib, /etc) - BLOCKED from deletion
	CategorySystemLog       FileCategory = "system_log"       // OS and application logs (/var/log, *.log) - Eligible for stale cleanup
	CategorySystemCache     FileCategory = "system_cache"     // System & package caches (/var/cache, apt, dnf)
	CategoryCrashDump       FileCategory = "crash_dump"       // Application crash snapshots, core dumps (/var/crash, coredump)
	CategoryTemp            FileCategory = "temp"             // Temporary staging files (/tmp, /var/tmp)
)

// FileMetadata represents the complete metadata captured for a single regular file
// from the Linux filesystem before or after indexing in SQLite.
type FileMetadata struct {
	ID             int64        `json:"id"`              // SQLite autoincrement primary key
	Path           string       `json:"path"`            // Absolute path on filesystem (UNIQUE)
	Size           int64        `json:"size"`            // Size in bytes
	Mtime          time.Time    `json:"mtime"`           // Last modification time
	Atime          time.Time    `json:"atime"`           // Last access time (Linux atime/relatime)
	Inode          uint64       `json:"inode"`           // Linux filesystem Inode number (identifies hardlinks)
	Extension      string       `json:"extension"`       // Lowercase file extension (e.g. ".log", ".mp4")
	ContentHash    string       `json:"content_hash"`    // SHA-256 hexadecimal digest (populated during Phase 2)
	StalenessScore float64      `json:"staleness_score"` // 0.0 (fresh/protected) to 1.0 (extremely stale) (Phase 3)
	IsSystem       bool         `json:"is_system"`       // True if file resides in OS system paths
	Category       FileCategory `json:"category"`        // File category (user, system_protected, system_log, crash_dump, temp, system_cache)
	LastScannedAt  time.Time    `json:"last_scanned_at"`  // Timestamp of the scan that recorded this entry
}

// ScanSnapshot captures a point-in-time summary of the indexed filesystem.
// This table serves as the primary time-series input for Sahil's Python forecasting model.
type ScanSnapshot struct {
	ID         int64     `json:"id"`
	ScannedAt  time.Time `json:"scanned_at"`  // Unix timestamp of snapshot completion
	RootPath   string    `json:"root_path"`   // Target root folder that was scanned
	TotalFiles int64     `json:"total_files"` // Total regular files found
	TotalBytes int64     `json:"total_bytes"` // Aggregated storage size in bytes
}

// DuplicateGroup represents a cluster of identical files sharing the exact same size and SHA-256 hash.
type DuplicateGroup struct {
	ContentHash    string         `json:"content_hash"`    // SHA-256 hexadecimal digest
	FileSize       int64          `json:"file_size"`       // Size of each individual file in bytes
	DuplicateCount int            `json:"duplicate_count"` // Number of identical copies (>= 2)
	WastedBytes    int64          `json:"wasted_bytes"`    // Total reclaimable storage: (DuplicateCount - 1) * FileSize
	Files          []FileMetadata `json:"files"`           // List of file entries in this duplicate cluster
}

// DedupReport contains aggregated metrics and grouped results from duplicate detection.
type DedupReport struct {
	TotalGroups         int              `json:"total_groups"`          // Total clusters of duplicate files
	TotalDuplicateFiles int              `json:"total_duplicate_files"` // Total count of redundant files
	TotalWastedBytes    int64            `json:"total_wasted_bytes"`    // Total storage that can be reclaimed
	Groups              []DuplicateGroup `json:"groups"`                // Sorted duplicate clusters (largest wasted first)
	Duration            time.Duration    `json:"duration"`              // Time taken to perform duplicate analysis
}

// FileHashUpdate represents a lightweight DTO for batch updating file content hashes in SQLite.
type FileHashUpdate struct {
	ID          int64  `json:"id"`
	ContentHash string `json:"content_hash"`
}

// FileStalenessUpdate represents a lightweight DTO for batch updating staleness scores in SQLite.
type FileStalenessUpdate struct {
	ID             int64   `json:"id"`
	StalenessScore float64 `json:"staleness_score"`
}

// StaleReport aggregates results of files exceeding staleness thresholds.
type StaleReport struct {
	ThresholdDays int            `json:"threshold_days"` // Minimum untouched days requested (e.g. 30, 90, 180)
	TotalFiles    int            `json:"total_files"`    // Total stale files matching criteria
	TotalBytes    int64          `json:"total_bytes"`    // Total disk storage occupied by stale files
	Files         []FileMetadata `json:"files"`          // Stale files sorted by score (descending) and size (descending)
	Duration      time.Duration  `json:"duration"`       // Computation duration
}

// CategoryBreakdown represents aggregated file counts and bytes for a single category.
type CategoryBreakdown struct {
	Category   FileCategory `json:"category"`
	TotalFiles int64        `json:"total_files"`
	TotalBytes int64        `json:"total_bytes"`
}

// StorageStats represents overall storage analysis metrics for dashboard views.
type StorageStats struct {
	TotalFiles       int64               `json:"total_files"`
	TotalBytes       int64               `json:"total_bytes"`
	TotalDuplicates  int                 `json:"total_duplicates"`
	TotalWastedBytes int64               `json:"total_wasted_bytes"`
	TotalSnapshots   int                 `json:"total_snapshots"`
	Categories       []CategoryBreakdown `json:"categories"`
}

// ScanRequest defines the payload for initiating a directory scan via HTTP API.
type ScanRequest struct {
	Path    string `json:"path"`
	Full    bool   `json:"full"`
	Workers int    `json:"workers"`
	NoPrune bool   `json:"no_prune"`
}

// ScanStatus represents the live or last-completed scan status.
type ScanStatus struct {
	Status       string     `json:"status"` // "idle", "scanning", "completed", "failed"
	TargetPath   string     `json:"target_path"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	FilesScanned int64      `json:"files_scanned"`
	DirsScanned  int64      `json:"dirs_scanned"`
	TotalBytes   int64      `json:"total_bytes"`
	PrunedRows   int64      `json:"pruned_rows"`
	Error        string     `json:"error,omitempty"`
	SnapshotID   int64      `json:"snapshot_id,omitempty"`
}

// ActionMode represents the deletion strategy requested by the user.
type ActionMode string

const (
	ActionModeTrash     ActionMode = "trash"     // Safe: relocate to <app_data>/trash/
	ActionModePermanent ActionMode = "permanent" // Irreversible: os.Remove with audit logging
)

// ActionLog represents a persistent record of any executed deletion/trash action.
// Every destructive action is audited here BEFORE filesystem mutation occurs.
type ActionLog struct {
	ID            int64      `json:"id"`
	FilePath      string     `json:"file_path"`       // Original absolute path
	ActionMode    ActionMode `json:"action_mode"`     // "trash" or "permanent"
	TrashedToPath *string    `json:"trashed_to_path"` // Target path if trashed; nil if permanent
	FileSize      int64      `json:"file_size"`       // Size of affected file
	PerformedAt   time.Time  `json:"performed_at"`    // Timestamp of execution
}

// ActionRequest defines the payload for executing a cleanup action.
type ActionRequest struct {
	IDs  []int64    `json:"ids"`
	Mode ActionMode `json:"mode"` // "trash" or "permanent"
}

// ActionResponse summarizes the result of an executed cleanup action.
type ActionResponse struct {
	Success        bool        `json:"success"`
	Mode           ActionMode  `json:"mode"`
	ProcessedCount int         `json:"processed_count"`
	FreedBytes     int64       `json:"freed_bytes"`
	Actions        []ActionLog `json:"actions"`
	Error          string      `json:"error,omitempty"`
}

