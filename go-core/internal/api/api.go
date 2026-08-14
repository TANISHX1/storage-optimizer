package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"storage-optimizer/go-core/internal/action"
	"storage-optimizer/go-core/internal/db"
	"storage-optimizer/go-core/internal/dedup"
	"storage-optimizer/go-core/internal/models"
	"storage-optimizer/go-core/internal/scanner"
	"storage-optimizer/go-core/internal/stale"
)

// ============================================================================
// LOCAL HTTP REST API SERVER (PHASE 5)
//
// ARCHITECTURE & INTEGRATION ROLE:
// 1. Decoupled Interface:
//    - Exposes standard HTTP/JSON endpoints on 127.0.0.1:8080.
//    - Serves as the communication bridge for both the Wails GUI frontend and
//      Sahil's Python forecasting/recommendation layer (Day 7).
//
// 2. Single Source of Truth & Zero Lock Contention:
//    - Python scripts and GUI views do NOT open the SQLite database directly.
//    - All queries and mutations funnel through Go's managed connection pool.
//
// 3. Concurrency & Scan State Machine:
//    - Background scan execution is guarded by `scanMu`.
//    - Real-time progress is observable via `GET /api/v1/scan/status`.
// ============================================================================

// Server manages HTTP routing, middleware, and database access.
type Server struct {
	db          *db.DB
	port        int
	httpServer  *http.Server
	scanMu      sync.RWMutex
	currentScan *models.ScanStatus
}

// New creates and configures a new HTTP REST API Server.
func New(database *db.DB, port int) *Server {
	if port <= 0 {
		port = 8080
	}

	s := &Server{
		db:   database,
		port: port,
		currentScan: &models.ScanStatus{
			Status: "idle",
		},
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Wrap mux with CORS, logging, and recovery middleware chain
	handler := s.corsMiddleware(s.loggingMiddleware(s.recoveryMiddleware(mux)))

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// Start runs the HTTP server and blocks until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	errChan := make(chan error, 1)

	go func() {
		log.Printf("[API] Server listening on http://%s\n", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("[API] Shutting down HTTP server gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)

	case err := <-errChan:
		return fmt.Errorf("HTTP server error: %w", err)
	}
}

// registerRoutes wires up all REST API endpoints under /api/v1 and serves GUI on /.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Health & System Summary
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/stats", s.handleStats)

	// Filesystem Scanning
	mux.HandleFunc("/api/v1/scan", s.handleScan)
	mux.HandleFunc("/api/v1/scan/status", s.handleScanStatus)

	// Duplicate Detection & Wasted Space
	mux.HandleFunc("/api/v1/files/duplicates", s.handleDuplicates)

	// Staleness & Inactive Storage
	mux.HandleFunc("/api/v1/files/stale", s.handleStale)

	// Historical Snapshots (Time-Series for Sahil's Python Layer)
	mux.HandleFunc("/api/v1/snapshots", s.handleSnapshots)

	// Actions & Audit History (Phase 6 Integration)
	mux.HandleFunc("/api/v1/actions/history", s.handleActionHistory)
	mux.HandleFunc("/api/v1/actions/restore", s.handleActionRestore)
	mux.HandleFunc("/api/v1/actions", s.handleActions)

	// GUI Frontend Static File Serving
	guiCandidates := []string{
		"gui/frontend/dist",
		"../gui/frontend/dist",
		"/home/blazex/Documents/git/storage-optimizer/gui/frontend/dist",
	}

	for _, dir := range guiCandidates {
		if abs, err := filepath.Abs(dir); err == nil {
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				log.Printf("[API] Serving GUI Dashboard from %s at http://127.0.0.1:%d/\n", abs, s.port)
				mux.Handle("/", http.FileServer(http.Dir(abs)))
				break
			}
		}
	}
}


// ============================================================================
// HANDLER IMPLEMENTATIONS
// ============================================================================

// handleHealth returns a basic health check and timestamp.
// GET /api/v1/health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "healthy",
		"service":   "storage-optimizer-core",
		"version":   "1.0.0",
		"timestamp": time.Now().UTC(),
	})
}

// handleStats returns high-level storage overview and category breakdowns.
// GET /api/v1/stats
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	stats, err := s.db.GetStorageStats(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to query storage stats: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, stats)
}

