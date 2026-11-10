/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package nbd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSys points sysfs at a temporary directory holding the given NBD devices; the configured
// ones expose a pid like the kernel does.
func fakeSys(t *testing.T, devices []string, configured []string) {
	t.Helper()
	old := sysBlockDir
	sysBlockDir = t.TempDir()
	t.Cleanup(func() { sysBlockDir = old })
	for _, d := range devices {
		require.NoError(t, os.MkdirAll(filepath.Join(sysBlockDir, d, "queue"), 0o755))
	}
	for _, d := range configured {
		require.NoError(t, os.WriteFile(filepath.Join(sysBlockDir, d, "pid"), []byte("1"), 0o644))
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		Name       string
		Devices    []string
		Configured []string
		InUse      []string
		Want       string
		WantErr    bool
	}{
		{Name: "FirstDevice", Devices: []string{"nbd0", "nbd1"}, Want: "/dev/nbd0"},
		{Name: "NumericOrder", Devices: []string{"nbd10", "nbd2"}, Want: "/dev/nbd2"},
		{Name: "SkipsConfigured", Devices: []string{"nbd0", "nbd1"}, Configured: []string{"nbd0"}, Want: "/dev/nbd1"},
		{Name: "SkipsInUse", Devices: []string{"nbd0", "nbd1"}, InUse: []string{"/dev/nbd0"}, Want: "/dev/nbd1"},
		// Regression: a missing device cannot be connected.
		{Name: "SkipsMissingDevice", Devices: []string{"nbd1"}, Want: "/dev/nbd1"},
		{Name: "AllBusy", Devices: []string{"nbd0", "nbd1"}, Configured: []string{"nbd0"}, InUse: []string{"/dev/nbd1"}, WantErr: true},
		{Name: "NoModuleLoaded", WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			fakeSys(t, tt.Devices, tt.Configured)
			inUse := map[string]bool{}
			for _, d := range tt.InUse {
				inUse[d] = true
			}

			device, err := Allocate(inUse)
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.Want, device)
		})
	}
}

func TestSetQueueAttributes(t *testing.T) {
	tests := []struct {
		Name       string
		Device     string
		Attributes map[string]string
		WantErr    bool
	}{
		{Name: "Set", Device: "nbd0", Attributes: map[string]string{"max_sectors_kb": "4096", "read_ahead_kb": "1024"}},
		{Name: "None", Device: "nbd0"},
		{Name: "MissingDevice", Device: "nbd1", Attributes: map[string]string{"read_ahead_kb": "1024"}, WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			fakeSys(t, []string{"nbd0"}, nil)

			err := SetQueueAttributes("/dev/"+tt.Device, tt.Attributes)
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			for attr, want := range tt.Attributes {
				b, err := os.ReadFile(filepath.Join(sysBlockDir, tt.Device, "queue", attr))
				require.NoError(t, err)
				assert.Equal(t, want, string(b), attr)
			}
		})
	}
}
