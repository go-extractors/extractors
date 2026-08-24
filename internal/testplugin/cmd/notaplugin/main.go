// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Command notaplugin is an executable that is deliberately not an extractor
// plugin: it prints no handshake and exits with a failure, which is what a
// host must survive when a stray binary sits under the plugin prefix.
//
// When TESTPLUGIN_MARKER names a file, it creates that file before exiting, so
// a test can tell whether the host started this binary at all.
package main

import (
	"os"

	"github.com/go-extractors/extractors/internal/testplugin"
)

// Seams, so the whole of main is exercised in process.
var (
	getenv    = os.Getenv
	writeFile = os.WriteFile
	exit      = os.Exit
)

// run returns the exit status: never zero, because a handshake never happened.
func run() int {
	if p := getenv(testplugin.EnvMarker); p != "" {
		if err := writeFile(p, []byte("launched\n"), 0o644); err != nil {
			return 2
		}
	}
	return 1
}

func main() { exit(run()) }
