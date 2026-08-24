// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package main

import (
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/go-extractors/extractors/extractor"
)

func TestMainServesUnderTheWrongName(t *testing.T) {
	old := serve
	defer func() { serve = old }()
	var got *goplugin.ServeConfig
	serve = func(cfg *goplugin.ServeConfig) { got = cfg }
	main()
	if got == nil {
		t.Fatal("main served nothing")
	}
	if got.HandshakeConfig != extractor.Handshake {
		t.Errorf("handshake = %+v, want the framework's", got.HandshakeConfig)
	}
	if _, ok := got.Plugins[extractor.PluginName]; ok {
		t.Errorf("the plugin was dispensed as %q after all", extractor.PluginName)
	}
	if _, ok := got.Plugins[DispensedAs]; !ok {
		t.Errorf("nothing was dispensed as %q: %v", DispensedAs, got.Plugins)
	}
}
