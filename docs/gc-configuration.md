# Garbage Collection Configuration

This document describes how to configure the garbage collection (GC) system for nydus blob cache.

## Overview

The GC system automatically removes unreferenced blobs from the cache directory, freeing disk space while ensuring that active blobs remain available for running daemons.

## Configuration

GC is configured in the `[cache_manager]` section of the snapshotter configuration file:

```toml
[cache_manager]
gc_period = "24h"                  # GC execution period (empty = disabled)
gc_grace_period = "10m"            # Grace period for recently accessed blobs
gc_max_daemon_workers = 10         # Parallel daemon workers
gc_max_instance_workers = 20       # Parallel instance workers (shared daemons)
gc_instance_timeout = "5s"         # Timeout per metrics query
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

### Configuration Parameters

#### `gc_period` (string, optional)

**Default**: `""` (disabled)

Defines how frequently GC runs. The GC system is **opt-in** by design - it remains disabled unless explicitly configured.

**Accepted formats**:
- `"24h"` - Run daily
- `"12h"` - Run twice daily
- `"2h"` - Run every 2 hours
- `"120m"` - Run every 120 minutes
- `""` - Disabled (default)

**Example**:
```toml
gc_period = "24h"  # Run GC once per day
```

**Recommendation**: Start with `"24h"` for production workloads.

#### `gc_grace_period` (string, optional)

**Default**: `"10m"`

Specifies the minimum age a blob must have before it can be deleted. Blobs modified within this grace period are skipped, even if unreferenced.

**Purpose**: Prevents deletion of blobs that might be in use by daemons starting up or images being pulled.

**Accepted formats**: Same as `gc_period`

**Example**:
```toml
gc_grace_period = "10m"  # Keep blobs accessed in last 10 minutes
```

**Recommendation**:
- Default `"10m"` is suitable for most workloads
- Increase to `"30m"` for environments with slow image pulls
- Decrease to `"5m"` for fast development environments

#### `gc_max_daemon_workers` (int, optional)

**Default**: `10`

Maximum number of daemons to query concurrently for cache metrics.

**Purpose**: Controls parallelism when collecting blob references from multiple daemons.

**Example**:
```toml
gc_max_daemon_workers = 20  # Query up to 20 daemons in parallel
```

**Recommendation**:
- Default `10` works well for most deployments
- Increase for clusters with >50 daemons
- Decrease for resource-constrained environments

#### `gc_max_instance_workers` (int, optional)

**Default**: `20`

Maximum number of RAFS instances to query concurrently within a shared daemon.

**Purpose**: Controls parallelism when collecting blob references from a shared daemon with many instances.

**Example**:
```toml
gc_max_instance_workers = 50  # Query up to 50 instances in parallel
```

**Recommendation**:
- Default `20` is sufficient for most shared daemon configurations
- Increase to `50` for shared daemons with >100 instances

#### `gc_instance_timeout` (string, optional)

**Default**: `"5s"`

Timeout for each metrics query to a daemon instance.

**Purpose**: Prevents GC from hanging if a daemon is unresponsive.

**Example**:
```toml
gc_instance_timeout = "10s"  # Wait up to 10 seconds per query
```

**Recommendation**:
- Default `"5s"` is suitable for local daemons
- Increase to `"10s"` for network-mounted storage

## Configuration Examples

### Minimal Configuration (Recommended)

Enable GC with default settings:

```toml
[cache_manager]
gc_period = "24h"
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

This enables daily GC with sensible defaults.

### Production Configuration

Balanced configuration for production environments:

```toml
[cache_manager]
gc_period = "12h"                  # Run twice daily
gc_grace_period = "15m"            # 15-minute safety margin
gc_max_daemon_workers = 15         # Handle more daemons
gc_max_instance_workers = 30       # Handle larger shared daemons
gc_instance_timeout = "10s"        # Longer timeout for reliability
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

### High-Frequency Configuration

For development or testing environments needing frequent cleanup:

```toml
[cache_manager]
gc_period = "2h"                   # Run every 2 hours
gc_grace_period = "5m"             # Shorter grace period
gc_max_daemon_workers = 10
gc_max_instance_workers = 20
gc_instance_timeout = "5s"
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

### Large Deployment Configuration

For clusters with many daemons and shared instances:

