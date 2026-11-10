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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

var sysBlockDir = "/sys/block"

// Allocate returns the lowest NBD device neither in inUse nor configured in the kernel.
func Allocate(inUse map[string]bool) (string, error) {
	paths, err := filepath.Glob(filepath.Join(sysBlockDir, "nbd*"))
	if err != nil {
		return "", err
	}
	index := func(p string) int {
		i, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(p), "nbd"))
		return i
	}
	slices.SortFunc(paths, func(a, b string) int { return index(a) - index(b) })
	for _, p := range paths {
		device := "/dev/" + filepath.Base(p)
		// The kernel keeps the pid of a configured device until it is disconnected.
		if _, err := os.Stat(filepath.Join(p, "pid")); inUse[device] || err == nil {
			continue
		}
		return device, nil
	}
	return "", errors.New("no free NBD device, is the nbd module loaded with enough nbds_max?")
}

// SetQueueAttributes sets attributes of the block queue of device.
func SetQueueAttributes(device string, attributes map[string]string) error {
	for attr, value := range attributes {
		p := filepath.Join(sysBlockDir, filepath.Base(device), "queue", attr)
		if err := os.WriteFile(p, []byte(value), 0o644); err != nil {
			return errors.Wrapf(err, "write %s", p)
		}
	}
	return nil
}
