// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package testplugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
)

func TestInfo(t *testing.T) {
	info, err := New().Info()
	if err != nil || info.Name != Name {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	t.Setenv(EnvFailInfo, "1")
	if _, err := New().Info(); err == nil {
		t.Fatal("the failure switch was ignored")
	}
}

func TestMatch(t *testing.T) {
	e := New()
	for _, u := range []string{"http://127.0.0.1:8080/x", "http://localhost/x", "http://[::1]/x"} {
		claim, err := e.Match(u)
		if err != nil || !claim.Handles || claim.Playlist {
			t.Errorf("Match(%q) = %+v, %v", u, claim, err)
		}
	}
	if claim, _ := e.Match("http://127.0.0.1/list/creator"); !claim.Playlist {
		t.Errorf("a /list URL must be claimed as a playlist: %+v", claim)
	}
	if claim, _ := e.Match("https://example.com/x"); claim.Handles {
		t.Error("a foreign host was claimed")
	}
	if claim, _ := e.Match("://"); claim.Handles {
		t.Error("an unparsable URL was claimed")
	}
}

func TestMatchSwitches(t *testing.T) {
	t.Setenv(EnvMatchAll, "1")
	if claim, err := New().Match("https://example.com/x"); err != nil || !claim.Handles {
		t.Errorf("match-all = %+v, %v", claim, err)
	}
	t.Setenv(EnvFailMatch, "1")
	if _, err := New().Match("http://127.0.0.1/x"); err == nil {
		t.Fatal("the failure switch was ignored")
	}
}

func TestExtract(t *testing.T) {
	want := media.Media{ID: "1", Title: "T", Formats: []media.Format{
		{ID: "f", URL: "https://cdn/v.mp4", Ext: "mp4", Protocol: media.ProtocolHTTP, Height: 720},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()
	got, err := New().Extract(extractor.Request{URL: srv.URL, HTTP: httpx.Config{UserAgent: "x"}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.Title != "T" || got.Site != Name || got.WebpageURL != srv.URL {
		t.Fatalf("media = %+v", got)
	}
}

func TestExtractSwitches(t *testing.T) {
	t.Setenv(EnvNoFormats, "1")
	m, err := New().Extract(extractor.Request{URL: "http://127.0.0.1:1/x"})
	if err != nil || len(m.Formats) != 0 {
		t.Fatalf("no-formats = %+v, %v", m, err)
	}
	t.Setenv(EnvNil, "1")
	if m, err := New().Extract(extractor.Request{URL: "x"}); m != nil || err != nil {
		t.Fatalf("nil switch = %+v, %v", m, err)
	}
}

func TestExtractSlow(t *testing.T) {
	t.Setenv(EnvSlow, "10ms")
	t.Setenv(EnvNil, "1")
	start := time.Now()
	if _, err := New().Extract(extractor.Request{URL: "x"}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if time.Since(start) < 10*time.Millisecond {
		t.Fatal("the sleep switch was ignored")
	}
	t.Setenv(EnvSlow, "not-a-duration")
	if _, err := New().Extract(extractor.Request{URL: "x"}); err == nil {
		t.Fatal("a bad duration was accepted")
	}
}

func TestExtractErrors(t *testing.T) {
	if _, err := New().Extract(extractor.Request{
		URL: "http://127.0.0.1:1/x", HTTP: httpx.Config{Proxy: "http://%zz"},
	}); err == nil {
		t.Fatal("a bad proxy was accepted")
	}
	if _, err := New().Extract(extractor.Request{URL: "http://127.0.0.1:1/x"}); err == nil {
		t.Fatal("an unreachable page was accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	_, err := New().Extract(extractor.Request{URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "not a JSON media") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractIgnoreHTTPSwitch(t *testing.T) {
	t.Setenv(EnvIgnoreHTTP, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(media.Media{ID: "1", Title: "T"})
	}))
	defer srv.Close()
	// A proxy that cannot be parsed would fail if the config were honoured.
	m, err := New().Extract(extractor.Request{URL: srv.URL, HTTP: httpx.Config{Proxy: "http://%zz"}})
	if err != nil || m.Title != "T" {
		t.Fatalf("Extract = %+v, %v", m, err)
	}
}

func TestList(t *testing.T) {
	want := extractor.Playlist{ID: "c", Title: "Creator", Entries: []extractor.Entry{
		{URL: "http://127.0.0.1/videos/1", ID: "1", Title: "One", Duration: time.Minute},
		{URL: "http://127.0.0.1/shorts/2", ID: "2", Title: "Two", Kind: "short"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()
	got, err := New().List(extractor.Request{URL: srv.URL + "/list/c"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Title != "Creator" || len(got.Entries) != 2 || got.WebpageURL != srv.URL+"/list/c" {
		t.Fatalf("playlist = %+v", got)
	}
}

func TestListRefusesASingleVideo(t *testing.T) {
	_, err := New().List(extractor.Request{URL: "http://127.0.0.1/videos/1"})
	if !extractor.MatchError(err, extractor.ErrNotAPlaylist) {
		t.Fatalf("err = %v, want ErrNotAPlaylist", err)
	}
}

func TestListSwitchesAndErrors(t *testing.T) {
	t.Setenv(EnvEmptyList, "1")
	pl, err := New().List(extractor.Request{URL: "http://127.0.0.1/list/c"})
	if err != nil || len(pl.Entries) != 0 {
		t.Fatalf("empty list = %+v, %v", pl, err)
	}
	t.Setenv(EnvEmptyList, "")
	t.Setenv(EnvFailMatch, "1")
	if _, err := New().List(extractor.Request{URL: "http://127.0.0.1/list/c"}); err == nil {
		t.Fatal("a failing Match was ignored")
	}
	t.Setenv(EnvFailMatch, "")
	if _, err := New().List(extractor.Request{
		URL: "http://127.0.0.1:1/list/c", HTTP: httpx.Config{Proxy: "http://%zz"},
	}); err == nil {
		t.Fatal("a bad proxy was accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	if _, err := New().List(extractor.Request{URL: srv.URL + "/list/c"}); err == nil {
		t.Fatal("a non-JSON listing was accepted")
	}
}

// TestSiteIsReadFromTheBinaryName proves several copies of this plugin,
// installed side by side, are distinct plugins to a host.
func TestSiteIsReadFromTheBinaryName(t *testing.T) {
	old := os.Args[0]
	defer func() { os.Args[0] = old }()
	for _, tc := range []struct{ argv0, want string }{
		{filepath.Join("dir", "sampletool-plugin-alpha"), "alpha"},
		{filepath.Join("dir", "sampletool-plugin-alpha.exe"), "alpha"},
		{filepath.Join("dir", "othertool-plugin-beta"), "beta"},
		{filepath.Join("dir", "testplugin.test"), Name},
	} {
		os.Args[0] = tc.argv0
		if got := site(); got != tc.want {
			t.Errorf("site() with argv[0] %q = %q, want %q", tc.argv0, got, tc.want)
		}
	}
}

func TestPriorityForReadsTheEnvironment(t *testing.T) {
	t.Setenv(EnvPriority, "alpha=-2,beta=7,gamma=not-a-number,noequalsign")
	for _, tc := range []struct {
		site string
		want int
	}{
		{"alpha", -2},
		{"beta", 7},
		{"gamma", 0},       // a value that is not a number
		{"noequalsign", 0}, // a pair that is not a pair
		{"delta", 0},       // a site the variable does not name
	} {
		if got := priorityFor(tc.site); got != tc.want {
			t.Errorf("priorityFor(%q) = %d, want %d", tc.site, got, tc.want)
		}
	}
	t.Setenv(EnvPriority, "")
	if got := priorityFor("alpha"); got != 0 {
		t.Errorf("priorityFor without the variable = %d, want 0", got)
	}
}

// TestInfoReportsTheSiteAndItsPriority ties the two together: what the host
// reads out of Info is what the binary name and the environment said.
func TestInfoReportsTheSiteAndItsPriority(t *testing.T) {
	old := os.Args[0]
	defer func() { os.Args[0] = old }()
	os.Args[0] = filepath.Join("dir", "sampletool-plugin-alpha")
	t.Setenv(EnvPriority, "alpha=-5")
	info, err := New().Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "alpha" || info.Priority != -5 {
		t.Fatalf("Info = %+v, want alpha at -5", info)
	}
}

func TestListHonoursTheSleepSwitch(t *testing.T) {
	t.Setenv(EnvSlow, "not-a-duration")
	if _, err := New().List(extractor.Request{URL: "http://127.0.0.1/list/c"}); err == nil {
		t.Fatal("a bad duration was accepted")
	}
}
