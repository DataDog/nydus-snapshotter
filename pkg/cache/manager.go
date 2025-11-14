/*
 * Copyright (c) 2022. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package cache

import (
	"context"
	"os"
	"path"
	"time"

	"github.com/pkg/errors"

	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/log"
	"github.com/containerd/nydus-snapshotter/pkg/store"
)

const (
	imageDiskFileSuffix = ".image.disk"
	layerDiskFileSuffix = ".layer.disk"
	chunkMapFileSuffix  = ".chunk_map"
	metaFileSuffix      = ".blob.meta"
	// Blob cache is suffixed after nydus v2.1
	dataFileSuffix = ".blob.data"
)

// Disk cache manager for fusedev.
type Manager struct {
	cacheDir string
	period   time.Duration
	eventCh  chan struct{}
}

type Opt struct {
	Disabled bool
	CacheDir string
	Period   time.Duration
	Database *store.Database
}

func NewManager(opt Opt) (*Manager, error) {
	// Ensure cache directory exists
	if err := os.MkdirAll(opt.CacheDir, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create cache dir %s", opt.CacheDir)
	}

	eventCh := make(chan struct{})
	m := &Manager{
		cacheDir: opt.CacheDir,
		period:   opt.Period,
		eventCh:  eventCh,
	}

	return m, nil
}

func (m *Manager) CacheDir() string {
	return m.cacheDir
}

// Report each blob disk usage
// TODO: For fscache cache files, the cache files are managed by nydusd and Linux kernel
// We don't know how it manages cache files. A method to address this is to query nydusd.
// So we can't report cache usage in the case of fscache now
func (m *Manager) CacheUsage(ctx context.Context, blobID string) (snapshots.Usage, error) {
	var usage snapshots.Usage

	blobCachePath := path.Join(m.cacheDir, blobID)
	blobChunkMap := path.Join(m.cacheDir, blobID+chunkMapFileSuffix)
	// For backward compatibility
	blobCacheSuffixedPath := path.Join(m.cacheDir, blobID+dataFileSuffix)
	blobChunkMapSuffixedPath := path.Join(m.cacheDir, blobID+dataFileSuffix+chunkMapFileSuffix)
	blobMeta := path.Join(m.cacheDir, blobID+metaFileSuffix)
	imageDisk := path.Join(m.cacheDir, blobID+imageDiskFileSuffix)
	layerDisk := path.Join(m.cacheDir, blobID+layerDiskFileSuffix)

	stuffs := []string{blobCachePath, blobChunkMap, blobCacheSuffixedPath, blobChunkMapSuffixedPath, blobMeta, imageDisk, layerDisk}

	for _, f := range stuffs {
		du, err := fs.DiskUsage(ctx, f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				log.L.Debugf("Cache %s does not exist", f)
				continue
			}
			return snapshots.Usage{}, err
		}
		usage.Add(snapshots.Usage(du))
	}

	return usage, nil
}

func (m *Manager) RemoveBlobCache(blobID string) error {
	blobCachePath := path.Join(m.cacheDir, blobID)
	blobChunkMap := path.Join(m.cacheDir, blobID+chunkMapFileSuffix)
	blobCacheSuffixedPath := path.Join(m.cacheDir, blobID+dataFileSuffix)
	blobChunkMapSuffixedPath := path.Join(m.cacheDir, blobID+dataFileSuffix+chunkMapFileSuffix)
	blobMeta := path.Join(m.cacheDir, blobID+metaFileSuffix)
	imageDisk := path.Join(m.cacheDir, blobID+imageDiskFileSuffix)
	layerDisk := path.Join(m.cacheDir, blobID+layerDiskFileSuffix)

	// NOTE: Delete chunk bitmap file before data blob
	stuffs := []string{blobChunkMap, blobChunkMapSuffixedPath, blobMeta, blobCachePath, blobCacheSuffixedPath, imageDisk, layerDisk}

	for _, f := range stuffs {
		err := os.Remove(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				log.L.Debugf("file %s doest not exist.", f)
				continue
			}
			return err
		}
	}
	return nil
}

// ListAllBlobs returns all blob IDs found in the cache directory.
// It scans for files matching blob patterns and extracts unique blob IDs.
func (m *Manager) ListAllBlobs(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(m.cacheDir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read cache directory %s", m.cacheDir)
	}

	// Use map to deduplicate blob IDs (multiple files per blob)
	blobMap := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		blobID := m.extractBlobID(name)
		if blobID != "" {
			blobMap[blobID] = true
		}
	}

	// Convert map to slice
	blobs := make([]string, 0, len(blobMap))
	for blobID := range blobMap {
		blobs = append(blobs, blobID)
	}

	return blobs, nil
}

// extractBlobID extracts the blob ID from a cache file name by removing known suffixes.
func (m *Manager) extractBlobID(filename string) string {
	// Try removing known suffixes
	suffixes := []string{
		dataFileSuffix + chunkMapFileSuffix, // .blob.data.chunk_map (longest first)
		chunkMapFileSuffix,                  // .chunk_map
		dataFileSuffix,                      // .blob.data
		metaFileSuffix,                      // .blob.meta
		imageDiskFileSuffix,                 // .image.disk
		layerDiskFileSuffix,                 // .layer.disk
	}

	for _, suffix := range suffixes {
		if len(filename) > len(suffix) && filename[len(filename)-len(suffix):] == suffix {
			return filename[:len(filename)-len(suffix)]
		}
	}

	// If no suffix matched, assume it's a legacy unsuffixed blob
	return filename
}

// IsBlobSafeToDelete checks if a blob can be safely deleted.
// It verifies the blob's modification time is older than the grace period.
func (m *Manager) IsBlobSafeToDelete(blobID string, gracePeriod time.Duration) (bool, error) {
	// Check all possible blob file paths
	paths := []string{
		path.Join(m.cacheDir, blobID),
		path.Join(m.cacheDir, blobID+dataFileSuffix),
		path.Join(m.cacheDir, blobID+chunkMapFileSuffix),
		path.Join(m.cacheDir, blobID+dataFileSuffix+chunkMapFileSuffix),
		path.Join(m.cacheDir, blobID+metaFileSuffix),
		path.Join(m.cacheDir, blobID+imageDiskFileSuffix),
		path.Join(m.cacheDir, blobID+layerDiskFileSuffix),
	}

	now := time.Now()
	cutoff := now.Add(-gracePeriod)

	// Check if any blob file was modified recently
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // File doesn't exist, check next
			}
			return false, errors.Wrapf(err, "failed to stat %s", p)
		}

		// If any file was modified within grace period, not safe to delete
		if info.ModTime().After(cutoff) {
			log.L.Debugf("Blob %s modified too recently: %v (cutoff: %v)", blobID, info.ModTime(), cutoff)
			return false, nil
		}
	}

	return true, nil
}