// handleScan triggers a background or synchronous filesystem scan.
// POST /api/v1/scan
// Payload: {"path": "/target/dir", "workers": 8, "full": false, "no_prune": false}
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req models.ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON request body: %v", err))
		return
	}

	if req.Path == "" {
		s.writeError(w, http.StatusBadRequest, "Missing required field: 'path'")
		return
	}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	if req.Workers <= 0 {
		req.Workers = runtime.NumCPU()
	}

	s.scanMu.Lock()
	if s.currentScan != nil && s.currentScan.Status == "scanning" {
		s.scanMu.Unlock()
		s.writeError(w, http.StatusConflict, fmt.Sprintf("A scan is already in progress for %q", s.currentScan.TargetPath))
		return
	}

	startTime := time.Now()
	s.currentScan = &models.ScanStatus{
		Status:     "scanning",
		TargetPath: absPath,
		StartedAt:  startTime,
	}
	s.scanMu.Unlock()

	// Execute scan asynchronously in background goroutine
	go s.executeBackgroundScan(absPath, req.Workers, req.NoPrune)

	s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":      "scanning",
		"target_path": absPath,
		"started_at":  startTime,
		"workers":     req.Workers,
		"message":     "Scan started in background. Poll /api/v1/scan/status for progress.",
	})
}

// executeBackgroundScan coordinates the worker pool and database batch writer.
func (s *Server) executeBackgroundScan(targetPath string, workers int, noPrune bool) {
	ctx := context.Background()

	metaChan := make(chan models.FileMetadata, 4096)
	errChan := make(chan error, 16)

	writerDone := make(chan struct{})
	go func() {
		s.db.BatchWriter(ctx, metaChan, 500, 50*time.Millisecond, errChan)
		close(writerDone)
	}()

	scn := scanner.New(scanner.ScanConfig{
		RootPath:      targetPath,
		NumWorkers:    workers,
		ChannelBuffer: 4096,
	})

	stats, scanErr := scn.Scan(ctx, metaChan)
	close(metaChan)
	<-writerDone

	var prunedCount int64
	if !noPrune {
		pCount, pruneErr := s.db.PruneDeletedFiles(ctx, targetPath, stats.ScanStart)
		if pruneErr != nil {
			log.Printf("[API Scan] Pruning error: %v\n", pruneErr)
		} else {
			prunedCount = pCount
		}
	}

	snapshotID, snapErr := s.db.RecordSnapshot(ctx, targetPath, stats.FilesScanned, stats.TotalBytes)
	if snapErr != nil {
		log.Printf("[API Scan] Snapshot recording error: %v\n", snapErr)
	}

	completedAt := time.Now()
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	if scanErr != nil {
		s.currentScan.Status = "failed"
		s.currentScan.Error = scanErr.Error()
	} else {
		s.currentScan.Status = "completed"
		s.currentScan.FilesScanned = stats.FilesScanned
		s.currentScan.DirsScanned = stats.DirsScanned
		s.currentScan.TotalBytes = stats.TotalBytes
		s.currentScan.PrunedRows = prunedCount
		s.currentScan.SnapshotID = snapshotID
		s.currentScan.CompletedAt = &completedAt
	}
}

// handleScanStatus returns the current or most recent scan progress.
// GET /api/v1/scan/status
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.scanMu.RLock()
	statusCopy := *s.currentScan
	s.scanMu.RUnlock()

	s.writeJSON(w, http.StatusOK, statusCopy)
}

// handleDuplicates executes duplicate detection or retrieves existing groups.
// GET /api/v1/files/duplicates?full=true&workers=8
func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	fullRehash := r.URL.Query().Get("full") == "true"
	workersStr := r.URL.Query().Get("workers")
	workers := runtime.NumCPU()
	if workersStr != "" {
		if wVal, err := strconv.Atoi(workersStr); err == nil && wVal > 0 {
			workers = wVal
		}
	}

	engine := dedup.New(s.db, dedup.Config{
		NumWorkers:  workers,
		ForceRehash: fullRehash,
	})

	report, err := engine.Execute(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Duplicate detection failed: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, report)
}

