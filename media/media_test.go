// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package media

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func sample() *Media {
	return &Media{
		Site: "samplesite", ID: "42", Title: "Some / Title", Uploader: "Ann",
		Duration: 90 * time.Second,
		Formats: []Format{
			{ID: "http-720p", URL: "u720", Ext: "mp4", Protocol: ProtocolHTTP, Height: 720, Quality: "720p", Size: 200},
			{ID: "hls-1080p", URL: "u1080hls", Ext: "ts", Protocol: ProtocolHLS, Height: 1080, Quality: "1080p"},
			{ID: "http-1080p", URL: "u1080", Ext: "mp4", Protocol: ProtocolHTTP, Height: 1080, Quality: "1080p"},
			{ID: "http-240p", URL: "u240", Ext: "mp4", Protocol: ProtocolHTTP, Height: 240, Quality: "240p"},
		},
	}
}

func TestSelect(t *testing.T) {
	cases := []struct {
		sel, want string
	}{
		{"", "http-1080p"},
		{"best", "http-1080p"},
		{"worst", "http-240p"},
		{"best-hls", "hls-1080p"},
		{"best-http", "http-1080p"},
		{"720p", "http-720p"},
		{"720", "http-720p"},
		{"http-240p", "http-240p"},
		{"BEST", "http-1080p"},
	}
	for _, c := range cases {
		m := sample()
		got, err := m.Select(c.sel)
		if err != nil {
			t.Fatalf("Select(%q): %v", c.sel, err)
		}
		if got.ID != c.want {
			t.Errorf("Select(%q) = %q, want %q", c.sel, got.ID, c.want)
		}
	}
}

func TestSelectRanksProgressiveOverHLS(t *testing.T) {
	m := sample()
	m.Sort()
	// Equal height, equal bitrate, equal size: HTTP must outrank HLS.
	if last := m.Formats[len(m.Formats)-1]; last.Protocol != ProtocolHTTP {
		t.Fatalf("best format is %s/%s, want a progressive one", last.ID, last.Protocol)
	}
	if m.Formats[0].ID != "http-240p" {
		t.Fatalf("worst format is %s, want http-240p", m.Formats[0].ID)
	}
}

func TestSelectTieBreaksOnBitrateThenSize(t *testing.T) {
	m := &Media{Formats: []Format{
		{ID: "a", Height: 720, Bitrate: 1000},
		{ID: "b", Height: 720, Bitrate: 2000},
		{ID: "c", Height: 720, Bitrate: 2000, Size: 10},
	}}
	got, err := m.Select("best")
	if err != nil || got.ID != "c" {
		t.Fatalf("Select(best) = %v, %v; want c", got.ID, err)
	}
}

func TestSelectByQualityLabel(t *testing.T) {
	m := &Media{Formats: []Format{{ID: "a", Quality: "SD"}, {ID: "b", Quality: "HD"}}}
	got, err := m.Select("hd")
	if err != nil || got.ID != "b" {
		t.Fatalf("Select(hd) = %v, %v; want b", got.ID, err)
	}
}

func TestSelectErrors(t *testing.T) {
	if _, err := (&Media{}).Select("best"); !errors.Is(err, ErrNoFormat) {
		t.Fatalf("empty media: %v, want ErrNoFormat", err)
	}
	m := sample()
	for _, sel := range []string{"nope", "4320p", "best-dash"} {
		if _, err := m.Select(sel); !errors.Is(err, ErrNoFormat) {
			t.Errorf("Select(%q) = %v, want ErrNoFormat", sel, err)
		}
	}
	noHLS := &Media{Formats: []Format{{ID: "a", Protocol: ProtocolHTTP}}}
	if _, err := noHLS.Select("best-hls"); !errors.Is(err, ErrNoFormat) {
		t.Errorf("best-hls without HLS = %v, want ErrNoFormat", err)
	}
}

