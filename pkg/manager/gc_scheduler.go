/*
 * Copyright (c) 2025. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/containerd/log"
	"github.com/containerd/nydus-snapshotter/config"
	"github.com/containerd/nydus-snapshotter/pkg/cache"
	"github.com/containerd/nydus-snapshotter/pkg/daemon"
	daemontypes "github.com/containerd/nydus-snapshotter/pkg/daemon/types"
	"github.com/containerd/nydus-snapshotter/pkg/errdefs"
	"github.com/containerd/nydus-snapshotter/pkg/metrics/data"
)

// GCScheduler manages scheduled garbage collection of unreferenced blobs.
type GCScheduler struct {
	manager      *Manager
	cacheManager *cache.Manager
	period       time.Duration
	gracePeriod  time.Duration
	enabled      bool
	cancel       context.CancelFunc

	// Parallelism configuration
	maxDaemonWorkers int
	instanceTimeout  time.Duration

	// State management
	mu            sync.Mutex
	running       bool
	currentExecID string
	lastExecution *GCExecutionResult
}

// GCExecutionResult holds the result of a GC execution.
type GCExecutionResult struct {
	ExecutionID     string
	StartedAt       time.Time
	CompletedAt     time.Time
	DurationMs      int64
	BlobsScanned    uint64
	BlobsReferenced uint64
	BlobsDeleted    uint64
	BytesFreed      uint64
	ErrorsCount     uint64
	Errors          []string
}

// NewGCScheduler creates a new GC scheduler instance.
func NewGCScheduler(m *Manager, cm *cache.Manager, cfg *config.CacheManagerConfig) (*GCScheduler, error) {
	instanceTimeout, err := cfg.ParseGCInstanceTimeout()
	if err != nil {
		return nil, errors.Wrap(err, "parse GC instance timeout")
	}

	scheduler := &GCScheduler{
		manager:          m,
		cacheManager:     cm,
		enabled:          cfg.GCPeriod > 0,
		period:           cfg.GCPeriod,
		gracePeriod:      cfg.GCGracePeriod,
		maxDaemonWorkers: cfg.GetGCMaxDaemonWorkers(),
		instanceTimeout:  cfg.GCInstanceTimeout,
	}

	log.L.Infof("GC scheduler initialized: period=%v, gracePeriod=%v, enabled=%v", scheduler.period, scheduler.gracePeriod, scheduler.enabled)
	return scheduler, nil
}

// Start begins the scheduled GC loop in a background goroutine.
func (gc *GCScheduler) Start(ctx context.Context) {
	if !gc.enabled {
		log.L.Info("GC scheduler is disabled")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	gc.cancel = cancel

	ticker := time.NewTicker(gc.period)
	defer ticker.Stop()

	log.L.Infof("GC scheduler started with period %v", gc.period)

	for {
		select {
		case <-ticker.C:
			log.L.Info("Starting scheduled GC cycle")
			result, err := gc.runScheduled(ctx)
			if err != nil {
				log.L.WithError(err).Error("Scheduled GC cycle failed")
			} else {
				log.L.WithField("execution_id", result.ExecutionID).
					WithField("blobs_deleted", result.BlobsDeleted).
					WithField("bytes_freed", result.BytesFreed).
					Info("Scheduled GC cycle completed")
			}
		case <-ctx.Done():
			log.L.Info("GC scheduler stopped")
			return
		}
	}
}

// Stop terminates the scheduled GC loop.
func (g *GCScheduler) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
}

// RunManual executes a GC cycle synchronously. Called by scheduled loop or HTTP API.
func (g *GCScheduler) RunManual(ctx context.Context, dryRun bool) (*GCExecutionResult, error) {
	return g.runWithMode(ctx, dryRun, "manual")
}

// runScheduled executes a scheduled GC cycle.
func (g *GCScheduler) runScheduled(ctx context.Context) (*GCExecutionResult, error) {
	return g.runWithMode(ctx, false, "scheduled")
}

// runWithMode executes a GC cycle with the specified mode (manual or scheduled).
func (g *GCScheduler) runWithMode(ctx context.Context, dryRun bool, mode string) (*GCExecutionResult, error) {
	// Check for fscache driver early
	if g.manager.FsDriver == config.FsDriverFscache {
		return nil, errors.New("GC is not supported for fscache driver - cache metrics unavailable")
	}

	g.mu.Lock()
	if g.running {
		currentID := g.currentExecID
		g.mu.Unlock()
		return nil, errors.Wrapf(errdefs.ErrAlreadyExists, "GC already running with execution ID %s", currentID)
	}
	g.running = true
	g.currentExecID = generateExecutionID()
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.running = false
		g.currentExecID = ""
		g.mu.Unlock()
	}()

	result := &GCExecutionResult{
		ExecutionID: g.currentExecID,
		StartedAt:   time.Now(),
		Errors:      make([]string, 0),
	}

	log.L.WithField("execution_id", result.ExecutionID).
		WithField("dry_run", dryRun).
		WithField("mode", mode).
		Info("Starting GC execution")

	// Track execution start
	startTime := time.Now()

	// Execute GC with dry-run option
	err := g.performGC(ctx, dryRun, result)

	result.CompletedAt = time.Now()
	result.DurationMs = result.CompletedAt.Sub(result.StartedAt).Milliseconds()
	duration := result.CompletedAt.Sub(startTime).Seconds()

	// Record metrics (only for non-dry-run executions)
	if !dryRun {
		status := "success"
		if err != nil {
			status = "error"
		}

		// Record execution
		data.GCExecutions.WithLabelValues(mode, status).Inc()

		// Record duration
		data.GCDuration.WithLabelValues(mode, status).Observe(duration)

		// Record last run timestamp
		data.GCLastRunTimestamp.Set(float64(result.CompletedAt.Unix()))

		if err == nil {
			// Record successful execution metrics
			data.GCBlobsDeleted.WithLabelValues(mode).Add(float64(result.BlobsDeleted))
			data.GCBlobsDeletedBytes.WithLabelValues(mode).Add(float64(result.BytesFreed))
			data.GCBlobsScanned.WithLabelValues(mode).Set(float64(result.BlobsScanned))
			data.GCBlobsReferenced.WithLabelValues(mode).Set(float64(result.BlobsReferenced))
		}

		// Record errors
		if result.ErrorsCount > 0 {
			data.GCErrors.WithLabelValues(mode).Add(float64(result.ErrorsCount))
		}
	}

	if err != nil {
		return result, err
	}

	g.mu.Lock()
	g.lastExecution = result
	g.mu.Unlock()

	return result, nil
}

// performGC executes the core GC logic.
func (g *GCScheduler) performGC(ctx context.Context, dryRun bool, result *GCExecutionResult) error {
	// Step 1: Collect referenced blobs from all running daemons
	log.L.Debug("Collecting referenced blobs from daemons")
	referenced, err := g.collectReferencedBlobsParallel(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to collect referenced blobs")
	}
	result.BlobsReferenced = uint64(len(referenced))
	log.L.Infof("Found %d referenced blobs", len(referenced))

	// Step 2: Scan cache directory for all blobs
	log.L.Debug("Scanning cache directory for blobs")
	allBlobs, err := g.cacheManager.ListAllBlobs(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to scan cache directory")
	}
	result.BlobsScanned = uint64(len(allBlobs))
	log.L.Infof("Found %d total blobs in cache", len(allBlobs))

	// Step 3: Compute unreferenced blobs
	unreferenced := make([]string, 0)
	for _, blobID := range allBlobs {
		if !referenced[blobID] {
			unreferenced = append(unreferenced, blobID)
		}
	}
	log.L.Infof("Found %d unreferenced blobs", len(unreferenced))

	// Step 4: Delete unreferenced blobs (or simulate if dry-run)
	for _, blobID := range unreferenced {
		// Safety check: verify blob modification time
		safe, err := g.cacheManager.IsBlobSafeToDelete(blobID, g.gracePeriod)
		if err != nil {
			errMsg := fmt.Sprintf("failed to check if blob %s is safe to delete: %v", blobID, err)
			result.ErrorsCount++
			result.Errors = append(result.Errors, errMsg)
			log.L.Warn(errMsg)
			continue
		}
		if !safe {
			log.L.Debugf("Skipping blob %s: modified within grace period", blobID)
			continue
		}

		// Get size before deletion
		usage, err := g.cacheManager.CacheUsage(ctx, blobID)
		if err != nil {
			log.L.WithError(err).Warnf("Failed to get cache usage for blob %s", blobID)
		}

		if dryRun {
			log.L.Infof("[DRY RUN] Would delete blob %s (%d bytes)", blobID, usage.Size)
		} else {
			// Final safety check: re-verify no daemon is using this blob
			if g.isBlobReferencedNow(ctx, blobID) {
				log.L.Warnf("Blob %s is now referenced by a daemon, skipping deletion", blobID)
				continue
			}

			if err := g.cacheManager.RemoveBlobCache(blobID); err != nil {
				errMsg := fmt.Sprintf("failed to delete blob %s: %v", blobID, err)
				result.ErrorsCount++
				result.Errors = append(result.Errors, errMsg)
				log.L.Warn(errMsg)
				continue
			}
			log.L.Infof("Deleted blob %s (%d bytes)", blobID, usage.Size)
		}

		result.BlobsDeleted++
		result.BytesFreed += uint64(usage.Size)
	}

	log.L.WithField("blobs_deleted", result.BlobsDeleted).
		WithField("bytes_freed", result.BytesFreed).
		WithField("errors", result.ErrorsCount).
		Info("GC cycle completed")

	return nil
}

// collectReferencedBlobsParallel collects blob references from all daemons in parallel.
func (g *GCScheduler) collectReferencedBlobsParallel(ctx context.Context) (map[string]bool, error) {
	// Get all daemons
	daemons := g.manager.ListDaemons()
	log.L.Debugf("Collecting references from %d daemons", len(daemons))

	// Result aggregation
	var mu sync.Mutex
	referenced := make(map[string]bool)

	// Error collection
	var errs []error
	var errMu sync.Mutex

	// Worker pool for daemon-level parallelism
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, g.maxDaemonWorkers)

	for _, d := range daemons {
		if d.State() != daemontypes.DaemonStateRunning {
			log.L.Debugf("Skipping daemon %s with state %s", d.ID(), d.State())
			continue
		}

		wg.Add(1)
		go func(daemon *daemon.Daemon) {
			defer wg.Done()

			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release

			// Collect from this daemon
			blobs, err := g.collectFromDaemon(ctx, daemon)
			if err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
				log.L.WithError(err).Warnf("Failed to collect from daemon %s", daemon.ID())
				return
			}

			// Merge results
			mu.Lock()
			for blobID := range blobs {
				referenced[blobID] = true
			}
			mu.Unlock()

			log.L.Debugf("Daemon %s references %d blobs", daemon.ID(), len(blobs))
		}(d)
	}

	wg.Wait()

	if len(errs) > 0 {
		log.L.Warnf("GC metrics collection had %d errors", len(errs))
	}

	return referenced, nil
}

// collectFromDaemon collects blob references from a single daemon.
func (g *GCScheduler) collectFromDaemon(ctx context.Context, d *daemon.Daemon) (map[string]bool, error) {
	blobs := make(map[string]bool)

	// Get all RAFS instances managed by this daemon
	instances := d.RafsCache.List()
	log.L.Debugf("Daemon %s has %d instances", d.ID(), len(instances))

	for _, instance := range instances {
		// Determine snapshot ID based on daemon mode
		snapshotID := instance.SnapshotID
		if d.IsSharedDaemon() {
			// Shared daemon requires snapshot ID
		} else {
			// Dedicated daemon uses empty snapshot ID
			snapshotID = ""
		}

		metrics, err := d.GetCacheMetrics(snapshotID)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get metrics for instance %s", instance.SnapshotID)
		}

		// Add all underlying files (blob IDs) to the set
		for _, blobID := range metrics.UnderlyingFiles {
			blobs[blobID] = true
		}
	}

	return blobs, nil
}

// isBlobReferencedNow performs a final check to see if any daemon is currently using the blob.
// This is a pessimistic safety check before deletion.
func (g *GCScheduler) isBlobReferencedNow(ctx context.Context, blobID string) bool {
	daemons := g.manager.ListDaemons()

	for _, d := range daemons {
		if d.State() != daemontypes.DaemonStateRunning {
			continue
		}

		instances := d.RafsCache.List()
		for _, instance := range instances {
			snapshotID := instance.SnapshotID
			if !d.IsSharedDaemon() {
				snapshotID = ""
			}

			metrics, err := d.GetCacheMetrics(snapshotID)
			if err != nil {
				// On error, be conservative and assume blob might be in use
				log.L.WithError(err).Warnf("Failed to verify blob %s usage for daemon %s", blobID, d.ID())
				return true
			}

			for _, refBlobID := range metrics.UnderlyingFiles {
				if refBlobID == blobID {
					return true
				}
			}
		}
	}

	return false
}

// GetLastExecution returns the last GC execution result.
func (g *GCScheduler) GetLastExecution() *GCExecutionResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastExecution
}

// IsRunning returns true if a GC cycle is currently running.
func (g *GCScheduler) IsRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

// GetCurrentExecutionID returns the current execution ID if running.
func (g *GCScheduler) GetCurrentExecutionID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.currentExecID
}

// generateExecutionID generates a unique execution ID for tracking.
func generateExecutionID() string {
	timestamp := time.Now().Format("20060102-150405")
	randomBytes := make([]byte, 3)
	if _, err := rand.Read(randomBytes); err != nil {
		// Fallback to timestamp only if random fails
		return fmt.Sprintf("gc-%s", timestamp)
	}
	return fmt.Sprintf("gc-%s-%s", timestamp, hex.EncodeToString(randomBytes))
}
