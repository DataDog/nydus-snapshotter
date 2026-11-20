/*
 * Copyright (c) 2022. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package daemonconfig

import (
	"encoding/json"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	"github.com/containerd/nydus-snapshotter/pkg/auth"
)

const CacheDir string = "cachedir"

// Used when nydusd works as a FUSE daemon or vhost-user-fs backend
type FuseDaemonConfig struct {
	Device          *DeviceConfig `json:"device"`
	Mode            string        `json:"mode"`
	DigestValidate  bool          `json:"digest_validate"`
	IOStatsFiles    bool          `json:"iostats_files,omitempty"`
	EnableXattr     bool          `json:"enable_xattr,omitempty"`
	AccessPattern   bool          `json:"access_pattern,omitempty"`
	LatestReadFiles bool          `json:"latest_read_files,omitempty"`
	AmplifyIo       *int          `json:"amplify_io,omitempty"`
	FSPrefetch      `json:"fs_prefetch,omitempty"`
	// (experimental) The nydus daemon could cache more data to increase hit ratio when enabled the warmup feature.
	Warmup uint64 `json:"warmup,omitempty"`
	// UID/GID mapping tuple (internal, external, range) for user-namespace
	// remap-ids support. Serialized as a 3-element JSON array to match nydusd's
	// `RafsConfigV2.id_mapping` tuple. Omitted when no mapping is configured.
	IDMapping *[3]uint32 `json:"id_mapping,omitempty"`
	// Options the snapshotter does not handle, e.g. the nbd section, kept as is for nydusd.
	Extra map[string]json.RawMessage `json:"-"`
}

// fuseConfigKeys are the JSON keys of the options FuseDaemonConfig handles.
var fuseConfigKeys = func() map[string]bool {
	keys := make(map[string]bool)
	t := reflect.TypeOf(FuseDaemonConfig{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

// UnmarshalJSON keeps the options FuseDaemonConfig does not handle in Extra.
func (c *FuseDaemonConfig) UnmarshalJSON(data []byte) error {
	type plain FuseDaemonConfig
	if err := json.Unmarshal(data, (*plain)(c)); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	for key, value := range all {
		if !fuseConfigKeys[key] {
			if c.Extra == nil {
				c.Extra = make(map[string]json.RawMessage)
			}
			c.Extra[key] = value
		}
	}
	return nil
}

// MarshalJSON writes the options of Extra back.
func (c FuseDaemonConfig) MarshalJSON() ([]byte, error) {
	type plain FuseDaemonConfig
	b, err := json.Marshal(plain(c))
	if err != nil || len(c.Extra) == 0 {
		return b, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for key, value := range c.Extra {
		if !fuseConfigKeys[key] {
			all[key] = value
		}
	}
	return json.Marshal(all)
}

// Control how to perform prefetch from file system layer
type FSPrefetch struct {
	Enable         bool `json:"enable"`
	PrefetchAll    bool `json:"prefetch_all"`
	ThreadsCount   int  `json:"threads_count,omitempty"`
	MergingSize    int  `json:"merging_size,omitempty"`
	BandwidthRate  int  `json:"bandwidth_rate,omitempty"`
	StreamPrefetch bool `json:"stream_prefetch,omitempty"`
}

// Load fuse daemon configuration from template file
func LoadFuseConfig(p string) (*FuseDaemonConfig, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, errors.Wrapf(err, "read FUSE configuration file %s", p)
	}
	var cfg FuseDaemonConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, errors.Wrapf(err, "unmarshal %s", p)
	}

	if cfg.Device == nil {
		return nil, errors.New("invalid fuse daemon configuration")
	}

	return &cfg, nil
}

func (c *FuseDaemonConfig) Supplement(host, repo, snapshotID string, params map[string]string) {
	if host != "" {
		c.Device.Backend.Config.Host = host
	}
	if repo != "" {
		c.Device.Backend.Config.Repo = repo
	}
	// Temporary fix while https://github.com/containerd/nydus-snapshotter/issues/712 is being addressed
	if snapshotID != "" {
		c.Device.ID = "/" + snapshotID
	}
	c.Device.Cache.Config.WorkDir = params[CacheDir]
}

func (c *FuseDaemonConfig) FillAuth(kc *auth.PassKeyChain) {
	if kc != nil {
		if kc.TokenBase() {
			c.Device.Backend.Config.RegistryToken = kc.Password
		} else {
			c.Device.Backend.Config.Auth = kc.ToBase64()
		}
	}
}

func (c *FuseDaemonConfig) StorageBackend() (string, *BackendConfig) {
	return c.Device.Backend.BackendType, &c.Device.Backend.Config
}

func (c *FuseDaemonConfig) SetIDMapping(m *[3]uint32) {
	c.IDMapping = m
}

func (c *FuseDaemonConfig) DumpString() (string, error) {
	return DumpConfigString(c)
}

func (c *FuseDaemonConfig) DumpFile(f string) error {
	if err := os.MkdirAll(path.Dir(f), 0755); err != nil {
		return err
	}
	return DumpConfigFile(c, f)
}
