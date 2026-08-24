// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolValidateAcceptsAPortableName(t *testing.T) {
	for _, name := range []Tool{"a", "sampletool", "my-tool_2", "TOOL", "0"} {
		if err := name.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}
}

func TestToolValidateRefusesAnEmptyName(t *testing.T) {
	err := Tool("").Validate()
	if !errors.Is(err, ErrToolName) {
		t.Fatalf("Validate(\"\") = %v, want ErrToolName", err)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the error does not say the name is empty: %v", err)
	}
}

func TestToolValidateRefusesWhatCannotBeAFileName(t *testing.T) {
	// A separator or a dot segment would escape the directory the name is
	// meant to create; a space or a non-ASCII rune would not survive an
	// environment variable name.
	for _, name := range []Tool{"..", ".", "a/b", `a\b`, "my tool", "café", "a.b", "a:b", "a*"} {
		err := name.Validate()
		if !errors.Is(err, ErrToolName) {
			t.Errorf("Validate(%q) = %v, want ErrToolName", name, err)
		}
	}
}

// TestToolDerivations pins the rule every name is turned into paths by: the
// name itself, and nothing else, decides the binary prefix, the environment
// variable, the install directory and the cache file.
func TestToolDerivations(t *testing.T) {
	const tool = Tool("sampletool")
	if got := tool.BinaryPrefix(); got != "sampletool-plugin-" {
		t.Errorf("BinaryPrefix = %q", got)
	}
	if got := tool.EnvPluginPath(); got != "SAMPLETOOL_PLUGIN_PATH" {
		t.Errorf("EnvPluginPath = %q", got)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	if want := filepath.Join(cache, "sampletool", "plugins.json"); tool.CachePath() != want {
		t.Errorf("CachePath = %q, want %q", tool.CachePath(), want)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	want := filepath.Join(cfg, "sampletool", "plugins")
	found := false
	for _, d := range tool.PluginDirs() {
		if d == want {
			found = true
		}
	}
	if !found {
		t.Errorf("PluginDirs = %v, want it to hold %q", tool.PluginDirs(), want)
	}
}

// A dash is legal in a file name and not in an environment variable name, so
// it is the one character the two spellings disagree on.
func TestToolEnvPluginPathSpellsADashAsAnUnderscore(t *testing.T) {
	if got := Tool("my-tool").EnvPluginPath(); got != "MY_TOOL_PLUGIN_PATH" {
		t.Fatalf("EnvPluginPath = %q, want MY_TOOL_PLUGIN_PATH", got)
	}
}

func TestToolPluginDirsHonoursTheEnvironment(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Setenv(testTool.EnvPluginPath(), a+string(os.PathListSeparator)+b)
	dirs := testTool.PluginDirs()
	if len(dirs) < 2 || dirs[0] != a || dirs[1] != b {
		t.Fatalf("PluginDirs = %v, want %s and %s first", dirs, a, b)
	}
	t.Setenv(testTool.EnvPluginPath(), "")
	if got := testTool.PluginDirs(); len(got) == 0 {
		t.Fatal("PluginDirs is empty without the environment variable")
	}
}

// TestToolPluginDirsWithoutAnyOSAnswer stages every operating-system lookup
// failing at once: what is left is exactly what the environment named.
func TestToolPluginDirsWithoutAnyOSAnswer(t *testing.T) {
	boom := errors.New("staged failure")
	defer stage(&osExecutable, func() (string, error) { return "", boom })()
	defer stage(&osUserConfigDir, func() (string, error) { return "", boom })()
	defer stage(&osGetwd, func() (string, error) { return "", boom })()

	dir := t.TempDir()
	t.Setenv(testTool.EnvPluginPath(), dir)
	if got := testTool.PluginDirs(); len(got) != 1 || got[0] != dir {
		t.Fatalf("PluginDirs = %v, want only %q", got, dir)
	}
	t.Setenv(testTool.EnvPluginPath(), "")
	if got := testTool.PluginDirs(); len(got) != 0 {
		t.Fatalf("PluginDirs = %v, want nothing at all", got)
	}
}

func TestToolCachePathWithoutACacheDirectory(t *testing.T) {
	defer stage(&osUserCacheDir, func() (string, error) { return "", errors.New("staged failure") })()
	if got := testTool.CachePath(); got != "" {
		t.Fatalf("CachePath = %q, want empty when the system names no cache directory", got)
	}
}

func TestNewHostRefusesAnInvalidTool(t *testing.T) {
	for _, name := range []Tool{"", "../escape"} {
		h, err := NewHost(name, nil, nil, "")
		if !errors.Is(err, ErrToolName) {
			t.Errorf("NewHost(%q) = %v, %v, want ErrToolName", name, h, err)
		}
		if h != nil {
			t.Errorf("NewHost(%q) returned a Host anyway", name)
		}
	}
}

func TestNewHostDerivesEverythingFromTheToolName(t *testing.T) {
	h, err := NewHost(testTool, []string{"first"}, nil, "trace")
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	if h.Tool() != testTool {
		t.Errorf("Tool = %q", h.Tool())
	}
	if h.dirs[0] != "first" {
		t.Errorf("the caller's directory is not searched first: %v", h.dirs)
	}
	if h.cachePath != testTool.CachePath() {
		t.Errorf("cachePath = %q, want %q", h.cachePath, testTool.CachePath())
	}
	if h.logger.Name() != string(testTool) {
		t.Errorf("logger name = %q, want %q", h.logger.Name(), testTool)
	}
	if !h.logger.IsTrace() {
		t.Error("the log level was not honoured")
	}
	// An unknown level is not a silent one: it means "error".
	h2, err := NewHost(testTool, nil, nil, "not-a-level")
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	if h2.logger.IsInfo() || !h2.logger.IsError() {
		t.Error("an unknown level did not fall back to error")
	}
}
