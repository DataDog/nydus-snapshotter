/*
 * Copyright (c) 2022. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package command

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildCommand(t *testing.T) {

	c := []Opt{WithMode("singleton"),
		WithFscacheDriver("fs_cache_dir"),
		WithFscacheThreads(4),
		WithAPISock("/dummy/apisock"),
		WithUpgrade()}

	args, err := BuildCommand(c)
	assert.Nil(t, err)
	actual := strings.Join(args, " ")
	assert.Equal(t, "singleton --fscache fs_cache_dir --fscache-threads 4 --upgrade --apisock /dummy/apisock", actual)

	c1 := []Opt{WithMode("singleton"),
		WithFscacheDriver("fs_cache_dir"),
		WithFscacheThreads(4),
		WithAPISock("/dummy/apisock")}

	args1, err := BuildCommand(c1)
	assert.Nil(t, err)
	actual1 := strings.Join(args1, " ")
	assert.Equal(t, "singleton --fscache fs_cache_dir --fscache-threads 4 --apisock /dummy/apisock", actual1)
}

func TestBuildNBDCommand(t *testing.T) {
	tests := []struct {
		Name string
		Opts []Opt
		Want string
	}{
		{
			Name: "Start",
			Opts: []Opt{WithMode("nbd"), WithNBDDevice("/dev/nbd3"), WithConfig("/c.json"),
				WithBootstrap("/image.boot"), WithNBDThreads(4), WithAPISock("/api.sock")},
			Want: "nbd --config /c.json --bootstrap /image.boot --apisock /api.sock --threads 4 /dev/nbd3",
		},
		{
			Name: "WithSupervisor",
			Opts: []Opt{WithMode("nbd"), WithNBDDevice("/dev/nbd3"), WithID("d1"),
				WithSupervisor("/su.sock")},
			Want: "nbd --id d1 --supervisor /su.sock /dev/nbd3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			args, err := BuildCommand(tt.Opts)
			assert.NoError(t, err)
			assert.Equal(t, tt.Want, strings.Join(args, " "))
		})
	}
}

// cpu: Intel(R) Xeon(R) Platinum 8260 CPU @ 2.40GHz
// BenchmarkBuildCommand-8   	  394146	      3084 ns/op
// BenchmarkXxx-8            	 3933902	       281.4 ns/op
// PASS
func BenchmarkBuildCommand(b *testing.B) {

	c := []Opt{WithMode("singleton"),
		WithFscacheDriver("fs_cache_dir"),
		WithFscacheThreads(4),
		WithAPISock("/dummy/apisock"),
		WithUpgrade()}

	for n := 0; n < b.N; n++ {
		_, err := BuildCommand(c)
		assert.Nil(b, err)
	}
}
