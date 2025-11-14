# Garbage Collection Operations Runbook

This runbook provides step-by-step procedures for operating and troubleshooting the nydus GC system.

## Table of Contents

1. [Deployment](#deployment)
2. [Monitoring](#monitoring)
3. [Troubleshooting](#troubleshooting)
4. [Maintenance](#maintenance)
5. [Emergency Procedures](#emergency-procedures)

---

## Deployment

### Initial Deployment

#### Prerequisites

- Nydus-snapshotter version with GC support
- Access to snapshotter configuration file
- Monitoring system (Prometheus recommended)
- Sufficient disk space for metrics retention

#### Deployment Steps

**1. Update Configuration**

Add GC configuration to `/etc/nydus/config.toml`:

```toml
[cache_manager]
gc_period = "24h"
gc_grace_period = "10m"
cache_dir = "/var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache"
```

**2. Validate Configuration**

```bash
# Test configuration syntax
containerd-nydus-grpc --config /etc/nydus/config.toml --help

# Check for syntax errors in logs
journalctl -u nydus-snapshotter -n 50
```

**3. Restart Snapshotter**

```bash
# Systemd
sudo systemctl restart nydus-snapshotter
sudo systemctl status nydus-snapshotter

# Check GC initialized
sudo journalctl -u nydus-snapshotter | grep "GC scheduler"
# Expected: "GC scheduler initialized: period=24h, gracePeriod=10m, enabled=true"
```

**4. Verify GC is Running**

```bash
# Wait a few minutes, then check status
curl http://localhost:7788/api/v1/gc/status | jq .

# Expected output:
# {
#   "enabled": true,
#   "scheduled_period": "24h0m0s",
#   "currently_running": false
# }
```

**5. Perform Test Run**

```bash
# Dry-run to verify behavior
curl -X POST http://localhost:7788/api/v1/gc/run \
  -H "Content-Type: application/json" \
  -d '{"dry_run": true}' | jq .

# Review output
# Check blobs_deleted count
# If acceptable, proceed with actual GC

curl -X POST http://localhost:7788/api/v1/gc/run | jq .
```

**6. Enable Monitoring**

Add Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: 'nydus-snapshotter'
    static_configs:
      - targets: ['localhost:7788']
```

**7. Set Up Alerting**

Deploy alert rules (see [Monitoring](#monitoring) section).

#### Staged Rollout

For production clusters, deploy incrementally:

1. **Canary (1-2 nodes)**: Enable GC, monitor for 7 days
2. **Pilot (10% of nodes)**: Expand if canary succeeds, monitor for 7 days
3. **Production (all nodes)**: Full rollout

**Rollback Procedure**:
```bash
# Remove gc_period from config
sed -i '/gc_period/d' /etc/nydus/config.toml

# Restart snapshotter
sudo systemctl restart nydus-snapshotter

# Verify GC disabled
curl http://localhost:7788/api/v1/gc/status | jq .enabled
# Expected: false or null
```

---

## Monitoring

### Key Metrics

#### Execution Metrics

| Metric | Type | Description | Healthy Range |
|--------|------|-------------|---------------|
| `nydus_gc_executions_total{mode="scheduled",status="success"}` | Counter | Successful scheduled executions | Increasing steadily |
| `nydus_gc_executions_total{mode="scheduled",status="error"}` | Counter | Failed executions | 0 or very low |
| `nydus_gc_duration_seconds` | Histogram | Execution duration | <60s typical |
| `nydus_gc_last_run_timestamp_seconds` | Gauge | Last execution timestamp | Within period |

#### Cleanup Metrics

| Metric | Type | Description | Healthy Range |
|--------|------|-------------|---------------|
| `nydus_gc_blobs_deleted_total` | Counter | Total blobs deleted | Depends on churn |
| `nydus_gc_blobs_deleted_bytes_total` | Counter | Total bytes freed | Depends on churn |
| `nydus_gc_errors_total` | Counter | Total errors | 0 or very low |
| `nydus_gc_blobs_scanned` | Gauge | Blobs scanned in last run | - |
| `nydus_gc_blobs_referenced` | Gauge | Referenced blobs in last run | - |

### Dashboards

#### Grafana Dashboard (Example)

```json
{
  "panels": [
    {
      "title": "GC Execution Rate",
      "targets": [
        {
          "expr": "rate(nydus_gc_executions_total[1h])"
        }
      ]
    },
    {
      "title": "GC Duration",
      "targets": [
        {
          "expr": "histogram_quantile(0.95, rate(nydus_gc_duration_seconds_bucket[1h]))"
        }
      ]
    },
    {
      "title": "Blobs Deleted Rate",
      "targets": [
        {
          "expr": "rate(nydus_gc_blobs_deleted_total[1h])"
        }
      ]
    },
    {
      "title": "Disk Space Freed",
      "targets": [
        {
          "expr": "rate(nydus_gc_blobs_deleted_bytes_total[1h])"
        }
      ]
    },
    {
      "title": "GC Error Rate",
      "targets": [
        {
          "expr": "rate(nydus_gc_errors_total[1h])"
        }
      ]
    }
  ]
}
```

### Alert Rules

```yaml
groups:
- name: nydus_gc_alerts
  rules:
  # GC not running
  - alert: NydusGCNotRunning
    expr: (time() - nydus_gc_last_run_timestamp_seconds) > 86400 * 1.5
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "Nydus GC has not run recently"
      description: "Last GC run was {{ $value | humanizeDuration }} ago (expected: daily)"

  # GC failures
  - alert: NydusGCFailureRate
    expr: rate(nydus_gc_executions_total{status="error"}[6h]) > 0.1
    for: 30m
    labels:
      severity: critical
    annotations:
      summary: "High GC failure rate"
      description: "{{ $value | humanizePercentage }} of GC executions failing"

  # GC duration
  - alert: NydusGCSlowExecution
    expr: histogram_quantile(0.95, rate(nydus_gc_duration_seconds_bucket[1h])) > 300
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "GC execution is slow"
      description: "95th percentile GC duration is {{ $value }}s (threshold: 300s)"

  # GC errors
  - alert: NydusGCHighErrors
    expr: rate(nydus_gc_errors_total[1h]) > 0.01
    for: 30m
    labels:
      severity: warning
    annotations:
      summary: "High GC error rate"
      description: "GC is encountering {{ $value }} errors per second"

  # Cache growth
  - alert: NydusGCIneffective
    expr: rate(nydus_gc_blobs_deleted_total[24h]) == 0 AND nydus_gc_blobs_scanned > 1000
    for: 2h
    labels:
      severity: info
    annotations:
      summary: "GC is not deleting blobs"
      description: "No blobs deleted in 24h despite {{ $value }} blobs in cache"
```

### Log Monitoring

**Key Log Patterns**:

```bash
# Successful execution
grep "GC cycle completed" /var/log/nydus/snapshotter.log

# Errors
grep "GC.*error\|GC.*failed" /var/log/nydus/snapshotter.log

# Specific execution
grep "execution_id=gc-20250103-142530-abc123" /var/log/nydus/snapshotter.log
```

**Log Aggregation Query (Elasticsearch/Kibana)**:

```
service:nydus-snapshotter AND message:"GC cycle completed"
| stats count, avg(blobs_deleted), avg(bytes_freed) by mode
```

---

## Troubleshooting

### Problem: GC Not Running

**Symptoms**:
- No "GC cycle completed" logs
- `nydus_gc_last_run_timestamp_seconds` not updating
- Status API shows `last_execution: null`

**Diagnosis**:

```bash
# 1. Check if GC is enabled
curl http://localhost:7788/api/v1/gc/status | jq .enabled

# 2. Check configuration
grep gc_period /etc/nydus/config.toml

# 3. Check snapshotter logs
journalctl -u nydus-snapshotter | grep "GC scheduler"

# 4. Check fs_driver
grep fs_driver /etc/nydus/config.toml
```

**Root Causes & Solutions**:

| Cause | Solution |
|-------|----------|
| `gc_period` not set | Add `gc_period = "24h"` to config, restart |
| Fscache driver detected | Change to fusedev driver or disable GC |
| No managers initialized | Verify daemon configuration |
| Configuration syntax error | Validate TOML syntax |

**Resolution Steps**:

```bash
# 1. Fix configuration
sudo vi /etc/nydus/config.toml
# Add: gc_period = "24h"

# 2. Validate config
containerd-nydus-grpc --config /etc/nydus/config.toml --help

# 3. Restart
sudo systemctl restart nydus-snapshotter

# 4. Verify
curl http://localhost:7788/api/v1/gc/status | jq .
```

---

### Problem: GC Deletes Nothing

**Symptoms**:
- `blobs_deleted` is always 0
- Cache directory continues growing
- `nydus_gc_blobs_deleted_total` not increasing

**Diagnosis**:

```bash
# 1. Check GC results
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.summary

# 2. Check blob ages
find /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache \
  -type f -mmin +15 | wc -l

# 3. Check grace period
grep gc_grace_period /etc/nydus/config.toml

# 4. Check daemon references
curl http://localhost:7788/api/v1/gc/status | jq '.last_execution.summary.blobs_referenced'
```

**Root Causes & Solutions**:

| Cause | Solution |
|-------|----------|
| All blobs are referenced | Normal - no action needed |
| Grace period too long | Reduce `gc_grace_period` to 5-10m |
| Recent image pulls | Wait for grace period to expire |
| Blobs not in cache dir | Verify `cache_dir` configuration |

**Resolution Steps**:

```bash
# 1. Run manual GC with dry-run
curl -X POST http://localhost:7788/api/v1/gc/run \
  -d '{"dry_run": true}' | jq .

# 2. If blobs_deleted > 0 in dry-run, adjust grace period
sudo vi /etc/nydus/config.toml
# Change: gc_grace_period = "5m"

# 3. Restart and test
sudo systemctl restart nydus-snapshotter
curl -X POST http://localhost:7788/api/v1/gc/run | jq .
```

---

### Problem: High GC Duration

**Symptoms**:
- GC takes >5 minutes
- `nydus_gc_duration_seconds` p95 >300s
- "GC execution is slow" alert firing

**Diagnosis**:

```bash
# 1. Check execution duration
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.duration_ms

# 2. Check blob count
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.summary.blobs_scanned

# 3. Check daemon count
# (requires access to manager internals or logs)
journalctl -u nydus-snapshotter | grep "Collecting references from .* daemons"

# 4. Check storage performance
time ls -l /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache | wc -l
```

**Root Causes & Solutions**:

| Cause | Solution |
|-------|----------|
| Many daemons (>50) | Increase `gc_max_daemon_workers` to 20-30 |
| Large shared daemon (>100 instances) | Increase `gc_max_instance_workers` to 50 |
| Slow storage (NFS) | Run GC less frequently, consider local cache |
| Large cache (>10000 blobs) | Normal - adjust expectations or clean manually |

**Resolution Steps**:

```bash
# 1. Increase parallelism
sudo vi /etc/nydus/config.toml
# Add:
# gc_max_daemon_workers = 30
# gc_max_instance_workers = 50

# 2. Restart
sudo systemctl restart nydus-snapshotter

# 3. Test
curl -X POST http://localhost:7788/api/v1/gc/run | jq .duration_ms
```

---

### Problem: GC Errors

**Symptoms**:
- `errors_count` > 0 in execution results
- `nydus_gc_errors_total` increasing
- "High GC error rate" alert firing

**Diagnosis**:

```bash
# 1. Check recent errors
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.errors

# 2. Check logs for details
journalctl -u nydus-snapshotter | grep "GC.*error" | tail -20

# 3. Check specific error types
journalctl -u nydus-snapshotter | grep -E "permission denied|timeout|no such file"
```

**Common Errors**:

| Error | Cause | Solution |
|-------|-------|----------|
| "permission denied" | Wrong file ownership | `chown -R containerd:containerd /var/lib/containerd/...` |
| "timeout querying daemon" | Slow daemon or network | Increase `gc_instance_timeout` |
| "no such file or directory" | Race condition | Ignore (blob deleted by another process) |
| "context deadline exceeded" | GC timeout | Increase timeout in manual trigger |
| "failed to delete blob" | File in use or locked | Check for running processes accessing cache |

**Resolution Steps**:

```bash
# Permission issues
sudo chown -R $(whoami):$(whoami) /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache

# Timeout issues
sudo vi /etc/nydus/config.toml
# Add: gc_instance_timeout = "15s"

# Restart and verify
sudo systemctl restart nydus-snapshotter
curl -X POST http://localhost:7788/api/v1/gc/run | jq .summary.errors_count
```

---

## Maintenance

### Routine Maintenance

#### Daily

- **Monitor GC execution**:
  ```bash
  curl http://localhost:7788/api/v1/gc/status | jq '{
    enabled: .enabled,
    last_run: .last_execution.completed_at,
    blobs_deleted: .last_execution.summary.blobs_deleted,
    errors: .last_execution.summary.errors_count
  }'
  ```

- **Check alert status**:
  - Verify no GC alerts firing
  - Review alert history for patterns

#### Weekly

- **Review metrics**:
  - GC duration trend
  - Disk space freed trend
  - Error rate trend

- **Check log errors**:
  ```bash
  journalctl -u nydus-snapshotter --since "7 days ago" | grep "GC.*error" | wc -l
  ```

#### Monthly

- **Performance review**:
  - Evaluate GC frequency (too often/too rare?)
  - Review grace period effectiveness
  - Assess parallelism settings

- **Capacity planning**:
  - Trend cache size growth
  - Predict future storage needs
  - Adjust GC schedule if needed

### Configuration Tuning

**Increase GC Frequency**:

When: Cache grows too quickly, disk space concerns

```toml
gc_period = "12h"  # Run twice daily
```

**Decrease GC Frequency**:

When: GC deletes very few blobs, wasting resources

```toml
gc_period = "48h"  # Run every 2 days
```

**Adjust Grace Period**:

When: Race conditions observed, or too conservative

```toml
gc_grace_period = "15m"  # More conservative
# or
gc_grace_period = "5m"   # More aggressive
```

**Increase Parallelism**:

When: GC duration >5 minutes

```toml
gc_max_daemon_workers = 30
gc_max_instance_workers = 50
gc_instance_timeout = "10s"
```

### Cache Size Management

**Manual Cache Inspection**:

```bash
# Total cache size
du -sh /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache

# Blob count
ls /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache | wc -l

# Old blobs (>7 days)
find /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache \
  -type f -mtime +7 -ls

# Largest blobs
du -ah /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache | sort -rh | head -20
```

**Manual Cleanup** (Emergency):

```bash
# Stop snapshotter
sudo systemctl stop nydus-snapshotter

# Remove old blobs manually
find /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache \
  -type f -mtime +30 -delete

# Restart
sudo systemctl start nydus-snapshotter
```

---

## Emergency Procedures

### Emergency: Disk Space Critical

**Symptoms**:
- Disk usage >90%
- "No space left on device" errors
- Container start failures

**Immediate Actions**:

```bash
# 1. Trigger immediate GC (manual)
curl -X POST http://localhost:7788/api/v1/gc/run

# 2. Monitor execution
while true; do
  curl http://localhost:7788/api/v1/gc/status | jq .currently_running
  sleep 5
done

# 3. Check space freed
curl http://localhost:7788/api/v1/gc/status | jq .last_execution.summary.bytes_freed

# 4. If still critical, reduce grace period temporarily
sudo vi /etc/nydus/config.toml
# Set: gc_grace_period = "1m"

sudo systemctl restart nydus-snapshotter
curl -X POST http://localhost:7788/api/v1/gc/run

# 5. If STILL critical, manual cleanup
find /var/lib/containerd/io.containerd.snapshotter.v1.nydus/cache \
  -type f -mtime +7 -delete
```

### Emergency: GC Deleted Active Blobs

**Symptoms**:
- Container start failures
- "Blob not found" errors in daemon logs
- Missing cache files

**Impact Assessment**:

```bash
# Check for blob errors
journalctl -u nydus-snapshotter --since "1 hour ago" | grep "blob.*not found"

# Identify affected containers
kubectl get pods --all-namespaces -o wide | grep -v Running
```

**Recovery Steps**:

```bash
# 1. STOP GC immediately
sudo vi /etc/nydus/config.toml
# Comment out: # gc_period = "24h"

sudo systemctl restart nydus-snapshotter

# 2. Verify GC disabled
curl http://localhost:7788/api/v1/gc/status | jq .enabled
# Should be false

# 3. Re-pull affected images
for img in $(kubectl get pods -A -o json | jq -r '.items[].spec.containers[].image' | sort -u); do
  ctr images pull $img
done

# 4. Restart affected pods
kubectl delete pod -l <selector>

# 5. Root cause analysis
# - Check grace period (was it too short?)
# - Check daemon states during GC
# - Review GC logs for that execution
journalctl -u nydus-snapshotter | grep "execution_id=<id>"

# 6. Fix configuration
# - Increase grace period to 15-20m
# - Add safety margins
# - Consider reducing GC frequency

# 7. Test extensively before re-enabling
```

### Emergency: GC Stuck/Hanging

**Symptoms**:
- GC running for >1 hour
- `currently_running` stuck at true
- No progress in logs

**Recovery Steps**:

```bash
# 1. Check if actually stuck
journalctl -u nydus-snapshotter -f | grep GC

# 2. Try graceful stop (send manual interrupt via API - not currently supported)
# Alternative: Wait for timeout

# 3. If no timeout set, restart snapshotter
sudo systemctl restart nydus-snapshotter

# 4. Verify GC reset
curl http://localhost:7788/api/v1/gc/status | jq .currently_running
# Should be false

# 5. Investigate cause
# - Check for hung daemons
# - Check storage I/O
# - Review timeout settings

# 6. Preventive measures
# - Set reasonable timeouts in manual triggers
# - Increase instance_timeout if needed
# - Monitor daemon health
```

---

## Operational Checklist

### Pre-Deployment

- [ ] GC configuration reviewed and approved
- [ ] Monitoring dashboards configured
- [ ] Alert rules deployed
- [ ] Test run completed successfully
- [ ] Rollback procedure documented
- [ ] Team trained on GC operations

### Post-Deployment

- [ ] GC scheduler started successfully
- [ ] First execution completed without errors
- [ ] Metrics visible in Prometheus
- [ ] Alerts configured and tested
- [ ] Documentation updated
- [ ] Runbook validated

### Ongoing Operations

- [ ] Daily: Monitor execution status
- [ ] Weekly: Review metrics and errors
- [ ] Monthly: Performance tuning review
- [ ] Quarterly: Configuration audit

---

## Contacts and Escalation

For issues beyond this runbook:

1. **Level 1**: Check documentation and logs
2. **Level 2**: Review metrics and perform diagnosis
3. **Level 3**: Disable GC, manual recovery
4. **Level 4**: Escalate to nydus-snapshotter maintainers

**Useful Resources**:
- Configuration docs: `/docs/gc-configuration.md`
- API reference: `/docs/gc-api.md`
- GitHub issues: https://github.com/containerd/nydus-snapshotter/issues
- Slack: #nydus-snapshotter (CNCF Slack)
