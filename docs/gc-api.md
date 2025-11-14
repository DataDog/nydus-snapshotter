# Garbage Collection HTTP API

This document describes the HTTP API for manually triggering and monitoring garbage collection.

## Overview

The GC HTTP API provides two endpoints:
- `POST /api/v1/gc/run` - Trigger a manual GC execution
- `GET /api/v1/gc/status` - Query GC status and last execution results

The API is automatically available when GC is enabled via configuration.

## Endpoints

### POST /api/v1/gc/run

Triggers a manual garbage collection cycle.

#### Request

**Method**: `POST`

**URL**: `http://<snapshotter-address>/api/v1/gc/run`

**Headers**:
- `Content-Type: application/json` (optional)

**Body** (optional):
```json
{
  "dry_run": false,
  "timeout": "5m"
}
```

**Parameters**:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `dry_run` | boolean | No | `false` | If true, simulates GC without deleting blobs |
| `timeout` | string | No | `"5m"` | Maximum execution time (format: "5m", "10s", "1h") |

#### Response

**Success (200 OK)**:
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
    "errors_count": 0
  },
  "errors": []
}
```

**Response Fields**:

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Execution status: "success" or "error" |
| `execution_id` | string | Unique identifier for this GC run |
| `started_at` | string | ISO 8601 timestamp of execution start |
| `completed_at` | string | ISO 8601 timestamp of execution completion |
| `duration_ms` | integer | Total execution time in milliseconds |
| `summary.blobs_scanned` | integer | Total number of blobs found in cache |
| `summary.blobs_referenced` | integer | Number of blobs currently in use |
| `summary.blobs_deleted` | integer | Number of blobs removed |
| `summary.bytes_freed` | integer | Total bytes freed |
| `summary.errors_count` | integer | Number of errors encountered |
| `errors` | array | List of error messages (if any) |

**Conflict (409 Conflict)**:

Returned when GC is already running.

```json
{
  "status": "error",
  "error": "GC already running",
  "current_execution_id": "gc-20250103-142000-def456"
}
```

**Error (500 Internal Server Error)**:

Returned when GC execution fails.

```json
{
  "status": "error",
  "error": "GC is not supported for fscache driver - cache metrics unavailable",
  "execution_id": "gc-20250103-142530-abc123",
  "summary": {
    "blobs_scanned": 1234,
    "blobs_referenced": 0,
    "blobs_deleted": 0,
    "bytes_freed": 0,
    "errors_count": 1
  },
  "errors": ["GC is not supported for fscache driver"]
}
```

#### Examples

**Basic manual GC**:
```bash
curl -X POST http://localhost:7788/api/v1/gc/run
```

**Dry-run mode** (simulate without deleting):
```bash
curl -X POST http://localhost:7788/api/v1/gc/run \
  -H "Content-Type: application/json" \
  -d '{"dry_run": true}'
```

**With custom timeout**:
```bash
curl -X POST http://localhost:7788/api/v1/gc/run \
  -H "Content-Type: application/json" \
  -d '{"timeout": "10m"}'
```

**With jq for formatted output**:
```bash
curl -s -X POST http://localhost:7788/api/v1/gc/run | jq .
```

---

### GET /api/v1/gc/status

Queries the current GC status and last execution results.

#### Request

**Method**: `GET`

**URL**: `http://<snapshotter-address>/api/v1/gc/status`

**Headers**: None required

#### Response

**Success (200 OK)**:
```json
{
  "enabled": true,
  "scheduled_period": "24h0m0s",
  "currently_running": false,
  "last_execution": {
    "execution_id": "gc-20250103-020000-xyz789",
    "started_at": "2025-01-03T02:00:00Z",
    "completed_at": "2025-01-03T02:01:45Z",
    "duration_ms": 105000,
    "summary": {
      "blobs_scanned": 1234,
      "blobs_referenced": 890,
      "blobs_deleted": 128,
      "bytes_freed": 2147483648,
      "errors_count": 0
    }
  }
}
```

**Response Fields**:

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | boolean | Whether GC scheduler is enabled |
| `scheduled_period` | string | Configured GC period (e.g., "24h0m0s") |
| `currently_running` | boolean | True if GC is currently executing |
| `last_execution` | object | Details of the last GC execution (null if never run) |
| `last_execution.execution_id` | string | Unique ID of last execution |
| `last_execution.started_at` | string | Start timestamp |
| `last_execution.completed_at` | string | Completion timestamp |
| `last_execution.duration_ms` | integer | Execution duration in milliseconds |
| `last_execution.summary` | object | Summary statistics |

