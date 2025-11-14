# Scheduled Garbage Collection Implementation Plan

## Overview

Periodic scanner that collects blob references from all running daemons, identifies unreferenced blobs in cache directory, and removes them safely.

**Integration Point**: Extend `pkg/manager/Manager` with GC scheduler.

## Architecture

### Blob Storage

- **Location**: `/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache` (default)
- **Configuration**: `config.CacheManagerConfig.CacheDir`
- **File Patterns**:
  - `{blobID}` - unsuffixed blob data (legacy)
  - `{blobID}.blob.data` - suffixed blob data (v2.1+)
  - `{blobID}.chunk_map` - chunk bitmap
  - `{blobID}.blob.meta` - metadata
  - `{blobID}.image.disk` - image disk files
  - `{blobID}.layer.disk` - layer disk files

### Cache Metrics

**Daemon HTTP API**: `/api/v1/metrics/blobcache`
- **Method**: `daemon.GetCacheMetrics(snapshotID string) (*types.CacheMetrics, error)`
- **Key Field**: `CacheMetrics.UnderlyingFiles []string` - blob IDs referenced by daemon

### Daemon Management

- **Manager**: `pkg/manager/Manager` - central daemon orchestration
- **DaemonCache**: In-memory daemon index
- **Methods**: `ListDaemons()`, `GetByDaemonID(id)`, `DestroyDaemon(d)`

## Design Decisions (Confirmed)

1. **Default GC period**: Disabled (empty string) - opt-in behavior
2. **Grace period**: 10 minutes
3. **Parallelism**: `max_daemon_workers=10`, `max_instance_workers=20`
4. **Manual API**: Enabled automatically when `gc_period` is set
5. **Fscache**: GC disabled for fscache driver with error message

## Component Design

### 1. GC Scheduler (`pkg/manager/gc_scheduler.go` - NEW)

```go
type GCScheduler struct {
    manager           *Manager
    cacheManager      *cache.Manager
    period            time.Duration
    gracePeriod       time.Duration
    enabled           bool
    cancel            context.CancelFunc

    // Configuration
    maxDaemonWorkers   int
    maxInstanceWorkers int
    instanceTimeout    time.Duration

    // State management
    mu              sync.Mutex
    running         bool
    currentExecID   string
    lastExecution   *GCExecutionResult
}

type GCExecutionResult struct {
    ExecutionID      string
    StartedAt        time.Time
    CompletedAt      time.Time
    DurationMs       int64
    BlobsScanned     uint64
    BlobsReferenced  uint64
    BlobsDeleted     uint64
    BytesFreed       uint64
    ErrorsCount      uint64
    Errors           []string
}
```

**Key Methods**:
- `Start(ctx)` - Launch background goroutine with ticker
- `Stop()` - Cancel background goroutine
- `RunManual(ctx, dryRun)` - Execute single GC cycle (for API)
- `performGC(ctx, dryRun, result)` - Core GC logic
- `collectReferencedBlobsParallel(ctx)` - Query all daemons
- `collectFromDaemon(ctx, daemon)` - Query single daemon

### 2. Cache Manager Extensions (`pkg/cache/manager.go` - MODIFY)

**Add Methods**:
```go
// ListAllBlobs returns all blob IDs in cache directory
func (cm *Manager) ListAllBlobs(ctx context.Context) ([]string, error)

// IsBlobSafeToDelete checks if blob can be deleted (mtime, locks)
func (cm *Manager) IsBlobSafeToDelete(blobID string, gracePeriod time.Duration) (bool, error)
```

**Existing**: `RemoveBlobCache(blobID string) error` - handles file pattern matching

### 3. Manager Integration (`pkg/manager/manager.go` - MODIFY)

**Add Fields**:
```go
type Manager struct {
    // ... existing fields
    gcScheduler  *GCScheduler
}
```

