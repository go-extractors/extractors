// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build windows

package extractor

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// isExecutable reports whether path names a program this system can start.
// Windows has no execute bit: the extension decides, and the only one a plugin
// can be launched from is ".exe".
func isExecutable(path string, _ fs.FileInfo) bool {
	return strings.EqualFold(filepath.Ext(path), ".exe")
}
