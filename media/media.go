// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package media holds the site-agnostic description of an extracted video plus
// the pure helpers used to rank formats and to name the output file. It is the
// wire contract shared by the host and every plugin, so every field must stay
// gob-encodable.
package media

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Protocol tells the downloader which engine can fetch a Format.
type Protocol string

const (
	// ProtocolHTTP is a single, directly addressable file (usually MP4).
	ProtocolHTTP Protocol = "http"
	// ProtocolHLS is an m3u8 playlist, master or media.
	ProtocolHLS Protocol = "hls"
	// ProtocolDASH is an MPEG-DASH manifest.
	ProtocolDASH Protocol = "dash"
	// ProtocolTorrent is a magnet link or a metainfo file: content fetched
	// from a swarm rather than from a server.
	ProtocolTorrent Protocol = "torrent"
)

// Format is one downloadable rendition of a Media.
type Format struct {
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Ext      string            `json:"ext"`
	Protocol Protocol          `json:"protocol"`
	Width    int               `json:"width,omitempty"`
	Height   int               `json:"height,omitempty"`
	FPS      float64           `json:"fps,omitempty"`
	Bitrate  int               `json:"bitrate,omitempty"` // bits per second
	Size     int64             `json:"size,omitempty"`    // bytes, 0 when unknown
	Quality  string            `json:"quality,omitempty"` // site label, e.g. "1080p"
	Note     string            `json:"note,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"` // per-format request headers
}

// Label is the human-readable resolution of the format.
func (f Format) Label() string {
	if f.Height > 0 {
		return strconv.Itoa(f.Height) + "p"
	}
	if f.Quality != "" {
		return f.Quality
	}
	return "?"
}

// String renders one line of the `-F` format table.
func (f Format) String() string {
	parts := []string{fmt.Sprintf("%-12s %-4s %-5s %-6s", f.ID, f.Ext, f.Protocol, f.Label())}
	if f.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", f.Bitrate/1000))
	}
	if f.Size > 0 {
		parts = append(parts, HumanBytes(f.Size))
	}
	if f.Note != "" {
		parts = append(parts, f.Note)
	}
	return strings.Join(parts, "  ")
}

// Media is everything a plugin could learn about a video page.
type Media struct {
	Site       string        `json:"site"`
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Uploader   string        `json:"uploader,omitempty"`
	Duration   time.Duration `json:"duration,omitempty"`
	Thumbnail  string        `json:"thumbnail,omitempty"`
	WebpageURL string        `json:"webpage_url"`
	AgeLimit   int           `json:"age_limit,omitempty"`
	Formats    []Format      `json:"formats"`
}

// ErrNoFormat is returned when no rendition satisfies a selector.
var ErrNoFormat = errors.New("media: no matching format")

// Sort orders formats worst-first, so Formats[len-1] is the best one. The
// ranking is height, then bitrate, then declared size, and finally a
// preference for progressive HTTP over HLS at equal quality.
func (m *Media) Sort() {
	sort.SliceStable(m.Formats, func(i, j int) bool { return less(m.Formats[i], m.Formats[j]) })
}

func less(a, b Format) bool {
	if a.Height != b.Height {
		return a.Height < b.Height
	}
	// Bitrate and size only rank two formats when both announce one: an
	// unknown bitrate is not a low one, and treating it as zero would sink
	// every progressive source below any HLS variant.
	if a.Bitrate > 0 && b.Bitrate > 0 && a.Bitrate != b.Bitrate {
		return a.Bitrate < b.Bitrate
	}
	if a.Size > 0 && b.Size > 0 && a.Size != b.Size {
		return a.Size < b.Size
	}
	return a.Protocol == ProtocolHLS && b.Protocol == ProtocolHTTP
}