**Modify `NewManager()`**:
```go
func NewManager(...) (*Manager, error) {
    // ... existing initialization

    gcPeriod, _ := cfg.CacheManager.ParseGCPeriod()
    if gcPeriod > 0 {
        m.gcScheduler = NewGCScheduler(m, cacheManager, cfg.CacheManager)
    }

    return m, nil
}
```

**Add Methods**:
```go
func (m *Manager) StartGC(ctx context.Context)
func (m *Manager) RegisterGCEndpoint(mux *http.ServeMux)
```

### 4. Configuration (`config/config.go` - MODIFY)

```go
type CacheManagerConfig struct {
    Disable              bool
    GCPeriod             string  // "24h", "2h" - empty = disabled
    GCGracePeriod        string  // "10m" - default
    GCMaxDaemonWorkers   int     `toml:"gc_max_daemon_workers"`   // Default: 10
    GCMaxInstanceWorkers int     `toml:"gc_max_instance_workers"` // Default: 20
    GCInstanceTimeout    string  `toml:"gc_instance_timeout"`     // Default: "5s"
    CacheDir             string
}

func (c *CacheManagerConfig) ParseGCPeriod() (time.Duration, error)
func (c *CacheManagerConfig) ParseGCGracePeriod() (time.Duration, error)
func (c *CacheManagerConfig) ParseGCInstanceTimeout() (time.Duration, error)
```

**Example TOML**:
```toml
[cache_manager]
gc_period = "24h"                  # Disabled if empty
gc_grace_period = "10m"            # Optional, defaults to 10m
gc_max_daemon_workers = 10         # Optional, defaults to 10
gc_max_instance_workers = 20       # Optional, defaults to 20
gc_instance_timeout = "5s"         # Optional, defaults to 5s
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

### 5. HTTP API (`pkg/manager/gc_http.go` - NEW)

**Endpoint**: `POST /api/v1/gc/run`

**Request**:
```json
{
  "dry_run": false,
  "timeout": "5m"
}
```

**Response**:
```json
{
  "status": "success",
  "execution_id": "gc-20250103-142530-abc123",
  "started_at": "2025-01-03T14:25:30Z",
  "completed_at": "2025-01-03T14:27:15Z",
  "duration_ms": 105000,
  "summary": {
    "blobs_scanned": 1234,
    "blobs_referenced": 890,
    "blobs_deleted": 344,
    "bytes_freed": 5368709120,
    "errors_count": 2
  },
  "errors": ["..."]
}
```

**Error Response** (409 Conflict):
```json
{
  "status": "error",
  "error": "GC already running",
  "current_execution_id": "gc-20250103-142530-abc123"
}
```

### 6. Metrics (`pkg/metrics/data/gc.go` - NEW)

**Prometheus Metrics**:
- `nydus_gc_last_run_timestamp_seconds` (gauge)
- `nydus_gc_blobs_deleted_total` (counter)
- `nydus_gc_blobs_deleted_bytes_total` (counter)
- `nydus_gc_errors_total` (counter)
- `nydus_gc_duration_seconds` (histogram)

## Algorithm: GC Cycle Execution

```
1. Acquire Manager.mu (prevent daemon destruction)
2. Collect referenced blobs (parallel):
   - For each daemon (10 concurrent workers):
     * Skip if state != RUNNING
     * Get daemon.RafsCache.List() for instances
     * For each instance:
       - Query daemon.GetCacheMetrics(snapshotID)
       - Add metrics.UnderlyingFiles to referenced set
     * Handle errors (log, continue)
3. Release Manager.mu

4. Scan cache directory:
   - List all files matching blob patterns
   - Extract blob IDs (strip suffixes)

5. Compute unreferenced = scanned - referenced

6. For each unreferenced blob:
   - Check mtime (skip if < gc_grace_period)
   - Re-acquire Manager.mu briefly
   - Re-verify no daemon using blob
   - Release Manager.mu
   - Call cacheManager.RemoveBlobCache(blobID)
   - Update metrics
   - Log deletion

