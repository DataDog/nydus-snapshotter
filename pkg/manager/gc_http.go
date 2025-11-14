/*
 * Copyright (c) 2025. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/containerd/log"
	"github.com/containerd/nydus-snapshotter/pkg/errdefs"
)

// GCHandler handles HTTP requests for manual GC operations.
type GCHandler struct {
	scheduler *GCScheduler
}

// NewGCHandler creates a new HTTP handler for GC operations.
func NewGCHandler(scheduler *GCScheduler) *GCHandler {
	return &GCHandler{scheduler: scheduler}
}

// GCRequest represents the request body for manual GC trigger.
type GCRequest struct {
	DryRun  bool   `json:"dry_run"`
	Timeout string `json:"timeout"` // e.g., "5m"
}

// GCResponse represents the response for a GC operation.
type GCResponse struct {
	Status        string      `json:"status"`
	ExecutionID   string      `json:"execution_id,omitempty"`
	StartedAt     string      `json:"started_at,omitempty"`
	CompletedAt   string      `json:"completed_at,omitempty"`
	DurationMs    int64       `json:"duration_ms,omitempty"`
	Summary       *GCSummary  `json:"summary,omitempty"`
	Errors        []string    `json:"errors,omitempty"`
	Error         string      `json:"error,omitempty"`
	CurrentExecID string      `json:"current_execution_id,omitempty"`
	StartedAtTime string      `json:"started_at_time,omitempty"`
}

// GCSummary contains summary statistics for a GC execution.
type GCSummary struct {
	BlobsScanned    uint64 `json:"blobs_scanned"`
	BlobsReferenced uint64 `json:"blobs_referenced"`
	BlobsDeleted    uint64 `json:"blobs_deleted"`
	BytesFreed      uint64 `json:"bytes_freed"`
	ErrorsCount     uint64 `json:"errors_count"`
}

// ServeHTTP handles the HTTP request for manual GC trigger.
func (h *GCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request
	var req GCRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Use defaults if body is empty or invalid
			req.DryRun = false
			req.Timeout = "5m"
		}
	} else {
		req.DryRun = false
		req.Timeout = "5m"
	}

	// Parse timeout
	timeout, err := time.ParseDuration(req.Timeout)
	if err != nil {
		timeout = 5 * time.Minute
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// Execute GC
	result, err := h.scheduler.RunManual(ctx, req.DryRun)

	// Handle concurrent execution error
	if err != nil && errdefs.IsAlreadyExists(err) {
		resp := GCResponse{
			Status:        "error",
			Error:         "GC already running",
			CurrentExecID: h.scheduler.GetCurrentExecutionID(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Handle other errors
	if err != nil {
		log.L.WithError(err).Error("GC execution failed")
		resp := GCResponse{
			Status: "error",
			Error:  err.Error(),
		}
		if result != nil {
			resp.ExecutionID = result.ExecutionID
			resp.Summary = &GCSummary{
				BlobsScanned:    result.BlobsScanned,
				BlobsReferenced: result.BlobsReferenced,
				BlobsDeleted:    result.BlobsDeleted,
				BytesFreed:      result.BytesFreed,
				ErrorsCount:     result.ErrorsCount,
			}
			resp.Errors = result.Errors
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Success response
	resp := GCResponse{
		Status:      "success",
		ExecutionID: result.ExecutionID,
		StartedAt:   result.StartedAt.Format(time.RFC3339),
		CompletedAt: result.CompletedAt.Format(time.RFC3339),
		DurationMs:  result.DurationMs,
		Summary: &GCSummary{
			BlobsScanned:    result.BlobsScanned,
			BlobsReferenced: result.BlobsReferenced,
			BlobsDeleted:    result.BlobsDeleted,
			BytesFreed:      result.BytesFreed,
			ErrorsCount:     result.ErrorsCount,
		},
		Errors: result.Errors,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ServeStatus handles the HTTP request for GC status query.
func (h *GCHandler) ServeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]interface{}{
		"enabled":           h.scheduler.enabled,
		"scheduled_period":  h.scheduler.period.String(),
		"currently_running": h.scheduler.IsRunning(),
	}

	lastExec := h.scheduler.GetLastExecution()
	if lastExec != nil {
		status["last_execution"] = map[string]interface{}{
			"execution_id": lastExec.ExecutionID,
			"started_at":   lastExec.StartedAt.Format(time.RFC3339),
			"completed_at": lastExec.CompletedAt.Format(time.RFC3339),
			"duration_ms":  lastExec.DurationMs,
			"summary": map[string]interface{}{
				"blobs_scanned":    lastExec.BlobsScanned,
				"blobs_referenced": lastExec.BlobsReferenced,
				"blobs_deleted":    lastExec.BlobsDeleted,
				"bytes_freed":      lastExec.BytesFreed,
				"errors_count":     lastExec.ErrorsCount,
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// RegisterGCEndpoints registers GC HTTP endpoints to the provided mux.
func RegisterGCEndpoints(mux *http.ServeMux, scheduler *GCScheduler) {
	if scheduler == nil {
		log.L.Debug("GC scheduler not initialized, skipping endpoint registration")
		return
	}

	handler := NewGCHandler(scheduler)
	mux.Handle("/api/v1/gc/run", handler)
	mux.HandleFunc("/api/v1/gc/status", handler.ServeStatus)

	log.L.Info("Registered GC API endpoints: POST /api/v1/gc/run, GET /api/v1/gc/status")
}