**When GC has never run**:
```json
{
  "enabled": true,
  "scheduled_period": "24h0m0s",
  "currently_running": false
}
```

**When GC is currently running**:
```json
{
  "enabled": true,
  "scheduled_period": "24h0m0s",
  "currently_running": true,
  "last_execution": {
    "execution_id": "gc-20250103-020000-xyz789",
    ...
  }
}
```

#### Examples

**Query GC status**:
```bash
curl http://localhost:7788/api/v1/gc/status
```

**Check if GC is running**:
```bash
curl -s http://localhost:7788/api/v1/gc/status | jq .currently_running
```

**Get last execution summary**:
```bash
curl -s http://localhost:7788/api/v1/gc/status | jq .last_execution.summary
```

---

## Use Cases

### Manual Cleanup After Image Removal

After removing images, trigger GC to immediately free space:

```bash
# Remove images
ctr images rm example.com/app:v1.0

# Trigger immediate GC
curl -X POST http://localhost:7788/api/v1/gc/run

# Check results
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.summary
```

### Testing GC Configuration

Before enabling scheduled GC, test with dry-run:

```bash
# Dry-run to see what would be deleted
curl -X POST http://localhost:7788/api/v1/gc/run \
  -H "Content-Type: application/json" \
  -d '{"dry_run": true}' | jq .

# Review output
# If acceptable, run actual GC
curl -X POST http://localhost:7788/api/v1/gc/run | jq .
```

### Monitoring GC Health

Periodically check GC status from monitoring system:

```bash
#!/bin/bash
# Monitor GC execution

STATUS=$(curl -s http://localhost:7788/api/v1/gc/status)

# Check if enabled
ENABLED=$(echo $STATUS | jq -r .enabled)
if [ "$ENABLED" != "true" ]; then
    echo "WARNING: GC is disabled"
    exit 1
fi

# Check last execution
LAST_RUN=$(echo $STATUS | jq -r .last_execution.completed_at)
if [ "$LAST_RUN" == "null" ]; then
    echo "WARNING: GC has never run"
    exit 1
fi

# Check for errors
ERROR_COUNT=$(echo $STATUS | jq -r .last_execution.summary.errors_count)
if [ "$ERROR_COUNT" -gt "0" ]; then
    echo "WARNING: Last GC had $ERROR_COUNT errors"
    exit 1
fi

echo "OK: GC healthy, last run $LAST_RUN"
```

### Triggering GC from CI/CD

Trigger GC after deploying new images:

```bash
#!/bin/bash
# deploy-with-gc.sh

# Deploy new images
kubectl apply -f deployment.yaml

# Wait for rollout
kubectl rollout status deployment/myapp

# Trigger GC to clean old images
curl -X POST http://snapshotter.example.com/api/v1/gc/run
```

### Concurrent Request Handling

If multiple clients try to trigger GC simultaneously:

```bash
# Client 1
curl -X POST http://localhost:7788/api/v1/gc/run &

# Client 2 (immediately after)
curl -X POST http://localhost:7788/api/v1/gc/run
# Response: {"status":"error","error":"GC already running",...}
```

The second request receives a 409 Conflict with the execution ID of the running GC.

---

## Error Handling

### Common Errors

#### 1. GC Already Running

**HTTP Status**: 409 Conflict

**Response**:
```json
{
  "status": "error",
  "error": "GC already running",
  "current_execution_id": "gc-20250103-142530-abc123"
}
```

**Resolution**: Wait for current execution to complete, then retry. Check status endpoint to monitor progress.

#### 2. Fscache Driver Detected

**HTTP Status**: 500 Internal Server Error

**Response**:
```json
{
  "status": "error",
  "error": "GC is not supported for fscache driver - cache metrics unavailable"
}
```

**Resolution**: GC is not compatible with fscache. Use a different fs_driver or manage cache manually.

#### 3. Timeout Exceeded

**HTTP Status**: 500 Internal Server Error

**Response**:
```json
{
  "status": "error",
  "error": "context deadline exceeded",
  "execution_id": "gc-20250103-142530-abc123",
  "summary": {
    "blobs_scanned": 1234,
    "blobs_referenced": 890,
    "blobs_deleted": 50,
    "bytes_freed": 1000000000,
    "errors_count": 1
  }
}
```

