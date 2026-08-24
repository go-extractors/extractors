// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"os"
	"path/filepath"
	"testing"
)

// stage replaces a package-level seam and returns the function that puts it
// back, so a test can watch the code take a path a healthy machine never does.
func stage[T any](seam *T, replacement T) func() {
	old := *seam
	*seam = replacement
	return func() { *seam = old }
}

// testTool is the tool name the internal tests run under. Nothing in the
// package knows an application name, so every test has to name one.
const testTool = Tool("sampletool")

// testHost builds a Host for testTool, failing the test if the tool name is
// somehow refused.
func testHost(t *testing.T) *Host {
	t.Helper()
	h, err := NewHost(testTool, nil, nil, "")
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	return h
}

func TestIndexEntryServes(t *testing.T) {
	e := indexEntry{
		Hosts:    []string{"example.com"},
		Patterns: []string{`(?i)^(?:[a-z0-9-]+\.)*samplesite[0-9]*\.(?:com|test)$`, "(("},
	}
	// Bare host, a subdomain of it, and a numbered sibling on the other
	// top-level domain the pattern allows.
	yes := []string{"samplesite.com", "www.samplesite.com", "samplesite42.test", "example.com", "www.example.com"}
	for _, h := range yes {
		if !e.serves(h) {
			t.Errorf("serves(%q) = false", h)
		}
	}
	// A host that merely ends with the name, and one that puts it in front
	// of somebody else's domain, are not this site.
	for _, h := range []string{"example.org", "notsamplesite.com", "samplesite.com.evil.test", ""} {
		if e.serves(h) {
			t.Errorf("serves(%q) = true", h)
		}
	}
}

func TestIndexEntryFreshness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, testTool.BinaryPrefix()+"x")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	e := indexEntry{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
	if !e.fresh(path) {
		t.Fatal("a untouched binary was called stale")
	}
	if err := os.WriteFile(path, []byte("rebuilt binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if e.fresh(path) {
		t.Fatal("a rebuilt binary must invalidate its entry")
	}
	if e.fresh(filepath.Join(dir, "gone")) {
		t.Fatal("a missing binary is not fresh")
	}
}

func TestIndexCandidates(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, testTool.BinaryPrefix()+"good")
	other := filepath.Join(dir, testTool.BinaryPrefix()+"other")
	for _, p := range []string{good, other} {
		if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	idx := newIndex()
	for _, p := range []string{good, other} {
		fi, _ := os.Stat(p)
		hosts := []string{"good.example"}
		if p == other {
			hosts = []string{"other.example"}
		}
		idx.Entries[p] = indexEntry{Size: fi.Size(), ModTime: fi.ModTime().UnixNano(), Hosts: hosts}
	}
	paths := []string{good, other}
	got := idx.candidates(paths, "https://good.example/videos/1")
	if len(got) != 1 || got[0] != good {
		t.Fatalf("candidates = %v, want just the matching plugin", got)
	}
	if got := idx.candidates(paths, "https://nobody.example/x"); got != nil {
		t.Fatalf("candidates = %v, want none", got)
	}
	if got := idx.candidates(paths, "://"); got != nil {
		t.Fatalf("an unparsable URL yielded %v", got)
	}
	// An unknown binary is never a candidate.
	if got := idx.candidates([]string{filepath.Join(dir, testTool.BinaryPrefix()+"unknown")}, "https://good.example/x"); got != nil {
		t.Fatalf("candidates = %v", got)
	}
}

func TestHostOf(t *testing.T) {
	if got := hostOf(" https://Example.COM:8080/x "); got != "Example.COM" {
		t.Errorf("hostOf = %q", got)
	}
	if got := hostOf("://"); got != "" {
		t.Errorf("hostOf = %q, want empty", got)
	}
}

func TestIndexPersistence(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, testTool.BinaryPrefix()+"x")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache", "plugins.json")
	h := testHost(t)
	h.SetCachePath(cache)
	h.index = newIndex()
	h.remember(bin, Info{Name: "x", Hosts: []string{"x.example"}, HostPatterns: []string{"^x$"}})
	h.remember(bin, Info{Name: "x", Hosts: []string{"x.example"}, HostPatterns: []string{"^x$"}})
	if !h.index.dirty {
		t.Fatal("the first remember must mark the index dirty")
	}
	h.saveIndex(h.index)

	back := h.loadIndex()
	e, ok := back.Entries[bin]
	if !ok || e.Name != "x" || len(e.Patterns) != 1 {
		t.Fatalf("reloaded entry = %+v, %v", e, ok)
	}
	if !e.fresh(bin) {
		t.Fatal("the reloaded entry does not match the binary")
	}

	// Saving a clean index writes nothing new, and a missing binary is not
	// recorded at all.
	back.dirty = false
	h.saveIndex(back)
	h.remember(filepath.Join(dir, "gone"), Info{Name: "gone"})
	if _, ok := h.index.Entries[filepath.Join(dir, "gone")]; ok {
		t.Fatal("a missing binary was recorded")
	}
}

