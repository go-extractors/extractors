// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package scrape

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
)

func TestClientCarriesTheHostPolicy(t *testing.T) {
	req := extractor.Request{URL: "https://x/videos/1", HTTP: httpx.Config{
		UserAgent: "scrape-test", Cookies: []string{"a=1"}, Retries: 2,
		Timeout: 5 * time.Second, RateLimit: 3,
	}}
	c, ctx, cancel, err := Client(req)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer cancel()
	cfg := c.Config()
	if cfg.UserAgent != "scrape-test" || cfg.Cookies[0] != "a=1" || cfg.Retries != 2 || cfg.RateLimit != 3 {
		t.Fatalf("config = %+v", cfg)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 5*time.Second {
		t.Fatalf("the plugin deadline must outlast one request: %v", deadline)
	}
	// Without a timeout of its own, the plugin still gets one.
	_, ctx2, cancel2, err := Client(extractor.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	if _, ok := ctx2.Deadline(); !ok {
		t.Fatal("no deadline at all")
	}
	if _, _, _, err := Client(extractor.Request{HTTP: httpx.Config{Proxy: "http://%zz"}}); err == nil {
		t.Fatal("a bad proxy was accepted")
	}
}

func TestPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Language") == "" {
			t.Error("the browser headers were not sent")
		}
		w.Write([]byte("<html>ok</html>"))
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := httpx.New(httpx.Config{Retries: 0, RateLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	body, final, err := Page(context.Background(), c, srv.URL+"/moved", nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if body != "<html>ok</html>" || !strings.HasSuffix(final, "/final") {
		t.Fatalf("body = %q, final = %q", body, final)
	}
	if _, _, err := Page(context.Background(), c, srv.URL+"/gone", nil); !errors.Is(err, extractor.ErrNotFound) {
		t.Errorf("404 = %v, want ErrNotFound", err)
	}
	if _, _, err := Page(context.Background(), c, srv.URL+"/boom", nil); !errors.Is(err, httpx.ErrHTTPStatus) {
		t.Errorf("500 = %v", err)
	}
	// A caller may impose its own headers.
	if _, _, err := Page(context.Background(), c, srv.URL+"/final", map[string]string{
		"Accept-Language": "fr", "Accept": "*/*",
	}); err != nil {
		t.Errorf("custom headers: %v", err)
	}
}

func TestJSONObject(t *testing.T) {
	cases := []struct {
		name, in, marker, want string
		ok                     bool
	}{
		{"simple", `x window.initials={"a":1};`, "window.initials=", `{"a":1}`, true},
		{"nested", `s={"a":{"b":[{"c":2}]}};`, "s=", `{"a":{"b":[{"c":2}]}}`, true},
		{"brace in string", `s={"a":"}{","b":1};`, "s=", `{"a":"}{","b":1}`, true},
		{"escaped quote", `s={"a":"x\"}","b":1};`, "s=", `{"a":"x\"}","b":1}`, true},
		{"escaped backslash", `s={"a":"x\\","b":1};`, "s=", `{"a":"x\\","b":1}`, true},
		{"no marker", `nothing`, "s=", "", false},
		{"no object", `s=null;`, "s=", "", false},
		{"unbalanced", `s={"a":{"b":1};`, "s=", "", false},
	}
	for _, c := range cases {
		got, ok := JSONObjectAfter(c.in, c.marker)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: JSONObjectAfter = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if got, ok := JSONObjectAt(`var v = {"a":1};`, 0); !ok || got != `{"a":1}` {
		t.Errorf("JSONObjectAt = %q, %v", got, ok)
	}
}

func TestUnescapeAndTitles(t *testing.T) {
	if got := Unescape("Tom &amp; Jerry &#124; &quot;x&quot; &nbsp;"); got != `Tom & Jerry | "x"` {
		t.Errorf("Unescape = %q", got)
	}
	page := `<html><head><title>clip.mp4 | Samplesite</title>
		<meta property="og:title" content="A Clip &#124; part 2">
		<meta content="https://thumb/1.jpg" property="og:image"></head></html>`
	if got := Title(page); got != "A Clip | part 2" {
		t.Errorf("Title = %q", got)
	}
	if got := OGTags(page)["image"]; got != "https://thumb/1.jpg" {
		t.Errorf("reversed attribute order failed: %q", got)
	}
	noOG := `<html><head><title>clip.mp4 | Samplesite</title></head></html>`
	if got := Title(noOG); got != "clip.mp4 | Samplesite" {
		t.Errorf("Title = %q", got)
	}
	if got := TrimSiteSuffix(Title(noOG)); got != "clip.mp4" {
		t.Errorf("TrimSiteSuffix = %q", got)
	}
	if got := TrimSiteSuffix("A Clip - Example.com"); got != "A Clip" {
		t.Errorf("TrimSiteSuffix = %q", got)
	}
	if got := Title("<html></html>"); got != "" {
		t.Errorf("Title = %q, want empty", got)
	}
}

func TestOGTagsKeepsTheFirstOccurrence(t *testing.T) {
	got := OGTags(`<meta property="og:title" content="first">
		<meta content="second" property="og:title">
		<meta name="og:site_name" content="site">`)
	if got["title"] != "first" || got["site_name"] != "site" {
		t.Fatalf("OGTags = %v", got)
	}
}

func TestHeightOf(t *testing.T) {
	cases := map[string]int{
		"1080p": 1080, "720P": 720, "hls-480p": 480,
		"https://cdn/x-2160p.mp4":              2160,
		"https://hv/1080P_4000K_45/index.m3u8": 1080,
		"HD":                                   0, "": 0, "12p": 0,
	}
	for in, want := range cases {
		if got := HeightOf(in); got != want {
			t.Errorf("HeightOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestQualityLabel(t *testing.T) {
	cases := []struct {
		quality string
		height  int
		want    string
	}{
		{"ignored", 720, "720p"},
		{"Full HD", 0, "fullhd"},
		{"auto", 0, "auto"},
		{"", 0, "auto"},
	}
	for _, c := range cases {
		if got := QualityLabel(c.quality, c.height); got != c.want {
			t.Errorf("QualityLabel(%q, %d) = %q, want %q", c.quality, c.height, got, c.want)
		}
	}
}

func TestCompact(t *testing.T) {
	in := []media.Format{
		{ID: "hls-720p", URL: "https://m/master.m3u8", Protocol: media.ProtocolHLS, Height: 720},
		{ID: "hls-1080p", URL: "https://m/master.m3u8", Protocol: media.ProtocolHLS, Height: 1080},
		{ID: "hls-720p", URL: "https://m/master.m3u8", Protocol: media.ProtocolHLS, Height: 720},
		{ID: "dup", URL: "https://m/a.mp4"},
		{ID: "dup", URL: "https://m/b.mp4"},
		{ID: "dup", URL: "https://m/c.mp4"},
		{ID: "dropped", URL: ""},
	}
	got := Compact(in)
	want := []string{"hls-720p", "hls-1080p", "dup", "dup-2", "dup-3"}
	if len(got) != len(want) {
		t.Fatalf("got %d formats: %+v", len(got), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("format %d = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestExtAndKind(t *testing.T) {
	kinds := map[string]string{
		"a.mp4": "video", "a.MKV": "video", "a.webm": "video",
		"a.jpg": "image", "a.PNG": "image", "a.mp3": "audio", "a.flac": "audio",
		"a.zip": "file", "noext": "file",
	}
	for name, want := range kinds {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
	if got := ExtOf("noext"); got != "bin" {
		t.Errorf("ExtOf = %q, want bin", got)
	}
	if got := ExtOf("a.MP4"); got != "mp4" {
		t.Errorf("ExtOf = %q", got)
	}
}

func TestCleanAndDedupeURLs(t *testing.T) {
	if got := CleanURL(` https:\/\/cdn\/a.mp4 `); got != "https://cdn/a.mp4" {
		t.Errorf("CleanURL = %q", got)
	}
	got := DedupeURLs([]string{`https://a\/b.mp4`, "https://a/b.mp4", "", "https://c.mp4"})
	if len(got) != 2 || got[0] != "https://a/b.mp4" || got[1] != "https://c.mp4" {
		t.Fatalf("DedupeURLs = %q", got)
	}
}

func TestLastPathSegment(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a/6urUNfkp": "6urUNfkp",
		"https://x/creators/name/shorts": "shorts",
		"https://x/trailing/":            "trailing",
		"://":                            "",
	}
	for in, want := range cases {
		if got := LastPathSegment(in); got != want {
			t.Errorf("LastPathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseClock(t *testing.T) {
	cases := map[string]time.Duration{
		"12:34":   12*time.Minute + 34*time.Second,
		"1:02:03": time.Hour + 2*time.Minute + 3*time.Second,
		"0:05":    5 * time.Second,
		// Composed for a human: the space between the parts is a
		// non-breaking one, which is not a space as far as ASCII is
		// concerned.
		"1:\u00a002": time.Minute + 2*time.Second,
		"":           0, "12": 0, "1:2:3:4": 0, "a:b": 0, "-1:00": 0,
	}
	for in, want := range cases {
		if got := ParseClock(in); got != want {
			t.Errorf("ParseClock(%q) = %v, want %v", in, got, want)
		}
	}
}

// gib is what the parser computes for a fractional count of gibibytes: the
// product truncated, not rounded.
func gib(n float64) int64 { return int64(n * (1 << 30)) }

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{
		"10.25 MB": 10.25 * (1 << 20), "512 KB": 512 << 10, "1.5 GB": 1.5 * (1 << 30),
		"3 B": 3, "2 TB": 2 << 40, "4 MiB": 4 << 20,
		// What a site actually serves: a non-breaking space before the
		// unit, and the narrow one French typography asks for.
		"2.9\u00a0GB": gib(2.9), "1.5\u202fGB": gib(1.5),
		"": 0, "big": 0, "12": 0, "1e9 MB": 0,
	}
	for in, want := range cases {
		if got := ParseByteSize(in); got != want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestISODuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT8M31S":  8*time.Minute + 31*time.Second,
		"PT1H2M3S": time.Hour + 2*time.Minute + 3*time.Second,
		"P1DT1H":   25 * time.Hour,
		"PT0.5S":   500 * time.Millisecond,
		"":         0, "garbage": 0, "PT1.2.3S": 0, "P1Y": 0,
	}
	for in, want := range cases {
		if got := ISODuration(in); got != want {
			t.Errorf("ISODuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHeightFromName(t *testing.T) {
	cases := map[string]int{
		"Big_Buck_Bunny_1080_10s_5MB.mp4": 1080,
		"clip-720p.mp4":                   720,
		"render_1920x1080.mp4":            1080,
		"video_480_x.mp4":                 480,
		"id-12345678.mp4":                 0, // an id is not a resolution
		"file_999.mp4":                    0, // 999 is no standard height
		"plain.mp4":                       0,
	}
	for in, want := range cases {
		if got := HeightFromName(in); got != want {
			t.Errorf("HeightFromName(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestParseByteSizeRefusesANumberItCannotHold covers a size so long it does not
// fit a float: a site stating nonsense must give nothing, not a wrapped value
// that would become a progress total.
func TestParseByteSizeRefusesANumberItCannotHold(t *testing.T) {
	huge := strings.Repeat("9", 400) + " MB"
	if got := ParseByteSize(huge); got != 0 {
		t.Fatalf("ParseByteSize(400 nines) = %d, want 0", got)
	}
	// The same digits within reach are read, so the refusal above is about
	// the size of the number and nothing else.
	if got := ParseByteSize("9 MB"); got != 9<<20 {
		t.Fatalf("ParseByteSize(\"9 MB\") = %d", got)
	}
}

// TestIsPreviewURL pins what counts as a preview. The cases are taken from what
// sites actually serve: the animated clip a listing plays under a cursor, the
// sprite sheet of stills a scrubber uses, and the image hosts these live on.
func TestIsPreviewURL(t *testing.T) {
	previews := []string{
		"https://cdn.example.com/vid/thumb_vid.mp4",
		// Written without a separator, which is how some sites name the clip
		// a listing plays under a cursor.
		"https://cdn.example.com/a/b/vidthumb.mp4",
		"https://cdn.example.com/a/b/vid_thumb.mp4",
		"https://cdn.example.com/thumbs/12345.mp4",
		"https://cdn.example.com/thumb/12345.mp4",
		"https://cdn.example.com/a/preview.mp4",
		"https://cdn.example.com/a/clip-preview-2.mp4",
		"https://cdn.example.com/a/sprite.jpg",
		"https://cdn.example.com/a/poster.jpg",
		"https://cdn.example.com/a/teaser.mp4",
		"https://thumb-01.example.com/a/whatever.mp4",
		"https://img.example.com/a/whatever.mp4",
		"https://static.example.com/a/whatever.mp4",
	}
	for _, u := range previews {
		if !IsPreviewURL(u) {
			t.Errorf("IsPreviewURL(%q) = false, want it recognised", u)
		}
	}
	// The other direction matters more: calling the real video a preview
	// would leave nothing to download.
	videos := []string{
		"https://cdn.example.com/vid/zybsmdxxju_1729521085407-video.mp4",
		"https://cdn.example.com/720p.mp4",
		"https://cdn.example.com/a/full-movie.mp4",
		"https://cdn.example.com/a/thumbprint-of-a-crime.mp4", // a word starting with it
		"https://media.example.com/postproduction.mp4",        // and one containing it
		"https://cdn.example.com/master.m3u8",
		"://",
	}
	for _, u := range videos {
		if IsPreviewURL(u) {
			t.Errorf("IsPreviewURL(%q) = true, want it kept", u)
		}
	}
}

// TestWithoutPreviewsKeepsTheVideo covers the shape a scanned page really has:
// one video and a dozen previews of its neighbours.
func TestWithoutPreviewsKeepsTheVideo(t *testing.T) {
	in := []media.Format{
		{ID: "a", URL: "https://cdn.example.com/vid/zybsmdxxju-video.mp4"},
		{ID: "b", URL: "https://cdn.example.com/vid/thumb_vid.mp4"},
		{ID: "c", URL: "https://cdn.example.com/other/thumb_vid.mp4"},
	}
	got := WithoutPreviews(in)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("kept %+v, want only the video", got)
	}

	// A page whose every candidate looks like a preview keeps them all:
	// something to try beats a refusal nobody can act on.
	all := []media.Format{
		{ID: "x", URL: "https://img.example.com/a/1.mp4"},
		{ID: "y", URL: "https://img.example.com/a/2.mp4"},
	}
	if got := WithoutPreviews(all); len(got) != 2 {
		t.Fatalf("kept %+v, want everything when nothing else is on offer", got)
	}
	if got := WithoutPreviews(nil); len(got) != 0 {
		t.Fatalf("kept %+v from nothing", got)
	}
}