7. Update Prometheus metrics, log summary
```

## Safety Mechanisms

### 1. Grace Period
- Skip blobs with `mtime > now - gc_grace_period`
- Default: 10 minutes
- **Rationale**: New daemon might be starting, blob just pulled

### 2. Fscache Guard
```go
if m.FsDriver == config.FsDriverFscache {
    log.Warn("GC disabled for fscache driver - metrics unavailable")
    return nil
}
```

### 3. Daemon State Validation
```go
if d.State() != types.DaemonStateRunning {
    continue // Skip dead/dying daemons
}

// Before deletion: re-check
m.mu.Lock()
currentDaemons := m.ListDaemons()
// Verify blob not in use
m.mu.Unlock()
```

### 4. Error Isolation
- Never fail entire GC cycle on single blob error
- Log errors, update error metrics, continue

### 5. Concurrency Control
- Mutex prevents concurrent GC executions
- Manual trigger returns HTTP 409 if already running

## Parallelization Strategy

### Daemon-Level Parallelism (Phase 1)

```go
func (g *GCScheduler) collectReferencedBlobsParallel(ctx context.Context) (map[string]bool, error) {
    daemons := g.manager.ListDaemons()

    var mu sync.Mutex
    referenced := make(map[string]bool)

    var wg sync.WaitGroup
    semaphore := make(chan struct{}, g.maxDaemonWorkers) // Default: 10

    for _, d := range daemons {
        if d.State() != types.DaemonStateRunning {
            continue
        }

        wg.Add(1)
        go func(daemon *daemon.Daemon) {
            defer wg.Done()
            semaphore <- struct{}{}
            defer func() { <-semaphore }()

            blobs, err := g.collectFromDaemon(ctx, daemon)
            if err != nil {
                log.Warnf("Failed to collect from daemon %s: %v", daemon.ID(), err)
                return
            }

            mu.Lock()
            for blobID := range blobs {
                referenced[blobID] = true
            }
            mu.Unlock()
        }(d)
    }

    wg.Wait()
    return referenced, nil
}
```

**Performance**: 10 daemons in parallel = ~10x faster (200ms vs 2s)

### Instance-Level Parallelism (Future Enhancement)

For shared daemons with >100 instances, add parallel metrics queries within daemon:

```go
func (g *GCScheduler) collectFromDaemonParallel(ctx context.Context, d *daemon.Daemon) (map[string]bool, error) {
    instances := d.RafsCache.List()

    // If small daemon, use sequential
    if len(instances) <= 10 {
        return g.collectFromDaemonSequential(ctx, d)
    }

    // Parallel for large daemon
    var mu sync.Mutex
    blobs := make(map[string]bool)

    var wg sync.WaitGroup
    semaphore := make(chan struct{}, g.maxInstanceWorkers) // Default: 20

    for _, instance := range instances {
        wg.Add(1)
        go func(inst rafs.Instance) {
            defer wg.Done()
            semaphore <- struct{}{}
            defer func() { <-semaphore }()

            queryCtx, cancel := context.WithTimeout(ctx, g.instanceTimeout)
            defer cancel()

            metrics, err := d.GetCacheMetrics(inst.SnapshotID)
            if err != nil {
                return
            }

            mu.Lock()
            for _, blobID := range metrics.UnderlyingFiles {
                blobs[blobID] = true
            }
            mu.Unlock()
        }(instance)
    }

    wg.Wait()
    return blobs, nil
}
```

## Edge Cases

| Scenario | Risk | Mitigation |
|----------|------|------------|
| Daemon dies during metrics query | Timeout/error | Catch error, log, continue |
| Blob deleted while daemon reads | I/O errors | Grace period, mtime check |
| Shared daemon with 100+ instances | GC timeout | Parallel queries, configurable timeout |
| Blob file locked | Delete fails | Check error, skip, retry next cycle |
| Cache on slow storage (NFS) | Long GC cycles | Configurable period, duration metric |
| Manual blob in cache dir | Deleted if not tracked | Expected - cache is managed |

## Implementation Phases

### Phase 1: Core GC Logic
- [ ] `pkg/manager/gc_scheduler.go` - scheduler and core logic
- [ ] `pkg/cache/manager.go` - ListAllBlobs, IsBlobSafeToDelete
- [ ] Configuration parsing in `config/config.go`
- [ ] Unit tests
- [ ] Fscache guard

### Phase 2: Manager Integration
- [ ] Modify `pkg/manager/manager.go` (NewManager, StartGC)
- [ ] Wire up in `cmd/containerd-nydus-grpc/main.go`
- [ ] Integration tests

### Phase 3: HTTP API
- [ ] `pkg/manager/gc_http.go` - HTTP handler
- [ ] Register endpoint in Manager
- [ ] API tests

### Phase 4: Observability
- [ ] `pkg/metrics/data/gc.go` - Prometheus metrics
- [ ] Integrate metrics collection
- [ ] Logging improvements

### Phase 5: Documentation
- [ ] Configuration documentation
- [ ] API documentation
- [ ] Operational runbook

## Testing Strategy

### Unit Tests
1. `gc_scheduler_test.go`:
   - CollectReferencedBlobs with mock daemons
   - ScanCacheDirectory with mock cache
   - SafeDelete with various scenarios
   - Error handling (dead daemon, timeout)

2. `cache_manager_test.go`:
   - ListAllBlobs with various suffixes
   - IsBlobSafeToDelete (mtime, locks)

3. `gc_http_test.go`:
   - Concurrent request handling
   - Dry-run mode
   - Timeout handling

### Integration Tests
1. Create test cache with known blobs
2. Start mock daemons reporting references
3. Run GC cycle
4. Verify correct blobs deleted

### Smoke Test
1. Deploy on test cluster
2. Pull multiple images
3. Delete snapshots
4. Trigger manual GC
5. Verify disk space reclaimed

## Risk Assessment

**Low Risk**:
- File I/O operations
- Metrics collection
- Configuration parsing

**Medium Risk**:
- Race conditions (daemon starts during GC)
- **Mitigation**: Grace period + state re-check

**High Risk**:
- Deleting blob in use by new daemon
- **Mitigation**: Multiple safety layers
- **Failure mode**: Explicit mount error, blob can be re-pulled

## Open Items

1. ~~Default GC period~~ - **Confirmed: Disabled (opt-in)**
2. ~~Grace period~~ - **Confirmed: 10 minutes**
3. ~~Parallelism defaults~~ - **Confirmed: 10 daemons, 20 instances**
4. ~~Manual API~~ - **Confirmed: Auto-enabled with gc_period**
5. ~~Fscache support~~ - **Confirmed: Disabled with error message**

## File Locations

**New Files**:
- `pkg/manager/gc_scheduler.go` - Core GC scheduler
- `pkg/manager/gc_http.go` - HTTP API handler
- `pkg/metrics/data/gc.go` - Prometheus metrics
- `pkg/manager/gc_scheduler_test.go` - Unit tests
- `pkg/manager/gc_http_test.go` - API tests

**Modified Files**:
- `pkg/cache/manager.go` - Add ListAllBlobs, IsBlobSafeToDelete
- `pkg/manager/manager.go` - Add gcScheduler field, StartGC method
- `config/config.go` - Enhance CacheManagerConfig
- `cmd/containerd-nydus-grpc/main.go` - Wire up GC lifecycle

## References

**Existing Patterns**:
- Periodic tasks: `pkg/metrics/serve.go:StartCollectMetrics()`
- Daemon management: `pkg/manager/manager.go:ListDaemons()`
- Cache operations: `pkg/cache/manager.go:RemoveBlobCache()`
- HTTP APIs: Daemon's `/api/v1/metrics/blobcache`
