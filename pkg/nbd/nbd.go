/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

// Package nbd holds helpers of the nbd filesystem driver: a dedicated `nydusd nbd`
// serves the image of a RAFS instance as a read-only NBD device, mounted as EROFS on the
// host and attached as is to VM guests.
package nbd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"

	"github.com/containerd/nydus-snapshotter/config/daemonconfig"
	"github.com/containerd/nydus-snapshotter/pkg/utils/mount"
)

const (
	// MaxDevices bounds the device indices Allocate considers. Only devices created by the nbd
	// module (its nbds_max parameter) are used: nydusd keeps them on disconnect.
	MaxDevices = 4096
)

var (
	// Overridden by tests.
	sysBlockDir = "/sys/block"
	devDir      = "/dev"
)

// Device returns the path of NBD device index.
func Device(index int) string {
	return filepath.Join(devDir, fmt.Sprintf("nbd%d", index))
}

// configured reports whether NBD device index is configured: the kernel exposes the pid of
// the process which configured it until it is disconnected, even if that process died.
func configured(index int) bool {
	_, err := os.Stat(filepath.Join(sysBlockDir, fmt.Sprintf("nbd%d", index), "pid"))
	return err == nil
}

// exists reports whether the kernel has NBD device index.
func exists(index int) bool {
	_, err := os.Stat(filepath.Join(sysBlockDir, fmt.Sprintf("nbd%d", index)))
	return err == nil
}

// Allocate returns the lowest existing NBD device neither in inUse nor configured in the
// kernel. The caller serializes allocations and records the device before releasing its lock.
func Allocate(inUse map[string]bool) (string, error) {
	for i := 0; i < MaxDevices; i++ {
		d := Device(i)
		if !exists(i) || inUse[d] || configured(i) {
			continue
		}
		return d, nil
	}
	return "", errors.New("no free NBD device, is the nbd module loaded with enough nbds_max?")
}

// Requests and readahead of NBD devices, in KiB. Both replace the kernel default of 128 KiB:
// each request nydusd serves costs a backend round trip per missing range on a cold image, so
// larger requests make fewer, larger fetches.
const (
	maxSectorsKB = 4096
	readAheadKB  = 4096
)

// Tune sets the request size and readahead of device.
func Tune(device string) error {
	for attr, kb := range map[string]int{"max_sectors_kb": maxSectorsKB, "read_ahead_kb": readAheadKB} {
		p := filepath.Join(sysBlockDir, filepath.Base(device), "queue", attr)
		if err := os.WriteFile(p, []byte(strconv.Itoa(kb)), 0o644); err != nil {
			return errors.Wrap(err, "write "+p)
		}
	}
	return nil
}

// amplifyIO is the least data nydusd fetches per cache miss of an NBD device, in bytes. A
// fetch costs a backend round trip (~100 ms to the first byte from object storage): the
// kernel requests of a block device are much smaller than the reads of a FUSE mount, so
// without a large amplification a cold image is read in many small round trips.
const amplifyIO = 64 << 20

// TuneDaemonConfig raises the IO amplification of the configuration of a daemon of the nbd
// driver to amplifyIO.
func TuneDaemonConfig(cfg daemonconfig.DaemonConfig) {
	c, ok := cfg.(*daemonconfig.FuseDaemonConfig)
	if !ok || (c.AmplifyIo != nil && *c.AmplifyIo >= amplifyIO) {
		return
	}
	v := amplifyIO
	c.AmplifyIo = &v
}

// Mount mounts the EROFS image of device read-only at mountpoint, unless already mounted.
func Mount(device, mountpoint string) error {
	if mounted, err := mount.IsMountpoint(mountpoint); err != nil {
		return err
	} else if mounted {
		return nil
	}
	if err := unix.Mount(device, mountpoint, "erofs", unix.MS_RDONLY, ""); err != nil {
		return errors.Wrapf(err, "mount %s as erofs on %s", device, mountpoint)
	}
	return nil
}

// Umount unmounts the EROFS image at mountpoint, if mounted.
func Umount(mountpoint string) error {
	if _, err := os.Stat(mountpoint); os.IsNotExist(err) {
		return nil
	}
	if mounted, err := mount.IsMountpoint(mountpoint); err != nil || !mounted {
		return err
	}
	return errors.Wrapf(unix.Unmount(mountpoint, 0), "umount %s", mountpoint)
}
