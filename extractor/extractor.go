// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package extractor is the contract between a host program and its plugins.
//
// A plugin is a standalone executable named "<tool>-plugin-<site>", where
// <tool> is the name of the host program it is installed for (see Tool). It
// serves one Extractor over hashicorp/go-plugin: it receives a page URL and
// returns the metadata and the list of downloadable formats, and it never
// writes a file. Downloading stays in the host, so a plugin only has to
// understand its site.
package extractor

import (
	"errors"
	"strings"
	"time"

	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
)

// Errors a plugin may return. They cross the RPC boundary as strings, so the
// host matches them with MatchError rather than with errors.Is.
var (
	// ErrUnsupported means the plugin does not handle this URL.
	ErrUnsupported = errors.New("extractor: unsupported URL")
	// ErrNotFound means the page is gone (404, deleted video).
	ErrNotFound = errors.New("extractor: video not found")
	// ErrGeoBlocked means the site refused to serve this region.
	ErrGeoBlocked = errors.New("extractor: geo-blocked")
	// ErrPaywalled means the video needs an account or a subscription.
	ErrPaywalled = errors.New("extractor: video is behind a paywall")
	// ErrNoFormats means the page parsed but exposed no playable source.
	ErrNoFormats = errors.New("extractor: no playable format found")
	// ErrWithheld means the page was served whole and the site kept the
	// player configuration out of it. Nothing is wrong with the page or with
	// the plugin: this session was decided not to get the sources, which is
	// what a soft rate limit looks like from the outside, and what an
	// unaccepted age or consent gate looks like too.
	ErrWithheld = errors.New("extractor: the site withheld the player configuration")
	// ErrNotAPlaylist means the URL is a single video, or the plugin does
	// not enumerate listings.
	ErrNotAPlaylist = errors.New("extractor: not a playlist")
	// ErrEmptyPlaylist means the listing parsed but held no video.
	ErrEmptyPlaylist = errors.New("extractor: playlist is empty")
)

// Claim is what a plugin answers when asked about a URL: whether it serves it
// at all, and whether that URL is one video or a list of them.
type Claim struct {
	Handles  bool
	Playlist bool
	// Kind labels what was recognised, for the log line: "video",
	// "creator", "shorts"… It is informational.
	Kind string
}

// Entry is one video announced by a listing. It carries just enough to name
// and skip it; the formats come from a later Extract on Entry.URL.
// The JSON names are the ones media.Media uses, because a caller piping a
// host's JSON output into a script should not have to learn two spellings for
// the same idea.
type Entry struct {
	URL      string        `json:"url"`
	ID       string        `json:"id"`
	Title    string        `json:"title,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
	// Kind distinguishes a full video from a short, an image or a plain
	// file. The host filters on it.
	Kind string `json:"kind,omitempty"`
	// Size is the announced byte size, when the listing states one.
	Size int64 `json:"size,omitempty"`
}

// Playlist is a list of videos: a creator page, a channel, a search.
type Playlist struct {
	ID         string  `json:"id"`
	Title      string  `json:"title,omitempty"`
	Uploader   string  `json:"uploader,omitempty"`
	WebpageURL string  `json:"webpage_url"`
	Entries    []Entry `json:"entries"`
}

// Info describes a plugin.
type Info struct {
	Name        string   // short site name, e.g. "samplesite"
	Version     string   // plugin version
	Description string   // one line
	Hosts       []string // example host names, for humans
	// HostPatterns are regular expressions matched against a URL's host
	// name. They let the host pick the right plugin from the URL alone,
	// instead of starting every plugin to ask. They are a filter, never a
	// decision: the chosen plugin still answers Match.
	HostPatterns []string
	// Priority orders plugins when several claim the same URL: the highest
	// wins, ties go to the first discovered. A plugin that knows one site
	// leaves it at zero; a catch-all that would otherwise steal every URL
	// declares a negative one, so it is only used when nothing better
	// answers.
	Priority int
	// MaxParallel is how many of this site's files may be fetched at once,
	// 0 meaning the plugin has no opinion and the host decides.
	//
	// It is a different limit from a format's MaxConns and the two are not
	// interchangeable: MaxConns bounds the connections spent on ONE file,
	// MaxParallel bounds how many files are in flight. A site can refuse
	// the first while allowing the second, and one here does — a second
	// range request on the same link is refused outright, while two whole
	// files download side by side without a complaint. Told only the first
	// limit, a host serialises the entire listing and takes hours over what
	// the site would have served in a fraction of that.
	MaxParallel int
}

// Request is one extraction job. The host passes its HTTP settings down so a
// plugin inherits the user's proxy, cookies, UA and retry policy.
type Request struct {
	URL  string
	HTTP httpx.Config
}

// Empty is the placeholder argument of no-argument RPC calls.
type Empty struct{}

// Extractor is what a plugin implements and what the host consumes.
type Extractor interface {
	// Info returns the plugin identity.
	Info() (Info, error)
	// Match reports whether this plugin claims the URL, and whether that
	// URL is a listing rather than a single video.
	Match(rawURL string) (Claim, error)
	// Extract turns a page URL into a Media with at least one Format.
	Extract(req Request) (*media.Media, error)
	// List enumerates the videos of a listing URL. A plugin that only
	// serves single videos returns ErrNotAPlaylist.
	List(req Request) (*Playlist, error)
}

// MatchError reports whether err is, or wraps, a sentinel with the same
// message as target. It survives the RPC round trip, where the concrete error
// type is lost.
func MatchError(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	if errors.Is(err, target) {
		return true
	}
	return contains(err.Error(), target.Error())
}

func contains(s, sub string) bool {
	return sub != "" && strings.Contains(s, sub)
}
