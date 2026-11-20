/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildNextAPISocket(t *testing.T) {
	for cur, want := range map[string]string{
		"api.sock":    "api1.sock",
		"api2.sock":   "api3.sock",
		"api23.sock":  "api24.sock",
		"api222.sock": "api223.sock",
	} {
		next, err := buildNextAPISocket(cur)
		assert.Nil(t, err)
		assert.Equal(t, want, next)
	}

	_, err := buildNextAPISocket("apisock")
	assert.Error(t, err)
}