// handleStale calculates or retrieves inactive files.
// GET /api/v1/files/stale?days=30&min_score=0.05&limit=50
func (s *Server) handleStale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	days := 30
	if dStr := r.URL.Query().Get("days"); dStr != "" {
		if dVal, err := strconv.Atoi(dStr); err == nil && dVal >= 0 {
			days = dVal
		}
	}

	minScore := 0.05
	if sStr := r.URL.Query().Get("min_score"); sStr != "" {
		if sVal, err := strconv.ParseFloat(sStr, 64); err == nil && sVal >= 0.0 {
			minScore = sVal
		}
	}

	limit := 100
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if lVal, err := strconv.Atoi(lStr); err == nil && lVal > 0 {
			limit = lVal
		}
	}

	engine := stale.New(s.db, stale.Config{
		NumWorkers: runtime.NumCPU(),
	})

	report, err := engine.FindStaleFiles(r.Context(), days, minScore, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Staleness calculation failed: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, report)
}

// handleSnapshots returns historical scan snapshots for time-series analytics (Sahil's Python Layer).
// GET /api/v1/snapshots?limit=50&root=/optional/path
func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if lVal, err := strconv.Atoi(lStr); err == nil && lVal > 0 {
			limit = lVal
		}
	}

	rootFilter := r.URL.Query().Get("root")

	var snapshots []models.ScanSnapshot
	var err error

	if rootFilter != "" {
		snapshots, err = s.db.GetSnapshotsByRoot(r.Context(), rootFilter, limit)
	} else {
		snapshots, err = s.db.GetSnapshots(r.Context(), limit)
	}

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to query snapshots: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_count": len(snapshots),
		"snapshots":   snapshots,
	})
}

// handleActionHistory returns the persistent audit log of cleanup actions.
// GET /api/v1/actions/history?limit=50
func (s *Server) handleActionHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if lVal, err := strconv.Atoi(lStr); err == nil && lVal > 0 {
			limit = lVal
		}
	}

	logs, err := s.db.GetActionLogs(r.Context(), limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to query action logs: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_count": len(logs),
		"actions":     logs,
	})
}

// handleActions executes user-confirmed cleanup actions (trash or permanent delete).
// POST /api/v1/actions
// Payload: {"ids": [1, 2], "mode": "trash"}
func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req models.ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON payload: %v", err))
		return
	}

	if len(req.IDs) == 0 {
		s.writeError(w, http.StatusBadRequest, "No file IDs provided for action execution")
		return
	}

	engine := action.New(s.db)
	resp, err := engine.Execute(r.Context(), req)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, resp)
		return
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleActionRestore restores a previously trashed file back to original location.
// POST /api/v1/actions/restore
// Payload: {"action_id": 10} or Query: ?id=10
func (s *Server) handleActionRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var actionID int64
	if qID := r.URL.Query().Get("id"); qID != "" {
		if val, err := strconv.ParseInt(qID, 10, 64); err == nil {
			actionID = val
		}
	}

	if actionID == 0 && r.Body != nil {
		var req struct {
			ActionID int64 `json:"action_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		actionID = req.ActionID
	}

	if actionID <= 0 {
		s.writeError(w, http.StatusBadRequest, "Missing or invalid 'action_id'")
		return
	}

	engine := action.New(s.db)
	restoredLog, err := engine.Restore(r.Context(), actionID)
	if err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("Restore failed: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("Successfully restored %s", restoredLog.FilePath),
		"restored": restoredLog,
	})
}

// ============================================================================
// HTTP MIDDLEWARE PIPELINE
// ============================================================================

// corsMiddleware sets headers allowing the Wails desktop frontend and development clients to communicate.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware records incoming HTTP requests, response timing, and paths.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		log.Printf("[HTTP] %s %s (%s)\n", r.Method, r.URL.Path, duration.Round(time.Millisecond))
	})
}

// recoveryMiddleware catches any unforeseen handler panics and returns 500 JSON.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[PANIC RECOVERED] %v\n", rec)
				s.writeError(w, http.StatusInternalServerError, "Internal server error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON serializes data as JSON and sets Content-Type application/json.
func (s *Server) writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[API] Failed to encode JSON response: %v\n", err)
	}
}

// writeError outputs a standardized JSON error message.
func (s *Server) writeError(w http.ResponseWriter, statusCode int, message string) {
	s.writeJSON(w, statusCode, map[string]interface{}{
		"error":       message,
		"status_code": statusCode,
		"timestamp":   time.Now().UTC(),
	})
}
