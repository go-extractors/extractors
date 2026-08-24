// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package testplugin is an extractor plugin used by the test suites of the
// host and of the plugin API. It claims loopback URLs and expects the page to be a JSON
// media.Media, which lets a test server describe any extraction outcome.
//
// Its behaviour is steered by environment variables, because the host passes
// its own environment down to the plugin process:
//
//	TESTPLUGIN_FAIL_INFO=1   Info returns an error
//	TESTPLUGIN_FAIL_MATCH=1  Match returns an error
//	TESTPLUGIN_MATCH_ALL=1   Match claims every URL
//	TESTPLUGIN_NO_FORMATS=1  Extract returns a media without any format
//	TESTPLUGIN_NIL=1         Extract returns neither media nor error
//	TESTPLUGIN_SLOW=<dur>    Extract sleeps before answering
//	TESTPLUGIN_EMPTY_LIST=1  List returns a playlist without any entry
//	TESTPLUGIN_IGNORE_HTTP=1 Extract ignores the host's HTTP config, so a
//	                        deliberately broken one only fails on the host
package testplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
)

// Name is the site name this plugin reports.
const Name = "testsite"

// The switches, named so a test never spells one out.
const (
	EnvFailInfo   = "TESTPLUGIN_FAIL_INFO"
	EnvFailMatch  = "TESTPLUGIN_FAIL_MATCH"
	EnvMatchAll   = "TESTPLUGIN_MATCH_ALL"
	EnvNoFormats  = "TESTPLUGIN_NO_FORMATS"
	EnvNil        = "TESTPLUGIN_NIL"
	EnvSlow       = "TESTPLUGIN_SLOW"
	EnvEmptyList  = "TESTPLUGIN_EMPTY_LIST"
	EnvIgnoreHTTP = "TESTPLUGIN_IGNORE_HTTP"
	// EnvPriority is a comma-separated list of "<site>=<priority>" pairs.
	// Each installed copy of this plugin reads the one that names it, so
	// several copies can be told apart by the host that ranks them.
	EnvPriority = "TESTPLUGIN_PRIORITY"
	// EnvMarker is read by the notaplugin command, not by this plugin: it
	// names a file that command creates when it is started.
	EnvMarker = "TESTPLUGIN_MARKER"
)

// site is what this instance calls itself: the part of its own binary name
// after "-plugin-", so several copies installed side by side are distinct
// plugins to the host. It falls back to Name when the binary is not installed
// under a plugin name, which is what an in-process test sees.
func site() string {
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if i := strings.LastIndex(base, "-plugin-"); i >= 0 {
		return base[i+len("-plugin-"):]
	}
	return Name
}

// priorityFor reads a site's priority out of EnvPriority. A site the variable
// does not name, and a value that is not a number, both mean zero.
func priorityFor(s string) int {
	for _, pair := range strings.Split(os.Getenv(EnvPriority), ",") {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name != s {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// Extractor is the test implementation.
type Extractor struct{}

// New returns the test extractor.
func New() *Extractor { return &Extractor{} }

// Info reports a fixed identity, or an error on demand.
func (e *Extractor) Info() (extractor.Info, error) {
	if os.Getenv(EnvFailInfo) == "1" {
		return extractor.Info{}, errors.New("testplugin: Info failed on purpose")
	}
	name := site()
	return extractor.Info{
		Name:         name,
		Version:      "9.9.9",
		Description:  "loopback pages carrying a JSON media description",
		Hosts:        []string{"127.0.0.1", "localhost"},
		HostPatterns: []string{`^(?:127\.0\.0\.1|localhost|::1)$`},
		Priority:     priorityFor(name),
	}, nil
}

// Match claims loopback URLs, and treats a path under /list/ as a playlist.
func (e *Extractor) Match(rawURL string) (extractor.Claim, error) {
	if os.Getenv(EnvFailMatch) == "1" {
		return extractor.Claim{}, errors.New("testplugin: Match failed on purpose")
	}
	if os.Getenv(EnvMatchAll) == "1" {
		return extractor.Claim{Handles: true, Kind: "video"}, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return extractor.Claim{}, nil
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return extractor.Claim{}, nil
	}
	if strings.HasPrefix(u.EscapedPath(), "/list") {
		return extractor.Claim{Handles: true, Playlist: true, Kind: "list"}, nil
	}
	return extractor.Claim{Handles: true, Kind: "video"}, nil
}

// List fetches the page and decodes it as an extractor.Playlist, so a test
// server describes whatever listing it needs.
func (e *Extractor) List(req extractor.Request) (*extractor.Playlist, error) {
	claim, err := e.Match(req.URL)
	if err != nil {
		return nil, err
	}
	if !claim.Playlist {
		return nil, fmt.Errorf("%w: %s", extractor.ErrNotAPlaylist, req.URL)
	}
	if err := slowSwitch(); err != nil {
		return nil, err
	}
	if os.Getenv(EnvEmptyList) == "1" {
		return &extractor.Playlist{ID: "empty", Title: "nothing here"}, nil
	}
	body, err := e.fetch(req)
	if err != nil {
		return nil, err
	}
	var pl extractor.Playlist
	if err := json.Unmarshal(body, &pl); err != nil {
		return nil, fmt.Errorf("testplugin: page is not a JSON playlist: %w", err)
	}
	pl.WebpageURL = req.URL
	return &pl, nil
}

// fetch is the HTTP work shared by Extract and List.
func (e *Extractor) fetch(req extractor.Request) ([]byte, error) {
	cfg := req.HTTP
	if os.Getenv(EnvIgnoreHTTP) == "1" {
		cfg = httpx.Config{}
	}
	client, err := httpx.New(cfg)
	if err != nil {
		return nil, err
	}
	body, _, err := client.Get(context.Background(), req.URL, nil)
	return body, err
}

// Extract fetches the page and decodes it as a media.Media.
func (e *Extractor) Extract(req extractor.Request) (*media.Media, error) {
	if err := slowSwitch(); err != nil {
		return nil, err
	}
	if os.Getenv(EnvNil) == "1" {
		return nil, nil
	}
	if os.Getenv(EnvNoFormats) == "1" {
		return &media.Media{Site: site(), ID: "empty", Title: "no formats here"}, nil
	}
	body, err := e.fetch(req)
	if err != nil {
		return nil, err
	}
	var m media.Media
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("testplugin: page is not a JSON media: %w", err)
	}
	m.Site = site()
	m.WebpageURL = req.URL
	return &m, nil
}

// slowSwitch honours TESTPLUGIN_SLOW, so a test can watch the host give up on a
// plugin that takes too long.
func slowSwitch() error {
	d := os.Getenv(EnvSlow)
	if d == "" {
		return nil
	}
	wait, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("testplugin: bad TESTPLUGIN_SLOW: %w", err)
	}
	time.Sleep(wait)
	return nil
}
