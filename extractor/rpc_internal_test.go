// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/go-extractors/extractors/media"
)

// nopExtractor is an Extractor that is only ever used as an identity.
type nopExtractor struct{}

func (nopExtractor) Info() (Info, error)                   { return Info{}, nil }
func (nopExtractor) Match(string) (Claim, error)           { return Claim{}, nil }
func (nopExtractor) Extract(Request) (*media.Media, error) { return nil, nil }
func (nopExtractor) List(Request) (*Playlist, error)       { return nil, nil }

// TestServeConfiguresGoPlugin checks the whole body of a plugin's main: the
// framework handshake, and the caller's own implementation under the one name
// the host dispenses.
func TestServeConfiguresGoPlugin(t *testing.T) {
	var got *goplugin.ServeConfig
	defer stage(&serve, func(cfg *goplugin.ServeConfig) { got = cfg })()

	impl := &nopExtractor{}
	Serve(impl)

	if got == nil {
		t.Fatal("Serve handed go-plugin nothing")
	}
	if got.HandshakeConfig != Handshake {
		t.Errorf("handshake = %+v, want %+v", got.HandshakeConfig, Handshake)
	}
	p, ok := got.Plugins[PluginName].(*ExtractorPlugin)
	if !ok {
		t.Fatalf("plugins = %v, want an *ExtractorPlugin under %q", got.Plugins, PluginName)
	}
	if p.Impl != impl {
		t.Errorf("served %v, want the caller's implementation", p.Impl)
	}
	// The plugin is the only thing served: a host asking for anything else
	// must fail rather than get a surprise.
	if len(got.Plugins) != 1 {
		t.Errorf("plugins = %v, want exactly one", got.Plugins)
	}
}
