// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// indexEntry remembers what one plugin binary said about itself. Size and
// modification time make a rebuilt plugin invalidate its own entry.
type indexEntry struct {
	Size     int64    `json:"size"`
	ModTime  int64    `json:"mod_time"`
	Name     string   `json:"name"`
	Hosts    []string `json:"hosts,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
	Priority int      `json:"priority,omitempty"`
}

// pluginIndex maps a plugin path to what it serves, so the URL alone can name
// the plugin to start.
type pluginIndex struct {
	mu      sync.Mutex
	Entries map[string]indexEntry `json:"plugins"`
	dirty   bool
}

func newIndex() *pluginIndex { return &pluginIndex{Entries: map[string]indexEntry{}} }

func (h *Host) loadIndex() *pluginIndex {
	idx := newIndex()
	if h.cachePath == "" {
		return idx
	}
	b, err := os.ReadFile(h.cachePath)
	if err != nil {
		return idx
	}
	var stored pluginIndex
	if json.Unmarshal(b, &stored) != nil || stored.Entries == nil {
		return idx
	}
	idx.Entries = stored.Entries
	return idx
}

// remember records what a freshly started plugin serves.
func (h *Host) remember(path string, info Info) {
	if h.index == nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	h.index.mu.Lock()
	defer h.index.mu.Unlock()
	entry := indexEntry{
		Size: fi.Size(), ModTime: fi.ModTime().UnixNano(),
		Name: info.Name, Hosts: info.Hosts, Patterns: info.HostPatterns,
		Priority: info.Priority,
	}
	if old, ok := h.index.Entries[path]; ok && old.same(entry) {
		return
	}
	h.index.Entries[path] = entry
	h.index.dirty = true
}

// same compares two entries, which hold slices and so cannot use ==.
func (e indexEntry) same(o indexEntry) bool {
	return e.Size == o.Size && e.ModTime == o.ModTime && e.Name == o.Name &&
		e.Priority == o.Priority &&
		slices.Equal(e.Hosts, o.Hosts) && slices.Equal(e.Patterns, o.Patterns)
}

func (h *Host) saveIndex(idx *pluginIndex) {
	if h.cachePath == "" || idx == nil || !idx.dirty {
		return
	}
	idx.mu.Lock()
	// An index holds nothing but strings, integers and slices of strings,
	// so this cannot fail.
	b, _ := json.Marshal(idx)
	idx.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.cachePath), 0o755); err != nil {
		return
	}
	tmp := h.cachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, h.cachePath)
	}
}

// candidates returns the plugins whose remembered patterns match the URL's
// host, highest priority first. An unknown or stale binary is not a candidate:
// it will be asked in the fallback pass, which refreshes the index.
func (idx *pluginIndex) candidates(paths []string, rawURL string) []string {
	host := hostOf(rawURL)
	if host == "" || idx == nil {
		return nil
	}
	type candidate struct {
		path     string
		priority int
		order    int
	}
	var found []candidate
	for i, p := range paths {
		e, ok := idx.Entries[p]
		if !ok || !e.fresh(p) {
			continue
		}
		if e.serves(host) {
			found = append(found, candidate{p, e.Priority, i})
		}
	}
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].priority != found[b].priority {
			return found[a].priority > found[b].priority
		}
		return found[a].order < found[b].order
	})
	if len(found) == 0 {
		return nil // no candidate, not an empty one
	}
	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.path)
	}
	return out
}

func (e indexEntry) fresh(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() == e.Size && fi.ModTime().UnixNano() == e.ModTime
}

// serves reports whether the plugin declared this host name. Patterns are
// regular expressions; a broken one simply never matches.
func (e indexEntry) serves(host string) bool {
	for _, p := range e.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(host) {
			return true
		}
	}
	for _, h := range e.Hosts {
		if strings.EqualFold(h, host) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(h)) {
			return true
		}
	}
	return false
}

func hostOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