func TestIndexIsOptional(t *testing.T) {
	h := testHost(t)
	h.SetCachePath("")
	if got := h.loadIndex(); len(got.Entries) != 0 {
		t.Fatalf("a disabled cache returned %v", got.Entries)
	}
	h.saveIndex(nil) // must not panic
	h.index = nil
	h.remember("whatever", Info{}) // must not panic
}

func TestLoadIndexIgnoresRubbish(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "plugins.json")
	if err := os.WriteFile(cache, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t)
	h.SetCachePath(cache)
	if got := h.loadIndex(); len(got.Entries) != 0 {
		t.Fatalf("a corrupt cache yielded %v", got.Entries)
	}
	if err := os.WriteFile(cache, []byte(`{"plugins":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.loadIndex(); got.Entries == nil {
		t.Fatal("loadIndex must always return a usable index")
	}
}

func TestSaveIndexSurvivesAnUnusableCachePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t)
	h.SetCachePath(filepath.Join(blocker, "sub", "plugins.json"))
	h.index = newIndex()
	h.index.Entries["x"] = indexEntry{Name: "x"}
	h.index.dirty = true
	h.saveIndex(h.index) // must not panic, and must not fail the run
	if _, err := os.Stat(filepath.Join(blocker, "sub")); err == nil {
		t.Fatal("the cache directory was created under a regular file")
	}
}

func TestCandidatesRankByPriority(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	idx := newIndex()
	// Discovery order is alphabetical, so the catch-all comes first by name
	// and must still be tried last.
	for _, spec := range []struct {
		name     string
		priority int
	}{{"aaa-generic", -100}, {"mmm-site", 0}, {"zzz-special", 10}} {
		p := filepath.Join(dir, testTool.BinaryPrefix()+spec.name)
		if err := os.WriteFile(p, []byte(spec.name), 0o755); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(p)
		idx.Entries[p] = indexEntry{
			Size: fi.Size(), ModTime: fi.ModTime().UnixNano(),
			Hosts: []string{"example.com"}, Priority: spec.priority,
		}
		paths = append(paths, p)
	}
	got := idx.candidates(paths, "https://example.com/x")
	want := []string{
		testTool.BinaryPrefix() + "zzz-special",
		testTool.BinaryPrefix() + "mmm-site",
		testTool.BinaryPrefix() + "aaa-generic",
	}
	if len(got) != 3 {
		t.Fatalf("candidates = %v", got)
	}
	for i, w := range want {
		if filepath.Base(got[i]) != w {
			t.Fatalf("candidate %d = %s, want %s", i, filepath.Base(got[i]), w)
		}
	}
}

// TestCandidatesKeepDiscoveryOrderOnATie checks the tie-break: two plugins that
// declare the same priority stay in the order they were discovered in, so a
// local build still shadows an installed one.
func TestCandidatesKeepDiscoveryOrderOnATie(t *testing.T) {
	dir := t.TempDir()
	idx := newIndex()
	var paths []string
	for _, name := range []string{"first", "second"} {
		p := filepath.Join(dir, testTool.BinaryPrefix()+name)
		if err := os.WriteFile(p, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		idx.Entries[p] = indexEntry{
			Size: fi.Size(), ModTime: fi.ModTime().UnixNano(),
			Hosts: []string{"example.com"}, Priority: 7,
		}
		paths = append(paths, p)
	}
	// Hand them over in each order in turn: the answer must follow the
	// order given, not the names.
	for _, order := range [][]string{{paths[0], paths[1]}, {paths[1], paths[0]}} {
		got := idx.candidates(order, "https://example.com/x")
		if len(got) != 2 {
			t.Fatalf("candidates = %v, want both", got)
		}
		if got[0] != order[0] || got[1] != order[1] {
			t.Fatalf("candidates = %v, want %v", got, order)
		}
	}
}