```toml
[cache_manager]
gc_period = "24h"
gc_grace_period = "20m"            # Longer grace for safety
gc_max_daemon_workers = 30         # Handle 30+ daemons
gc_max_instance_workers = 50       # Handle 100+ instances
gc_instance_timeout = "15s"        # Generous timeout
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

## Driver Compatibility

### Supported Drivers

- ✅ **fusedev**: Full GC support
- ✅ **blockdev**: Full GC support (if using cache)
- ✅ **proxy**: Full GC support (if using cache)
- ❌ **fscache**: Not supported - cache metrics unavailable

### Fscache Limitation

The fscache driver manages cache files via the Linux kernel, and the snapshotter cannot query cache metrics. GC will automatically disable itself with an error message if fscache is detected.

**Workaround**: None currently available. Cache cleanup must be managed manually or via kernel fscache mechanisms.

## Verification

After enabling GC, verify the configuration:

1. **Check logs at startup**:
```
GC scheduler initialized: period=24h, gracePeriod=10m, enabled=true
GC scheduler started for fusedev driver with period 24h
```

2. **Query GC status via HTTP API**:
```bash
curl http://localhost:7788/api/v1/gc/status
```

Expected response:
```json
{
  "enabled": true,
  "scheduled_period": "24h0m0s",
  "currently_running": false,
  "last_execution": {
    "execution_id": "gc-20250103-020000-abc123",
    "started_at": "2025-01-03T02:00:00Z",
    "completed_at": "2025-01-03T02:01:45Z",
    "duration_ms": 105000,
    "summary": {
      "blobs_scanned": 1234,
      "blobs_referenced": 890,
      "blobs_deleted": 344,
      "bytes_freed": 5368709120,
      "errors_count": 0
    }
  }
}
```

3. **Check Prometheus metrics**:
```
nydus_gc_last_run_timestamp_seconds
nydus_gc_blobs_deleted_total{mode="scheduled"}
nydus_gc_duration_seconds_bucket{mode="scheduled",status="success"}
```

## Troubleshooting

### GC not running

**Symptoms**: No GC execution logs, `last_execution` is null in status API

**Possible causes**:
1. `gc_period` not set or empty
2. No managers initialized (check fs_driver configuration)
3. All managers use fscache driver

**Resolution**:
- Verify `gc_period` is set in configuration
- Check snapshotter logs for "GC scheduler started" message
- Ensure fusedev or compatible driver is enabled

### GC runs but deletes nothing

**Symptoms**: `blobs_deleted` is always 0

**Possible causes**:
1. All blobs are actively referenced
2. Grace period is too long
3. Blob modification times are recent

**Resolution**:
- Verify blobs exist: `ls -lh /var/lib/containerd/.../cache/`
- Check blob ages: `find /var/lib/containerd/.../cache/ -mmin +15`
- Review grace period setting

### GC errors

**Symptoms**: `errors_count` > 0 in results

**Common errors**:
- "Permission denied" - Check cache directory permissions
- "No such file or directory" - Blob deleted between scan and deletion
- "Timeout" - Increase `gc_instance_timeout`

**Resolution**:
- Check snapshotter logs for detailed error messages
- Review file system permissions
- Adjust timeout settings if needed

### High GC duration

**Symptoms**: GC takes >5 minutes to complete

**Possible causes**:
1. Many daemons (>100)
2. Large shared daemon with many instances (>200)
3. Slow storage (network FS)

**Resolution**:
- Increase `gc_max_daemon_workers`
- Increase `gc_max_instance_workers`
- Consider running GC less frequently with longer grace period

## Best Practices

1. **Start Conservative**: Begin with `gc_period = "24h"` and adjust based on disk usage patterns

2. **Monitor Metrics**: Track `nydus_gc_duration_seconds` and `nydus_gc_blobs_deleted_total` to tune configuration

3. **Grace Period**: Don't set below 5 minutes to avoid race conditions during image pulls

4. **Manual Testing**: Use dry-run mode to test GC behavior:
   ```bash
   curl -X POST http://localhost:7788/api/v1/gc/run \
     -H "Content-Type: application/json" \
     -d '{"dry_run": true}'
   ```

5. **Staged Rollout**: Enable GC on a subset of nodes first, monitor for issues before cluster-wide deployment

6. **Backup Before Production**: Test GC behavior in staging environment with production-like workloads

## Performance Impact

GC is designed to have minimal performance impact:

- **CPU**: Low (<1% during execution, 0% between cycles)
- **Memory**: Low (<50MB for typical deployments)
- **Disk I/O**: Moderate during execution (scanning + deletion)
- **Network**: None (local metrics queries only)

**During GC execution**:
- Metrics collection: 100-500ms per daemon
- Cache scan: 1-5 seconds for typical cache (1000-10000 blobs)
- Blob deletion: <1ms per blob

**Total GC duration**: Typically 10-60 seconds for production workloads
