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

	"github.com/containerd/nydus-snapshotter/config/daemonconfig"
)

// fakeSys points sysfs and /dev at a temporary directory holding the given NBD devices; the
// configured ones expose a pid like the kernel does.
func fakeSys(t *testing.T, devices []string, configured []string) {
	t.Helper()
	root := t.TempDir()
	oldSys, oldDev := sysBlockDir, devDir
	sysBlockDir, devDir = filepath.Join(root, "sys"), filepath.Join(root, "dev")
	t.Cleanup(func() { sysBlockDir, devDir = oldSys, oldDev })
	require.NoError(t, os.MkdirAll(devDir, 0o755))
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
		InUse      []int
		Want       int
		WantErr    bool
	}{
		{Name: "FirstDevice", Devices: []string{"nbd0", "nbd1"}, Want: 0},
		{Name: "SkipsConfigured", Devices: []string{"nbd0", "nbd1"}, Configured: []string{"nbd0"}, Want: 1},
		{Name: "SkipsInUse", Devices: []string{"nbd0", "nbd1"}, InUse: []int{0}, Want: 1},
		// Regression: a missing device cannot be connected.
		{Name: "SkipsMissingDevice", Devices: []string{"nbd1"}, Want: 1},
		{Name: "AllBusy", Devices: []string{"nbd0", "nbd1"}, Configured: []string{"nbd0"}, InUse: []int{1}, WantErr: true},
		{Name: "NoModuleLoaded", WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			fakeSys(t, tt.Devices, tt.Configured)
			inUse := map[string]bool{}
			for _, i := range tt.InUse {
				inUse[Device(i)] = true
			}

			device, err := Allocate(inUse)
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, Device(tt.Want), device)
		})
	}
}

func TestTune(t *testing.T) {
	tests := []struct {
		Name    string
		Device  string
		WantErr bool
	}{
		{Name: "Existing", Device: "nbd0"},
		{Name: "MissingDevice", Device: "nbd1", WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			fakeSys(t, []string{"nbd0"}, nil)

			err := Tune("/dev/" + tt.Device)
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			for _, attr := range []string{"max_sectors_kb", "read_ahead_kb"} {
				b, err := os.ReadFile(filepath.Join(sysBlockDir, tt.Device, "queue", attr))
				require.NoError(t, err)
				assert.Equal(t, "4096", string(b), attr)
			}
		})
	}
}

func TestUmountNotMounted(t *testing.T) {
	tests := []struct {
		Name string
		Path func(t *testing.T) string
	}{
		{Name: "Missing", Path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }},
		{Name: "PlainDirectory", Path: func(t *testing.T) string { return t.TempDir() }},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			assert.NoError(t, Umount(tt.Path(t)))
		})
	}
}

func TestTuneDaemonConfig(t *testing.T) {
	small, large := 10<<20, 128<<20
	tests := []struct {
		Name    string
		Amplify *int
		Want    int
	}{
		{Name: "Unset", Want: amplifyIO},
		{Name: "Smaller", Amplify: &small, Want: amplifyIO},
		{Name: "Larger", Amplify: &large, Want: large},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			cfg := &daemonconfig.FuseDaemonConfig{AmplifyIo: tt.Amplify}

			TuneDaemonConfig(cfg)

			require.NotNil(t, cfg.AmplifyIo)
			assert.Equal(t, tt.Want, *cfg.AmplifyIo)
		})
	}
}
