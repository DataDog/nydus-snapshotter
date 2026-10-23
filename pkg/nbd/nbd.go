/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

// Package nbd exports RAFS instances as read-only NBD block devices served by
// `nydusd nbd`, so that VM runtimes (Kata) can hot-plug the image as a block
// device and mount it as EROFS in the guest instead of sharing the FUSE mount
// through virtio-fs. Data is still fetched lazily from the backend.
//
// One export per RAFS instance (image), shared by all containers of that
// image, lives as long as the RAFS instance. Its state is kept next to the
// snapshot so exports survive snapshotter restarts, like nydusd FUSE daemons.
package nbd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/log"
	"github.com/pkg/errors"

	"github.com/containerd/nydus-snapshotter/pkg/daemon"
)

const (
	stateDirName  = "nbd"
	stateFileName = "state.json"
	apiSockName   = "api.sock"
	logFileName   = "nydusd.log"

	readyTimeout = 10 * time.Second
	stopTimeout  = 5 * time.Second
	pollInterval = 20 * time.Millisecond
)

var (
	// Overridden by tests.
	sysBlockDir = "/sys/block"
	devDir      = "/dev"
	procDir     = "/proc"
)

// Manager starts and stops `nydusd nbd` exports.
type Manager struct {
	nydusdPath string
	// mu serializes device allocation and per-snapshot state changes; exports
	// are created once per image so contention is negligible.
	mu sync.Mutex
}

// state is persisted in <snapshot dir>/nbd/state.json.
type state struct {
	Device string `json:"device"`
	Pid    int    `json:"pid"`
}

func NewManager(nydusdPath string) *Manager {
	return &Manager{nydusdPath: nydusdPath}
}

func stateDir(snapshotDir string) string {
	return filepath.Join(snapshotDir, stateDirName)
}

// Export returns the NBD device serving the RAFS instance whose bootstrap and
// nydusd configuration are given, starting `nydusd nbd` if needed. It is
// idempotent.
func (m *Manager) Export(snapshotDir, bootstrap, configFile string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	dir := stateDir(snapshotDir)
	if st, err := readState(dir); err == nil && st.alive() {
		return st.Device, nil
	}
	// No export, or a stale one (e.g. after a node reboot): start afresh.
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrap(err, "create nbd state dir")
	}

	device, err := freeDevice()
	if err != nil {
		return "", err
	}

	logFile, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", errors.Wrap(err, "open nydusd nbd log")
	}
	defer logFile.Close()

	cmd := exec.Command(m.nydusdPath, "nbd",
		"--bootstrap", bootstrap,
		"--config", configFile,
		"--apisock", filepath.Join(dir, apiSockName),
		"--log-level", "info",
		device)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Like FUSE daemons, exports must outlive snapshotter restarts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", errors.Wrap(err, "start nydusd nbd")
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	st := state{Device: device, Pid: cmd.Process.Pid}
	if err := st.waitReady(exited); err != nil {
		stopProcess(st.Pid, exited)
		return "", errors.Wrapf(err, "nydusd nbd on %s not ready, see %s", device, filepath.Join(dir, logFileName))
	}
	if err := writeState(dir, st); err != nil {
		stopProcess(st.Pid, exited)
		return "", err
	}
	log.L.Infof("exported RAFS %s on %s (pid %d)", bootstrap, device, st.Pid)
	return device, nil
}

// Remove stops the export of a RAFS instance, if any. It must only be called
// once no container uses the image anymore.
func (m *Manager) Remove(snapshotDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dir := stateDir(snapshotDir)
	st, err := readState(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && st.alive() {
		stopProcess(st.Pid, nil)
	}
	return os.RemoveAll(dir)
}

// UpdateAuth hot-reloads registry credentials of a running export.
func (m *Manager) UpdateAuth(snapshotDir, auth string) error {
	m.mu.Lock()
	dir := stateDir(snapshotDir)
	st, err := readState(dir)
	m.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.alive() {
		return nil
	}
	client, err := daemon.NewNydusClient(filepath.Join(dir, apiSockName))
	if err != nil {
		return err
	}
	// The registry backend of a block device is registered under id "/".
	return client.UpdateConfig("/", map[string]string{"registry_auth": auth})
}

func readState(dir string) (state, error) {
	var st state
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, errors.Wrap(err, "decode nbd state")
	}
	return st, nil
}

func writeState(dir string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, stateFileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return errors.Wrap(err, "write nbd state")
	}
	return errors.Wrap(os.Rename(tmp, filepath.Join(dir, stateFileName)), "install nbd state")
}

// alive reports whether the recorded daemon still serves the recorded device.
func (st state) alive() bool {
	if st.Pid <= 0 {
		return false
	}
	tid := devicePid(st.Device)
	if tid == 0 {
		return false
	}
	// The kernel records the thread running NBD_DO_IT, not the process.
	_, err := os.Stat(filepath.Join(procDir, strconv.Itoa(st.Pid), "task", strconv.Itoa(tid)))
	return err == nil
}

func (st state) waitReady(exited <-chan struct{}) error {
	deadline := time.After(readyTimeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if st.alive() {
			return nil
		}
		select {
		case <-exited:
			return errors.New("nydusd exited")
		case <-deadline:
			return errors.New("timeout")
		case <-ticker.C:
		}
	}
}

// devicePid returns the id of the server thread attached to an NBD device, or
// 0. The kernel exposes it while NBD_DO_IT runs.
func devicePid(device string) int {
	b, err := os.ReadFile(filepath.Join(sysBlockDir, filepath.Base(device), "pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// freeDevice returns the first NBD device without a server. Callers hold
// Manager.mu, and only the snapshotter attaches NBD devices.
func freeDevice() (string, error) {
	entries, err := os.ReadDir(sysBlockDir)
	if err != nil {
		return "", errors.Wrap(err, "list block devices")
	}
	var indexes []int
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), "nbd"); ok {
			if idx, err := strconv.Atoi(rest); err == nil {
				indexes = append(indexes, idx)
			}
		}
	}
	if len(indexes) == 0 {
		return "", errors.New("no NBD device, is the nbd kernel module loaded?")
	}
	sort.Ints(indexes)
	for _, idx := range indexes {
		device := filepath.Join(devDir, fmt.Sprintf("nbd%d", idx))
		if devicePid(device) == 0 {
			return device, nil
		}
	}
	return "", errors.Errorf("all %d NBD devices are in use", len(indexes))
}

// stopProcess asks nydusd to exit (it disconnects the device), then kills it.
// exited, when known, is closed once the process has been reaped.
func stopProcess(pid int, exited <-chan struct{}) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if exited != nil {
			select {
			case <-exited:
				return
			default:
			}
		} else if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(pollInterval)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
