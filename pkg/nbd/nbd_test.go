/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package nbd

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultEventually = 5 * time.Second

// fakeNydusd mimics `nydusd nbd ... <device>`: it registers itself as the
// server of the device in the fake sysfs, then serves until killed.
const fakeNydusd = `#!/bin/sh
for dev; do :; done
echo $$ > "$FAKE_SYS_BLOCK/$(basename "$dev")/pid"
trap 'rm -f "$FAKE_SYS_BLOCK/$(basename "$dev")/pid"; exit 0' TERM
while :; do sleep 0.05; done
`

// setup fakes sysfs with the given NBD devices and returns a Manager running
// the fake nydusd plus a snapshot directory.
func setup(t *testing.T, devices ...string) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	for _, d := range devices {
		require.NoError(t, os.MkdirAll(filepath.Join(sys, d), 0o755))
	}
	bin := filepath.Join(root, "nydusd")
	require.NoError(t, os.WriteFile(bin, []byte(fakeNydusd), 0o755))
	t.Setenv("FAKE_SYS_BLOCK", sys)

	oldSys := sysBlockDir
	sysBlockDir = sys
	t.Cleanup(func() { sysBlockDir = oldSys })

	snapshotDir := filepath.Join(root, "snapshots", "1")
	require.NoError(t, os.MkdirAll(snapshotDir, 0o755))
	return NewManager(bin), snapshotDir
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestExport(t *testing.T) {
	tests := []struct {
		Name    string
		Devices []string
		// Busy devices are already served by another process.
		Busy    []string
		Want    string
		WantErr bool
	}{
		{Name: "FirstDevice", Devices: []string{"nbd0", "nbd1"}, Want: "/dev/nbd0"},
		{Name: "SkipsBusyDevice", Devices: []string{"nbd0", "nbd1"}, Busy: []string{"nbd0"}, Want: "/dev/nbd1"},
		{Name: "NumericOrder", Devices: []string{"nbd10", "nbd2"}, Want: "/dev/nbd2"},
		{Name: "AllBusy", Devices: []string{"nbd0"}, Busy: []string{"nbd0"}, WantErr: true},
		{Name: "NoNBDModule", Devices: []string{"sda"}, WantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			m, snapshotDir := setup(t, tt.Devices...)
			for _, d := range tt.Busy {
				require.NoError(t, os.WriteFile(filepath.Join(sysBlockDir, d, "pid"), []byte("1"), 0o644))
			}

			device, err := m.Export(snapshotDir, "/boot", "/config.json")
			if tt.WantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Remove(snapshotDir) })
			assert.Equal(t, tt.Want, device)

			st, err := readState(stateDir(snapshotDir))
			require.NoError(t, err)
			assert.Equal(t, tt.Want, st.Device)
			assert.True(t, processAlive(st.Pid))
		})
	}
}

func TestExportLifecycle(t *testing.T) {
	tests := []struct {
		Name   string
		Assert func(t *testing.T, m *Manager, snapshotDir string, first state)
	}{
		{
			Name: "Idempotent",
			Assert: func(t *testing.T, m *Manager, snapshotDir string, first state) {
				device, err := m.Export(snapshotDir, "/boot", "/config.json")
				require.NoError(t, err)
				assert.Equal(t, first.Device, device)
				st, err := readState(stateDir(snapshotDir))
				require.NoError(t, err)
				assert.Equal(t, first.Pid, st.Pid)
			},
		},
		{
			Name: "RestartsStaleExport",
			Assert: func(t *testing.T, m *Manager, snapshotDir string, first state) {
				// The daemon died (e.g. node reboot): the device has no server.
				require.NoError(t, syscall.Kill(first.Pid, syscall.SIGKILL))
				require.NoError(t, os.Remove(filepath.Join(sysBlockDir, filepath.Base(first.Device), "pid")))

				device, err := m.Export(snapshotDir, "/boot", "/config.json")
				require.NoError(t, err)
				assert.Equal(t, first.Device, device)
				st, err := readState(stateDir(snapshotDir))
				require.NoError(t, err)
				assert.NotEqual(t, first.Pid, st.Pid)
			},
		},
		{
			Name: "Remove",
			Assert: func(t *testing.T, m *Manager, snapshotDir string, first state) {
				require.NoError(t, m.Remove(snapshotDir))
				assert.Eventually(t, func() bool { return !processAlive(first.Pid) }, defaultEventually, pollInterval)
				_, err := os.Stat(stateDir(snapshotDir))
				assert.True(t, os.IsNotExist(err))
				// Removing again is a no-op.
				assert.NoError(t, m.Remove(snapshotDir))
			},
		},
		{
			Name: "UpdateAuthWithoutAPI",
			Assert: func(t *testing.T, m *Manager, snapshotDir string, first state) {
				// The fake daemon has no API socket: the error is reported.
				assert.Error(t, m.UpdateAuth(snapshotDir, "auth"))
				// Without export there is nothing to update.
				require.NoError(t, m.Remove(snapshotDir))
				assert.NoError(t, m.UpdateAuth(snapshotDir, "auth"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			m, snapshotDir := setup(t, "nbd0")
			_, err := m.Export(snapshotDir, "/boot", "/config.json")
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Remove(snapshotDir) })
			first, err := readState(stateDir(snapshotDir))
			require.NoError(t, err)

			tt.Assert(t, m, snapshotDir, first)
		})
	}
}

func TestStateAlive(t *testing.T) {
	_, _ = setup(t, "nbd0")
	self := os.Getpid()
	tests := []struct {
		Name      string
		DevicePid string
		State     state
		Want      bool
	}{
		{Name: "ServedByProcess", DevicePid: strconv.Itoa(self), State: state{Device: "/dev/nbd0", Pid: self}, Want: true},
		{Name: "NotConnected", State: state{Device: "/dev/nbd0", Pid: self}},
		{Name: "ServedByAnotherProcess", DevicePid: "1", State: state{Device: "/dev/nbd0", Pid: self}},
		{Name: "NoPid", DevicePid: strconv.Itoa(self), State: state{Device: "/dev/nbd0"}},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			pidFile := filepath.Join(sysBlockDir, "nbd0", "pid")
			_ = os.Remove(pidFile)
			if tt.DevicePid != "" {
				require.NoError(t, os.WriteFile(pidFile, []byte(tt.DevicePid+"\n"), 0o644))
			}
			assert.Equal(t, tt.Want, tt.State.alive())
		})
	}
}
