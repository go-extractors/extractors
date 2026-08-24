// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package scrape is the toolkit every plugin ends up needing: fetching a page
// under the host's own HTTP policy, pulling a JSON blob out of a script tag,
// reading OpenGraph tags, and turning what a site says about a file into the
// fields a media.Format expects.
//
// It exists so a new plugin is only the part that is specific to its site.
package scrape

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
)

// DefaultTimeout is how long a plugin gives itself when the host names no
// timeout of its own.
const DefaultTimeout = 60 * time.Second

// Client builds the HTTP client and the deadline one extraction runs under,
// from the policy the host passed down: its user agent, cookies, proxy,
// retries and rate limits. The host owns the real cancellation; the deadline
// here is the plugin's own safety net.
func Client(req extractor.Request) (*httpx.Client, context.Context, context.CancelFunc, error) {
	c, err := httpx.New(req.HTTP)
	if err != nil {
		return nil, nil, nil, err
	}
	timeout := req.HTTP.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
	return c, ctx, cancel, nil
}

// PageHeaders are what a browser sends when it asks for a page.
func PageHeaders() map[string]string {
	return map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	}
}

// Page fetches an HTML page and reports the URL it ended up at, which is what
// a plugin must use to resolve relative links. A 404 becomes ErrNotFound, the
// one status every site means the same way.
func Page(ctx context.Context, c *httpx.Client, rawURL string, hdr map[string]string) (string, string, error) {
	if hdr == nil {
		hdr = PageHeaders()
	}
	body, resp, err := c.Get(ctx, rawURL, hdr)
	if err != nil {
		if se, ok := err.(*httpx.StatusError); ok && se.Code == 404 {
			return "", "", fmt.Errorf("%w: %s", extractor.ErrNotFound, rawURL)
		}
		return "", "", err
	}
	final := rawURL
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return string(body), final, nil
}

// JSONObjectAfter returns the balanced {…} literal that follows marker. A
// regexp cannot do this: a player state is megabytes of nested objects, and
// its strings contain braces.
func JSONObjectAfter(s, marker string) (string, bool) {
	i := strings.Index(s, marker)
	if i < 0 {
		return "", false
	}
	return JSONObjectAt(s, i+len(marker))
}

// JSONObjectAt returns the balanced {…} literal starting at or after i,
// ignoring braces that sit inside strings.
func JSONObjectAt(s string, i int) (string, bool) {
	rel := strings.IndexByte(s[i:], '{')
	if rel < 0 {
		return "", false
	}
	start := i + rel
	depth, inStr, esc := 0, false, false
	for j := start; j < len(s); j++ {
		c := s[j]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// braces inside strings do not count
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : j+1], true
			}
		}
	}
	return "", false
}

// Unescape resolves the entities a page puts in its text, numeric ones
// included, and trims the result.
func Unescape(s string) string { return strings.TrimSpace(html.UnescapeString(s)) }

var (
	reOG    = regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)\s*=\s*["']og:([a-z:_]+)["'][^>]*content\s*=\s*["']([^"']*)["']`)
	reOGAlt = regexp.MustCompile(`(?is)<meta[^>]+content\s*=\s*["']([^"']*)["'][^>]*(?:property|name)\s*=\s*["']og:([a-z:_]+)["']`)
	reTitle = regexp.MustCompile(`(?is)<title>([^<]*)</title>`)
	// No word boundary after the "p": real URLs spell it "1080P_4000K".
	reQuality = regexp.MustCompile(`(\d{3,4})\s*[pP]`)
)

// OGTags collects the OpenGraph metadata of a page, in either attribute order.
func OGTags(page string) map[string]string {
	out := map[string]string{}
	for _, m := range reOG.FindAllStringSubmatch(page, -1) {
		out[strings.ToLower(m[1])] = Unescape(m[2])
	}
	for _, m := range reOGAlt.FindAllStringSubmatch(page, -1) {
		k := strings.ToLower(m[2])
		if _, ok := out[k]; !ok {
			out[k] = Unescape(m[1])
		}
	}
	return out
}

// Title is what the page calls itself: its og:title, else its <title> tag.
func Title(page string) string {
	if t := OGTags(page)["title"]; t != "" {
		return t
	}
	if m := reTitle.FindStringSubmatch(page); m != nil {
		return Unescape(m[1])
	}
	return ""
}

// TrimSiteSuffix drops the site name pages append to their title, as in
// "clip.mp4 | Samplesite" or "A Clip - Example.com".
func TrimSiteSuffix(s string) string {
	for _, sep := range []string{" | ", " - "} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// HeightOf reads the pixel height out of anything a site labels with one: a
// quality label like "1080p", or a URL such as ".../1080P_4000K/index.m3u8".
func HeightOf(s string) int {
	m := reQuality.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1]) // the pattern only captures 3 or 4 digits
	return n
}

