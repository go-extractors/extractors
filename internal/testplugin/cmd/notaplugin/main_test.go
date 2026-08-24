// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWithoutAMarkerJustFails(t *testing.T) {
	old := getenv
	defer func() { getenv = old }()
	getenv = func(string) string { return "" }
	if got := run(); got != 1 {
		t.Fatalf("run() = %d, want 1", got)
	}
}

func TestRunLeavesTheMarkerBehind(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "was-launched")
	old := getenv
	defer func() { getenv = old }()
	getenv = func(string) string { return marker }
	if got := run(); got != 1 {
		t.Fatalf("run() = %d, want 1", got)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the marker was not written: %v", err)
	}
	if string(b) != "launched\n" {
		t.Fatalf("marker = %q", b)
	}
}

func TestRunReportsAnUnwritableMarker(t *testing.T) {
	oldEnv, oldWrite := getenv, writeFile
	defer func() { getenv, writeFile = oldEnv, oldWrite }()
	getenv = func(string) string { return "somewhere" }
	writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only") }
	if got := run(); got != 2 {
		t.Fatalf("run() = %d, want 2", got)
	}
}

func TestMainExitsWithRunsStatus(t *testing.T) {
	oldExit, oldEnv := exit, getenv
	defer func() { exit, getenv = oldExit, oldEnv }()
	getenv = func(string) string { return "" }
	got := 0
	exit = func(code int) { got = code }
	main()
	if got != 1 {
		t.Fatalf("main exited with %d, want 1", got)
	}
}
