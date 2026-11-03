package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LogStatsResponse represents disk_log capacity telemetry.
type LogStatsResponse struct {
	LogBaseDir    string `json:"log_base_dir"`
	TotalBytes    int64  `json:"total_bytes"`
	UsedBytes     int64  `json:"used_bytes"`
	LogFilesCount int    `json:"log_files_count"`
	LogFilesBytes int64  `json:"log_files_bytes"`
}

// LogCleanupRequest represents criteria for purging log files.
type LogCleanupRequest struct {
	OlderThanDays *int   `json:"older_than_days"`
	BatchID       string `json:"batch_id"`
}

// LogCleanupResponse represents the outcome of a cleanup operation.
type LogCleanupResponse struct {
	Status            string `json:"status"`
	DeletedFilesCount int    `json:"deleted_files_count"`
	DeletedBytes      int64  `json:"deleted_bytes"`
	Message           string `json:"message"`
}

// handleLogStats returns disk utilization and file metrics for the dedicated log directory.
func (s *Server) handleLogStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	logDir := s.config.LogBaseDir
	if logDir == "" {
		logDir = "/data/disk_log"
	}

	var totalBytes, usedBytes int64
	var stat syscall.Statfs_t
	if err := syscall.Statfs(logDir, &stat); err == nil {
		totalBytes = int64(stat.Blocks) * int64(stat.Bsize)
		free := int64(stat.Bavail) * int64(stat.Bsize)
		usedBytes = totalBytes - free
	}

	var logCount int
	var logSize int64
	if _, err := os.Stat(logDir); err == nil {
		_ = filepath.Walk(logDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				if strings.HasSuffix(info.Name(), ".log") || strings.HasSuffix(info.Name(), ".gz") {
					logCount++
					logSize += info.Size()
				}
			}
			return nil
		})
	}

	json.NewEncoder(w).Encode(LogStatsResponse{
		LogBaseDir:    logDir,
		TotalBytes:    totalBytes,
		UsedBytes:     usedBytes,
		LogFilesCount: logCount,
		LogFilesBytes: logSize,
	})
}

// handleLogCleanup securely purges log files matching the given criteria.
func (s *Server) handleLogCleanup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req LogCleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}

	logDir := s.config.LogBaseDir
	if logDir == "" {
		logDir = "/data/disk_log"
	}
	cleanLogBase := filepath.Clean(logDir)

	var deletedCount int
	var deletedBytes int64

	// Case 1: Purge by batch_id
	if req.BatchID != "" {
		// Strict path traversal prevention: ensure batch_id does not escape
		if strings.Contains(req.BatchID, "..") || strings.Contains(req.BatchID, "/") || strings.Contains(req.BatchID, "\\") {
			http.Error(w, `{"error":"invalid batch_id parameter"}`, http.StatusBadRequest)
			return
		}

		batchDir := filepath.Join(cleanLogBase, "builds", req.BatchID)
		if !strings.HasPrefix(filepath.Clean(batchDir), cleanLogBase) {
			http.Error(w, `{"error":"path traversal forbidden"}`, http.StatusForbidden)
			return
		}

		_ = filepath.Walk(batchDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				deletedCount++
				deletedBytes += info.Size()
			}
			return nil
		})
		_ = os.RemoveAll(batchDir)
	}

	// Case 2: Purge by age (older_than_days)
	if req.OlderThanDays != nil && *req.OlderThanDays >= 0 {
		cutoff := time.Now().AddDate(0, 0, -*req.OlderThanDays)

		_ = filepath.Walk(cleanLogBase, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				// Only clean log or compressed log archives
				if strings.HasSuffix(info.Name(), ".log") || strings.HasSuffix(info.Name(), ".gz") {
					if info.ModTime().Before(cutoff) {
						deletedCount++
						deletedBytes += info.Size()
						_ = os.Remove(path)
					}
				}
			}
			return nil
		})
	}

	json.NewEncoder(w).Encode(LogCleanupResponse{
		Status:            "ok",
		DeletedFilesCount: deletedCount,
		DeletedBytes:      deletedBytes,
		Message:           fmt.Sprintf("Successfully cleaned %d log files (%d bytes)", deletedCount, deletedBytes),
	})
}
