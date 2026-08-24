// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Command wrongplugin completes the handshake like any plugin and then serves
// its extractor under a name the host never asks for. It is the one way a
// binary can be a well-behaved go-plugin server and still fail to dispense an
// Extractor, which is the case extractor.Host.Open has to report.
package main

import (
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/internal/testplugin"
)

// DispensedAs is deliberately not extractor.PluginName.
const DispensedAs = "not-the-extractor"

// serve is a seam, as in every plugin command.
var serve = goplugin.Serve

func main() {
	serve(&goplugin.ServeConfig{
		HandshakeConfig: extractor.Handshake,
		Plugins: map[string]goplugin.Plugin{
			DispensedAs: &extractor.ExtractorPlugin{Impl: testplugin.New()},
		},
	})
}