// knownHeights are the resolutions a file name may state without a "p", which
// is the only way to read one safely: any 3-or-4-digit number would otherwise
// turn an id into a resolution.
var knownHeights = map[int]bool{
	144: true, 180: true, 240: true, 270: true, 360: true, 480: true, 540: true,
	576: true, 720: true, 1080: true, 1440: true, 2160: true, 4320: true,
}

var reBareHeight = regexp.MustCompile(`(?:^|[^0-9a-zA-Z])(\d{3,4})(?:$|[^0-9a-zA-Z])`)
var reDimensions = regexp.MustCompile(`(\d{3,4})\s*[xX]\s*(\d{3,4})`)

// HeightFromName reads the resolution a file name or URL states, in any of the
// three spellings sites use: "1080p", "1920x1080", or a bare "1080" that
// happens to be a standard resolution.
func HeightFromName(s string) int {
	if h := HeightOf(s); h > 0 {
		return h
	}
	if m := reDimensions.FindStringSubmatch(s); m != nil {
		if h, err := strconv.Atoi(m[2]); err == nil && h > 0 {
			return h
		}
	}
	best := 0
	for _, m := range reBareHeight.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil && knownHeights[n] && n > best {
			best = n
		}
	}
	return best
}

// QualityLabel names a rendition: its height when known, else the label the
// site used, else "auto" for an adaptive source that announces neither.
func QualityLabel(quality string, height int) string {
	if height > 0 {
		return fmt.Sprintf("%dp", height)
	}
	if q := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(quality), " ", "")); q != "" && q != "auto" {
		return q
	}
	return "auto"
}

// Compact drops sourceless entries and duplicates, and makes every ID unique.
// Several formats may legitimately share one URL — a master playlist is listed
// once per rendition it serves — so the identity of a format is its URL, its
// protocol and its height together.
func Compact(in []media.Format) []media.Format {
	seen, used := map[string]bool{}, map[string]bool{}
	out := make([]media.Format, 0, len(in))
	for _, f := range in {
		if f.URL == "" {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d", f.URL, f.Protocol, f.Height)
		if seen[key] {
			continue
		}
		seen[key] = true
		id := f.ID
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", f.ID, n)
		}
		used[id] = true
		f.ID = id
		out = append(out, f)
	}
	return out
}

// ExtOf is a file name's extension without the dot, "bin" when it has none.
func ExtOf(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	if ext == "" {
		return "bin"
	}
	return ext
}

// KindOf labels a file by its extension, which is what --kind filters on.
func KindOf(name string) string {
	switch ExtOf(name) {
	case "mp4", "m4v", "mkv", "webm", "mov", "avi", "ts", "flv", "wmv":
		return "video"
	case "jpg", "jpeg", "png", "gif", "webp", "avif", "heic", "bmp":
		return "image"
	case "mp3", "m4a", "flac", "wav", "ogg", "opus":
		return "audio"
	default:
		return "file"
	}
}

// CleanURL undoes the escaping a page applies to URLs inside its scripts.
func CleanURL(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), `\/`, "/")
}

// DedupeURLs keeps the first occurrence of each URL, in order, unescaping them
// on the way.
func DedupeURLs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, u := range in {
		u = CleanURL(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// LastPathSegment is the last part of a URL's path, which is how most sites
// spell an id.
func LastPathSegment(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	return parts[len(parts)-1]
}

// ParseClock reads the "12:34" or "1:02:03" a listing prints on a thumbnail.
func ParseClock(s string) time.Duration {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	units := []time.Duration{time.Hour, time.Minute, time.Second}
	units = units[len(units)-len(parts):]
	var total time.Duration
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0
		}
		total += time.Duration(n) * units[i]
	}
	return total
}

var reByteSize = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*(B|KB|MB|GB|TB|KIB|MIB|GIB|TIB)\s*$`)

// ParseByteSize reads the "10.25 MB" a listing prints under a file.
func ParseByteSize(s string) int64 {
	m := reByteSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{
		"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40,
		"KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40,
	}[strings.ToUpper(m[2])]
	return int64(v * mult)
}

// ISODuration decodes an ISO 8601 duration such as "PT8M31S", as JSON-LD uses.
func ISODuration(s string) time.Duration {
	m := reISO.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	var d time.Duration
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second} {
		if m[i+1] == "" {
			continue
		}
		f, err := strconv.ParseFloat(m[i+1], 64)
		if err != nil {
			return 0
		}
		d += time.Duration(f * float64(unit))
	}
	return d
}

var reISO = regexp.MustCompile(`^P(?:(\d+)D)?T(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?$`)