func TestFormatLabelAndString(t *testing.T) {
	f := Format{ID: "http-720p", Ext: "mp4", Protocol: ProtocolHTTP, Height: 720,
		Bitrate: 2_000_000, Size: 5 << 20, Note: "hello"}
	s := f.String()
	for _, want := range []string{"http-720p", "mp4", "720p", "2000 kbps", "5.0 MiB", "hello"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
	if got := (Format{Quality: "HD"}).Label(); got != "HD" {
		t.Errorf("Label() = %q, want HD", got)
	}
	if got := (Format{}).Label(); got != "?" {
		t.Errorf("Label() = %q, want ?", got)
	}
	if got := (Format{Height: 1080}).String(); !strings.Contains(got, "1080p") {
		t.Errorf("String() = %q, want 1080p", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		1 << 20: "1.0 MiB", 1 << 30: "1.0 GiB", 1 << 40: "1.0 TiB", 1 << 50: "1.0 PiB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"":                       "video",
		"   ":                    "video",
		"a/b\\c:d*e?f\"g<h>i|j":  "a_b_c_d_e_f_g_h_i_j",
		"hello\x00\x07 world":    "hello world",
		"  spaced   out  ":       "spaced out",
		"trailing dots...":       "trailing dots",
		"...":                    "video",
		"line\nbreak\tand\ttabs": "line break and tabs",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	long := Sanitize(strings.Repeat("é", 400))
	if n := len([]rune(long)); n != 150 {
		t.Errorf("long title kept %d runes, want 150", n)
	}
}

func TestExpand(t *testing.T) {
	m := sample()
	f := m.Formats[0]
	got, err := Expand("%(site)s/%(uploader)s - %(title)s [%(id)s] %(quality)s %(height)dx%(width)d %(duration)ds.%(ext)s", m, f)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := "samplesite/Ann - Some _ Title [42] 720p 720x0 90s.mp4"
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
	if got, err := Expand("%(format)s", m, f); err != nil || got != "http-720p" {
		t.Fatalf("Expand(%%(format)s) = %q, %v", got, err)
	}
}

// TestExpandWithExtraKeys covers what a caller adds to a template: keys the
// media itself knows nothing about, such as the listing a video was reached
// through. They are sanitised like a title, and they win over the built-in
// keys of the same name.
func TestExpandWithExtraKeys(t *testing.T) {
	m := sample()
	f := m.Formats[0]
	extra := map[string]string{
		"playlist": "A / Creator\tpage",
		"site":     "over/ridden",
	}
	got, err := ExpandWith("%(playlist)s/%(site)s - %(title)s.%(ext)s", m, f, extra)
	if err != nil {
		t.Fatalf("ExpandWith: %v", err)
	}
	// The slash and the tab are what Sanitize turns into "_" and a space.
	want := "A _ Creator page/over_ridden - Some _ Title.mp4"
	if got != want {
		t.Fatalf("ExpandWith = %q, want %q", got, want)
	}
	// Without the extra map, the same template no longer resolves, and the
	// built-in key is back to what the media says.
	if _, err := ExpandWith("%(playlist)s.%(ext)s", m, f, nil); !errors.Is(err, ErrTemplateKey) {
		t.Errorf("an extra key survived the call that did not supply it: %v", err)
	}
	if got, err := ExpandWith("%(site)s", m, f, nil); err != nil || got != "samplesite" {
		t.Errorf("ExpandWith(%%(site)s) = %q, %v", got, err)
	}
}

func TestExpandErrors(t *testing.T) {
	m := sample()
	for _, tmpl := range []string{"%(nope)s", "%(title", "%(title)", "%(id)s"} {
		_, err := Expand(tmpl, &Media{}, Format{})
		if tmpl == "%(id)s" {
			if !errors.Is(err, ErrTemplateKey) {
				t.Errorf("Expand(%q) on empty media = %v, want ErrTemplateKey", tmpl, err)
			}
			continue
		}
		if !errors.Is(err, ErrTemplateKey) {
			t.Errorf("Expand(%q) = %v, want ErrTemplateKey", tmpl, err)
		}
	}
	if _, err := Expand("plain.mp4", m, m.Formats[0]); err != nil {
		t.Errorf("literal template: %v", err)
	}
}

func TestSelectWhenNoFormatAnnouncesAHeight(t *testing.T) {
	// An adaptive master playlist has no resolution: best and worst then
	// fall back to the plain ranking instead of failing.
	m := &Media{Formats: []Format{
		{ID: "hls-auto", Protocol: ProtocolHLS},
		{ID: "hls-auto-2", Protocol: ProtocolHLS, Bitrate: 10},
	}}
	best, err := m.Select("best")
	if err != nil || best.ID != "hls-auto-2" {
		t.Fatalf("Select(best) = %q, %v", best.ID, err)
	}
	worst, err := m.Select("worst")
	if err != nil || worst.ID != "hls-auto" {
		t.Fatalf("Select(worst) = %q, %v", worst.ID, err)
	}
}

func TestSelectSkipsUngradedFormats(t *testing.T) {
	// The master playlist sorts lowest, but "worst" must still mean 240p.
	m := &Media{Formats: []Format{
		{ID: "hls-auto", Protocol: ProtocolHLS},
		{ID: "http-240p", Protocol: ProtocolHTTP, Height: 240},
		{ID: "http-1080p", Protocol: ProtocolHTTP, Height: 1080},
	}}
	if got, _ := m.Select("worst"); got.ID != "http-240p" {
		t.Fatalf("Select(worst) = %q, want http-240p", got.ID)
	}
	if got, _ := m.Select("best"); got.ID != "http-1080p" {
		t.Fatalf("Select(best) = %q, want http-1080p", got.ID)
	}
	if got, _ := m.Select("hls-auto"); got.ID != "hls-auto" {
		t.Fatal("an ungraded format must stay reachable by ID")
	}
}

func TestRankingIgnoresUnknownBitratesAndSizes(t *testing.T) {
	// A progressive source rarely announces a bitrate. Treating that as zero
	// would sink it below any HLS variant of the same resolution, which is
	// not a quality judgement but a missing-data artefact.
	m := &Media{Formats: []Format{
		{ID: "hls-480p", Protocol: ProtocolHLS, Height: 480, Bitrate: 2_800_000},
		{ID: "http-480p", Protocol: ProtocolHTTP, Height: 480},
	}}
	if got, _ := m.Select("best"); got.ID != "http-480p" {
		t.Fatalf("Select(best) = %q, want the progressive one at equal height", got.ID)
	}
	// When both do announce one, the higher bitrate wins.
	m = &Media{Formats: []Format{
		{ID: "hls-480p", Protocol: ProtocolHLS, Height: 480, Bitrate: 2_800_000},
		{ID: "http-480p", Protocol: ProtocolHTTP, Height: 480, Bitrate: 900_000},
	}}
	if got, _ := m.Select("best"); got.ID != "hls-480p" {
		t.Fatalf("Select(best) = %q, want the higher bitrate", got.ID)
	}
	// Sizes follow the same rule: two known sizes rank, an unknown one
	// neither promotes nor demotes, so the input order stands.
	m = &Media{Formats: []Format{
		{ID: "small", Height: 480, Size: 10},
		{ID: "big", Height: 480, Size: 20},
	}}
	if got, _ := m.Select("best"); got.ID != "big" {
		t.Fatalf("Select(best) = %q, want the bigger of two known sizes", got.ID)
	}
	m = &Media{Formats: []Format{
		{ID: "sized", Height: 480, Size: 10},
		{ID: "unsized", Height: 480},
	}}
	if got, _ := m.Select("best"); got.ID != "unsized" {
		t.Fatalf("Select(best) = %q, want the order left untouched", got.ID)
	}
}
