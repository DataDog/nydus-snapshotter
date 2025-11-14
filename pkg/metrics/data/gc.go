/*
 * Copyright (c) 2025. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package data

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	gcStatusLabel = "status" // success, error
	gcModeLabel   = "mode"   // scheduled, manual
)

var (
	// GCLastRunTimestamp records the Unix timestamp of the last GC execution
	GCLastRunTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "nydus_gc_last_run_timestamp_seconds",
			Help: "Unix timestamp of the last GC execution.",
		},
	)

	// GCBlobsDeleted counts the total number of blobs deleted by GC
	GCBlobsDeleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nydus_gc_blobs_deleted_total",
			Help: "Total number of blobs deleted by GC.",
		},
		[]string{gcModeLabel},
	)

	// GCBlobsDeletedBytes counts the total bytes freed by GC
	GCBlobsDeletedBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nydus_gc_blobs_deleted_bytes_total",
			Help: "Total bytes freed by GC blob deletion.",
		},
		[]string{gcModeLabel},
	)

	// GCErrors counts the total number of errors during GC
	GCErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nydus_gc_errors_total",
			Help: "Total number of errors during GC execution.",
		},
		[]string{gcModeLabel},
	)

	// GCDuration records the duration of GC executions
	GCDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nydus_gc_duration_seconds",
			Help:    "Duration of GC executions in seconds.",
			Buckets: prometheus.DefBuckets, // [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
		},
		[]string{gcModeLabel, gcStatusLabel},
	)

	// GCBlobsScanned records the number of blobs scanned during GC
	GCBlobsScanned = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nydus_gc_blobs_scanned",
			Help: "Number of blobs scanned in the last GC execution.",
		},
		[]string{gcModeLabel},
	)

	// GCBlobsReferenced records the number of referenced blobs found during GC
	GCBlobsReferenced = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nydus_gc_blobs_referenced",
			Help: "Number of referenced blobs found in the last GC execution.",
		},
		[]string{gcModeLabel},
	)

	// GCExecutions counts the total number of GC executions
	GCExecutions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nydus_gc_executions_total",
			Help: "Total number of GC executions.",
		},
		[]string{gcModeLabel, gcStatusLabel},
	)
)
