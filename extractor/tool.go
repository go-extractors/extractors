// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tool is the name of the host application a set of plugins belongs to.
//
// The framework itself knows no application name. Everything that would
// otherwise have to be one is derived from this single value: the file-name
// prefix of a plugin executable, the environment variable that overrides
// discovery, the per-user directory plugins are installed in, and the file the
// plugin index is cached in. A host named "example" therefore looks for
// "example-plugin-*" binaries, honours $EXAMPLE_PLUGIN_PATH, reads
// <user config dir>/example/plugins and caches
// <user cache dir>/example/plugins.json.
//
// The name is not a free-form string: it becomes a file name, a directory name
// and an environment variable name on three operating systems, so it is
// restricted to ASCII letters, digits, '-' and '_'. Validate rejects anything
// else, which is also what keeps a name such as ".." from reaching
// filepath.Join.
type Tool string

// ErrToolName reports a tool name that cannot be turned into a file name, a
// directory name and an environment variable name.
var ErrToolName = errors.New("extractor: invalid tool name")

// Validate reports whether t may be used to derive paths and names.
//
// An empty name is refused rather than defaulted: a shared framework has no
// application name to fall back to, and silently picking one would put a
// caller's plugins and index in a directory it never asked for.
func (t Tool) Validate() error {
	if t == "" {
		return fmt.Errorf("%w: the tool name is empty", ErrToolName)
	}
	for _, r := range string(t) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("%w: %q contains %q, want only ASCII letters, digits, '-' and '_'",
				ErrToolName, string(t), string(r))
		}
	}
	return nil
}

// BinaryPrefix is the file-name prefix the host looks for when discovering
// plugin executables: "<tool>-plugin-".
func (t Tool) BinaryPrefix() string { return string(t) + "-plugin-" }

// EnvPluginPath is the environment variable that overrides plugin discovery,
// os.PathListSeparator separated: "<TOOL>_PLUGIN_PATH".
func (t Tool) EnvPluginPath() string {
	return strings.ToUpper(strings.ReplaceAll(string(t), "-", "_")) + "_PLUGIN_PATH"
}

// Seams for the OS lookups, so a test can stage the failure of a call that
// does not fail on a healthy machine.
var (
	osExecutable    = os.Executable
	osUserConfigDir = os.UserConfigDir
	osUserCacheDir  = os.UserCacheDir
	osGetwd         = os.Getwd
)

// PluginDirs lists where plugins are looked up, most specific first: the
// directories named by EnvPluginPath, then the directory of the running binary
// and its plugins/ subdirectory, then the per-user install directory, then
// bin/ under the working directory.
func (t Tool) PluginDirs() []string {
	var dirs []string
	if p := os.Getenv(t.EnvPluginPath()); p != "" {
		dirs = append(dirs, filepath.SplitList(p)...)
	}
	if exe, err := osExecutable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs, exeDir, filepath.Join(exeDir, "plugins"))
	}
	if cfg, err := osUserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(cfg, string(t), "plugins"))
	}
	if wd, err := osGetwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "bin"))
	}
	return dirs
}

// CachePath is where the plugin index lives, per user. It is empty when the
// operating system names no cache directory, which disables the index.
func (t Tool) CachePath() string {
	dir, err := osUserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, string(t), "plugins.json")
}
