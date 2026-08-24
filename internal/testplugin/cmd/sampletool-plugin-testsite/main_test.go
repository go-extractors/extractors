// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package main

import (
	"testing"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/internal/testplugin"
)

func TestMainServesTheTestExtractor(t *testing.T) {
	old := serve
	defer func() { serve = old }()
	var got extractor.Extractor
	serve = func(impl extractor.Extractor) { got = impl }
	main()
	if _, ok := got.(*testplugin.Extractor); !ok {
		t.Fatalf("main served %T, want *testplugin.Extractor", got)
	}
}
