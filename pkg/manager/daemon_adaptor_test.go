/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/containerd/nydus-snapshotter/config"
	"github.com/containerd/nydus-snapshotter/pkg/daemon"
	"github.com/containerd/nydus-snapshotter/pkg/rafs"
)

func TestBuildDaemonCommandNBD(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, config.ProcessConfigurations(&config.SnapshotterConfig{Root: root, DaemonMode: string(config.DaemonModeDedicated)}))
	bootstrap := filepath.Join(config.GetSnapshotsRootDir(), "1", "fs", "image", "image.boot")
	require.NoError(t, os.MkdirAll(filepath.Dir(bootstrap), 0o755))
	require.NoError(t, os.WriteFile(bootstrap, nil, 0o644))

	tests := []struct {
		Name    string
		Device  string
		Rafs    bool
		WantErr bool
	}{
		{Name: "Start", Device: "/dev/nbd3", Rafs: true},
		{Name: "NoRafs", Device: "/dev/nbd3", WantErr: true},
		{Name: "NoDevice", Rafs: true, WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			d, err := daemon.NewDaemon(
				daemon.WithSocketDir(root),
				daemon.WithConfigDir(root),
				daemon.WithLogDir(root),
				daemon.WithFsDriver(config.FsDriverNbd),
				daemon.WithDaemonMode(config.DaemonModeDedicated),
				daemon.WithNBDDevice(tt.Device),
			)
			require.NoError(t, err)
			if tt.Rafs {
				r, err := rafs.NewRafs("1", "image", config.FsDriverNbd)
				require.NoError(t, err)
				d.AddRafsInstance(r)
			}

			cmd, err := (&Manager{}).BuildDaemonCommand(d, "nydusd", false)
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			args := cmd.Args[1:]
			assert.Equal(t, "nbd", args[0])
			assert.Equal(t, tt.Device, args[len(args)-1])
			assert.Subset(t, args, []string{"--config", d.ConfigFile(""), "--bootstrap", bootstrap})
			assert.NotContains(t, args, "--mountpoint")
			assert.NotContains(t, args, "--failover-policy")
		})
	}
}
