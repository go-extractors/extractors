// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Command sampletool-plugin-testsite serves the test extractor. It exists so the
// host test suite can exercise a real plugin process.
package main

import (
	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/internal/testplugin"
)

// serve is a seam, as in the real plugins.
var serve = extractor.Serve

func main() {
	serve(testplugin.New())
}