// Select resolves a selector against the media formats. Accepted selectors:
//
//	""            same as "best"
//	"best"        highest ranked format
//	"worst"       lowest ranked format
//	"best-http"   highest ranked progressive format ("best-hls" likewise)
//	"720p", "720" highest ranked format at that height or site label
//	"<id>"        exact format ID
func (m *Media) Select(selector string) (Format, error) {
	if len(m.Formats) == 0 {
		return Format{}, ErrNoFormat
	}
	m.Sort()
	sel := strings.ToLower(strings.TrimSpace(selector))
	switch sel {
	case "", "best":
		if f, err := lastMatch(m.Formats, graded); err == nil {
			return f, nil
		}
		return m.Formats[len(m.Formats)-1], nil
	case "worst":
		if f, err := firstMatch(m.Formats, graded); err == nil {
			return f, nil
		}
		return m.Formats[0], nil
	case "best-http", "best-hls":
		want := ProtocolHTTP
		if sel == "best-hls" {
			want = ProtocolHLS
		}
		return lastMatch(m.Formats, func(f Format) bool { return f.Protocol == want })
	}
	if f, err := lastMatch(m.Formats, func(f Format) bool { return f.ID == selector }); err == nil {
		return f, nil
	}
	if h, err := strconv.Atoi(strings.TrimSuffix(sel, "p")); err == nil {
		return lastMatch(m.Formats, func(f Format) bool {
			return f.Height == h || strings.EqualFold(f.Quality, sel)
		})
	}
	return lastMatch(m.Formats, func(f Format) bool { return strings.EqualFold(f.Quality, sel) })
}

// graded reports whether a format announces a real resolution. An adaptive
// master playlist does not, and must not win "worst" over a 240p rendition.
func graded(f Format) bool { return f.Height > 0 }

func firstMatch(fs []Format, ok func(Format) bool) (Format, error) {
	for _, f := range fs {
		if ok(f) {
			return f, nil
		}
	}
	return Format{}, ErrNoFormat
}

func lastMatch(fs []Format, ok func(Format) bool) (Format, error) {
	for i := len(fs) - 1; i >= 0; i-- {
		if ok(fs[i]) {
			return fs[i], nil
		}
	}
	return Format{}, ErrNoFormat
}

// HumanBytes formats a byte count with binary prefixes.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, exp := float64(n), 0
	for v >= unit && exp < 5 {
		v /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", v, "KMGTP"[exp-1])
}

// Sanitize turns an arbitrary title into a portable file name component.
func Sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// newlines and tabs are word separators, other control
			// characters are noise
			if unicode.IsSpace(r) {
				b.WriteByte(' ')
			}
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.FieldsFunc(b.String(), unicode.IsSpace), " ")
	out = strings.Trim(out, " ._")
	if out == "" {
		return "video"
	}
	if r := []rune(out); len(r) > 150 {
		out = strings.TrimRight(string(r[:150]), " .")
	}
	return out
}

// ErrTemplateKey reports a malformed or unknown %(key)s in an output template.
var ErrTemplateKey = errors.New("media: bad output template")

// Expand renders an output template such as
// "%(uploader)s - %(title)s [%(id)s].%(ext)s" for one media and format.
func Expand(tmpl string, m *Media, f Format) (string, error) {
	return ExpandWith(tmpl, m, f, nil)
}

// ExpandWith renders a template with extra keys the caller supplies, such as
// the playlist a video was reached through. Extra keys are sanitised the same
// way a title is, and override the built-in ones.
func ExpandWith(tmpl string, m *Media, f Format, extra map[string]string) (string, error) {
	vals := map[string]string{
		"id":       m.ID,
		"title":    Sanitize(m.Title),
		"uploader": Sanitize(m.Uploader),
		"site":     m.Site,
		"ext":      f.Ext,
		"format":   f.ID,
		"quality":  f.Label(),
		"height":   strconv.Itoa(f.Height),
		"width":    strconv.Itoa(f.Width),
		"duration": strconv.Itoa(int(m.Duration / time.Second)),
	}
	for k, v := range extra {
		vals[k] = Sanitize(v)
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if !strings.HasPrefix(tmpl[i:], "%(") {
			b.WriteByte(tmpl[i])
			i++
			continue
		}
		end := strings.Index(tmpl[i:], ")")
		if end < 0 {
			return "", fmt.Errorf("%w: unterminated key in %q", ErrTemplateKey, tmpl[i:])
		}
		key := tmpl[i+2 : i+end]
		if rest := tmpl[i+end+1:]; rest == "" {
			return "", fmt.Errorf("%w: key %q misses its conversion", ErrTemplateKey, key)
		}
		v, ok := vals[key]
		if !ok {
			return "", fmt.Errorf("%w: unknown key %q", ErrTemplateKey, key)
		}
		b.WriteString(v)
		i += end + 2
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("%w: %q expands to nothing", ErrTemplateKey, tmpl)
	}
	return out, nil
}
