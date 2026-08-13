package models

import "time"

// ============================================================================
// SYSTEM MODEL DEFINITIONS
//
// DATA FLOW CONTRACT:
// 1. Filesystem Scanner -> FileMetadata (Raw Linux inode/stat extraction)
// 2. Writer Channel Funnel -> SQLite "files" table
// 3. Duplicate Engine -> ContentHash (SHA-256) updates & DuplicateGroup reports
// 4. Staleness Engine -> StalenessScore computation
// 5. Action Layer -> ActionLog audit trail & deletion execution
// ============================================================================

// FileMetadata represents the complete metadata captured for a single regular file
// from the Linux filesystem before or after indexing in SQLite.
type FileMetadata struct {
	ID             int64     `json:"id"`              // SQLite autoincrement primary key
	Path           string    `json:"path"`            // Absolute path on filesystem (UNIQUE)
	Size           int64     `json:"size"`            // Size in bytes
	Mtime          time.Time `json:"mtime"`           // Last modification time
	Atime          time.Time `json:"atime"`           // Last access time (Linux atime/relatime)
	Inode          uint64    `json:"inode"`           // Linux filesystem Inode number (identifies hardlinks)
	Extension      string    `json:"extension"`       // Lowercase file extension (e.g. ".log", ".mp4")
	ContentHash    string    `json:"content_hash"`    // SHA-256 hexadecimal digest (populated during Phase 2)
	StalenessScore float64   `json:"staleness_score"` // 0.0 (fresh) to 1.0 (extremely stale) (populated in Phase 3)
	LastScannedAt  time.Time `json:"last_scanned_at"`  // Timestamp of the scan that recorded this entry
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
