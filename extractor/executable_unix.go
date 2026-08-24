// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build !windows

package extractor

import "io/fs"

// isExecutable reports whether path names a program this system can start,
// which everywhere but Windows means an execute bit is set for somebody.
func isExecutable(_ string, fi fs.FileInfo) bool {
	return fi.Mode().Perm()&0o111 != 0
}