**Resolution**: Increase timeout in request body or adjust `gc_instance_timeout` in configuration.

#### 4. Permission Denied

**HTTP Status**: 500 Internal Server Error

Errors array contains:
```json
{
  "errors": [
    "failed to delete blob abc123: permission denied"
  ]
}
```

**Resolution**: Check cache directory permissions and snapshotter process user.

---

## Security Considerations

### Access Control

The GC API has no built-in authentication. Secure access using:

1. **Unix Socket** (default): Implicit authentication via file permissions
   ```toml
   address = "/run/containerd/containerd-nydus-grpc.sock"
   ```

2. **Network Socket**: Use external authentication (e.g., API gateway, mTLS)
   ```toml
   address = "127.0.0.1:7788"  # Bind to localhost only
   ```

3. **Firewall Rules**: Restrict access to trusted clients only

### Rate Limiting

Manual GC triggers are rate-limited by concurrent execution prevention:
- Only one GC execution allowed at a time
- Subsequent requests receive 409 Conflict

To implement additional rate limiting, use a reverse proxy or API gateway.

### Audit Logging

All GC executions are logged:
```
level=info msg="Starting GC execution" execution_id=gc-20250103-142530-abc123 dry_run=false mode=manual
level=info msg="GC cycle completed" execution_id=gc-20250103-142530-abc123 blobs_deleted=344 bytes_freed=5368709120 errors=0
```

Monitor logs for unauthorized or suspicious GC triggers.

---

## Integration Examples

### Prometheus Alerting

Alert on GC failures:

```yaml
groups:
- name: nydus_gc
  rules:
  - alert: NydusGCFailed
    expr: rate(nydus_gc_executions_total{status="error"}[1h]) > 0
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Nydus GC execution failed"
      description: "GC has failed {{ $value }} times in the last hour"

  - alert: NydusGCHighErrors
    expr: rate(nydus_gc_errors_total[1h]) > 10
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "High GC error rate"
      description: "GC is encountering {{ $value }} errors per second"
```

### Kubernetes CronJob

Trigger GC periodically via Kubernetes:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nydus-gc
  namespace: kube-system
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: gc-trigger
            image: curlimages/curl:latest
            command:
            - sh
            - -c
            - |
              curl -f -X POST http://nydus-snapshotter:7788/api/v1/gc/run || exit 1
          restartPolicy: OnFailure
```

### Python Client

```python
import requests
import time

def trigger_gc(url="http://localhost:7788", dry_run=False, timeout="5m"):
    """Trigger manual GC and wait for completion."""
    response = requests.post(
        f"{url}/api/v1/gc/run",
        json={"dry_run": dry_run, "timeout": timeout}
    )

    if response.status_code == 409:
        print("GC already running, waiting...")
        while True:
            status = requests.get(f"{url}/api/v1/gc/status").json()
            if not status["currently_running"]:
                return status["last_execution"]
            time.sleep(5)

    response.raise_for_status()
    return response.json()

# Usage
result = trigger_gc(dry_run=True)
print(f"Deleted {result['summary']['blobs_deleted']} blobs")
print(f"Freed {result['summary']['bytes_freed']} bytes")
```

---

## Best Practices

1. **Use Dry-Run First**: Always test with `dry_run: true` before actual deletion
2. **Monitor Results**: Check `errors` array for issues
3. **Set Appropriate Timeouts**: Use longer timeouts for large caches
4. **Avoid Concurrent Triggers**: Check `currently_running` before triggering
5. **Log Execution IDs**: Track execution IDs for debugging
6. **Automate Cautiously**: Prefer scheduled GC over frequent manual triggers
7. **Handle 409 Gracefully**: Retry with exponential backoff if concurrent

---

## API Endpoint Discovery

The GC API is available at the same address as the snapshotter service:

```bash
# Check snapshotter configuration
grep "address" /etc/nydus/config.toml

# Common addresses:
# Unix socket: /run/containerd/containerd-nydus-grpc.sock
# Network: 127.0.0.1:7788 or localhost:7788
```

For Unix socket access via HTTP:
```bash
curl --unix-socket /run/containerd/containerd-nydus-grpc.sock \
     http://localhost/api/v1/gc/status
```
