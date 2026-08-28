// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-extractors/extractors/extractor"
	"github.com/go-extractors/extractors/internal/testplugin"
	"github.com/go-extractors/extractors/media"
	"github.com/go-streamkit/streamkit/httpx"
	goplugin "github.com/hashicorp/go-plugin"
)

// tool is the host name this suite runs under. The framework knows no
// application name, so the tests have to pick one, and everything they look
// for is derived from it exactly as a real host would derive it.
const tool = extractor.Tool("sampletool")

// exeSuffix is what an executable is called here. It matters: on Windows,
// discovery only accepts a ".exe", so the test binaries must carry one.
var exeSuffix = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()

// pluginName is the file name a plugin for site must have to be discovered.
func pluginName(site string) string { return tool.BinaryPrefix() + site + exeSuffix }

var (
	// pluginBin is the test plugin, built once for the whole suite.
	pluginBin string
	// notPluginBin is an executable that is not a plugin at all: it prints
	// no handshake and exits, which is what a host must survive.
	notPluginBin string
	// wrongPluginBin is a well-behaved go-plugin server that dispenses its
	// extractor under a name this framework never asks for.
	wrongPluginBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "extractor-plugins")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pluginBin = filepath.Join(dir, pluginName("testsite"))
	notPluginBin = filepath.Join(dir, "notaplugin"+exeSuffix)
	wrongPluginBin = filepath.Join(dir, "wrongplugin"+exeSuffix)
	const pkg = "github.com/go-extractors/extractors/internal/testplugin/cmd/"
	for _, b := range []struct{ out, path string }{
		{pluginBin, pkg + "sampletool-plugin-testsite"},
		{notPluginBin, pkg + "notaplugin"},
		{wrongPluginBin, pkg + "wrongplugin"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.path)
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n", b.path, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeExtractor is an in-process Extractor used for the RPC round trip.
type fakeExtractor struct {
	info    extractor.Info
	infoErr error
	claim   extractor.Claim
	matchEr error
	m       *media.Media
	extErr  error
	pl      *extractor.Playlist
	listErr error
	gotReq  chan extractor.Request
}

func (f *fakeExtractor) Info() (extractor.Info, error) { return f.info, f.infoErr }

func (f *fakeExtractor) Match(string) (extractor.Claim, error) { return f.claim, f.matchEr }

func (f *fakeExtractor) List(r extractor.Request) (*extractor.Playlist, error) {
	if f.gotReq != nil {
		f.gotReq <- r
	}
	return f.pl, f.listErr
}
func (f *fakeExtractor) Extract(r extractor.Request) (*media.Media, error) {
	if f.gotReq != nil {
		f.gotReq <- r
	}
	return f.m, f.extErr
}

// dispense wires an in-process client and server over go-plugin's net/rpc
// transport, which is exactly the transport a real plugin uses.
func dispense(t *testing.T, impl extractor.Extractor) extractor.Extractor {
	t.Helper()
	client, server := goplugin.TestPluginRPCConn(t, extractor.PluginMap(impl), nil)
	t.Cleanup(func() { client.Close() })
	_ = server
	raw, err := client.Dispense(extractor.PluginName)
	if err != nil {
		t.Fatalf("Dispense: %v", err)
	}
	ext, ok := raw.(extractor.Extractor)
	if !ok {
		t.Fatalf("dispensed %T, not an Extractor", raw)
	}
	return ext
}

func TestRPCRoundTrip(t *testing.T) {
	want := &media.Media{
		Site: "s", ID: "1", Title: "T", Uploader: "U",
		Duration: 42 * time.Second, Thumbnail: "th", WebpageURL: "w", AgeLimit: 18,
		Formats: []media.Format{{
			ID: "http-720p", URL: "https://v/720", Ext: "mp4", Protocol: media.ProtocolHTTP,
			Width: 1280, Height: 720, FPS: 29.97, Bitrate: 1234, Size: 99,
			Quality: "720p", Note: "n", Headers: map[string]string{"X": "y"},
		}},
	}
	impl := &fakeExtractor{
		info:   extractor.Info{Name: "s", Version: "1", Description: "d", Hosts: []string{"h"}},
		claim:  extractor.Claim{Handles: true, Kind: "video"},
		m:      want,
		gotReq: make(chan extractor.Request, 1),
	}
	ext := dispense(t, impl)

	info, err := ext.Info()
	if err != nil || info.Name != "s" || info.Hosts[0] != "h" {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	claim, err := ext.Match("https://x/videos/1")
	if err != nil || !claim.Handles || claim.Playlist || claim.Kind != "video" {
		t.Fatalf("Match = %+v, %v", claim, err)
	}
	req := extractor.Request{URL: "https://x/videos/1", HTTP: httpx.Config{
		UserAgent: "ua", Cookies: []string{"a=1"}, Proxy: "http://p", Timeout: time.Second,
		Retries: 2, Backoff: time.Millisecond, MaxBodyBytes: 10,
	}}
	got, err := ext.Extract(req)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Every field must survive the gob round trip in both directions.
	sent := <-impl.gotReq
	if sent.URL != req.URL || sent.HTTP.UserAgent != "ua" || sent.HTTP.Cookies[0] != "a=1" ||
		sent.HTTP.Timeout != time.Second || sent.HTTP.Retries != 2 || sent.HTTP.MaxBodyBytes != 10 {
		t.Fatalf("plugin received %+v", sent)
	}
	if got.Title != want.Title || got.Duration != want.Duration || got.AgeLimit != 18 {
		t.Fatalf("media = %+v", got)
	}
	f := got.Formats[0]
	w := want.Formats[0]
	if f.ID != w.ID || f.URL != w.URL || f.Ext != w.Ext || f.Protocol != w.Protocol ||
		f.Width != w.Width || f.Height != w.Height || f.FPS != w.FPS ||
		f.Bitrate != w.Bitrate || f.Size != w.Size || f.Quality != w.Quality ||
		f.Note != w.Note || f.Headers["X"] != "y" {
		t.Fatalf("format = %+v, want %+v", f, w)
	}
}

func TestRPCErrorsCrossTheBoundary(t *testing.T) {
	ext := dispense(t, &fakeExtractor{
		infoErr: errors.New("no info"),
		matchEr: errors.New("no match"),
		extErr:  fmt.Errorf("%w: nothing here", extractor.ErrNoFormats),
		listErr: fmt.Errorf("%w: single video", extractor.ErrNotAPlaylist),
	})
	if _, err := ext.Info(); err == nil || !strings.Contains(err.Error(), "no info") {
		t.Errorf("Info error = %v", err)
	}
	if _, err := ext.Match("x"); err == nil || !strings.Contains(err.Error(), "no match") {
		t.Errorf("Match error = %v", err)
	}
	if _, err := ext.List(extractor.Request{URL: "x"}); err == nil {
		t.Error("List error was swallowed")
	}
	_, err := ext.Extract(extractor.Request{URL: "x"})
	if err == nil {
		t.Fatal("Extract error was swallowed")
	}
	// errors.Is cannot survive gob, MatchError must.
	if errors.Is(err, extractor.ErrNoFormats) {
		t.Log("errors.Is happened to work")
	}
	if !extractor.MatchError(err, extractor.ErrNoFormats) {
		t.Errorf("MatchError(%v, ErrNoFormats) = false", err)
	}
}

func TestRPCNilMediaIsAnError(t *testing.T) {
	ext := dispense(t, &fakeExtractor{})
	if _, err := ext.Extract(extractor.Request{URL: "x"}); err == nil {
		t.Fatal("a nil media with no error was accepted")
	}
	if _, err := ext.List(extractor.Request{URL: "x"}); err == nil {
		t.Fatal("a nil playlist with no error was accepted")
	}
}

func TestRPCPlaylistRoundTrip(t *testing.T) {
	want := &extractor.Playlist{
		ID: "creator", Title: "A Creator", Uploader: "creator",
		WebpageURL: "https://x/creators/creator",
		Entries: []extractor.Entry{
			{URL: "https://x/videos/1", ID: "1", Title: "One", Duration: 90 * time.Second, Kind: "video"},
			{URL: "https://x/shorts/2", ID: "2", Title: "Two", Kind: "short"},
		},
	}
	impl := &fakeExtractor{
		claim:  extractor.Claim{Handles: true, Playlist: true, Kind: "creator"},
		pl:     want,
		gotReq: make(chan extractor.Request, 1),
	}
	ext := dispense(t, impl)
	claim, err := ext.Match("https://x/creators/creator")
	if err != nil || !claim.Playlist || claim.Kind != "creator" {
		t.Fatalf("Match = %+v, %v", claim, err)
	}
	got, err := ext.List(extractor.Request{URL: "https://x/creators/creator"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Title != want.Title || len(got.Entries) != 2 {
		t.Fatalf("playlist = %+v", got)
	}
	if e := got.Entries[0]; e.URL != want.Entries[0].URL || e.Duration != 90*time.Second || e.Kind != "video" {
		t.Fatalf("entry 0 = %+v", e)
	}
	if got.Entries[1].Kind != "short" {
		t.Fatalf("entry 1 = %+v", got.Entries[1])
	}
	<-impl.gotReq
}

func TestServerRejectsANilImplementation(t *testing.T) {
	p := &extractor.ExtractorPlugin{}
	if _, err := p.Server(nil); err == nil {
		t.Fatal("serving a nil implementation succeeded")
	}
	if _, err := (&extractor.ExtractorPlugin{Impl: &fakeExtractor{}}).Server(nil); err != nil {
		t.Fatalf("Server: %v", err)
	}
	if _, err := (&extractor.ExtractorPlugin{}).Client(nil, nil); err != nil {
		t.Fatalf("Client: %v", err)
	}
}

func TestMatchError(t *testing.T) {
	if !extractor.MatchError(nil, nil) {
		t.Error("MatchError(nil, nil) = false")
	}
	if extractor.MatchError(nil, extractor.ErrNotFound) {
		t.Error("MatchError(nil, target) = true")
	}
	if extractor.MatchError(extractor.ErrNotFound, nil) {
		t.Error("MatchError(err, nil) = true")
	}
	wrapped := fmt.Errorf("layer: %w", extractor.ErrGeoBlocked)
	if !extractor.MatchError(wrapped, extractor.ErrGeoBlocked) {
		t.Error("wrapped sentinel not matched")
	}
	flattened := errors.New("extractor: rpc error: " + extractor.ErrPaywalled.Error())
	if !extractor.MatchError(flattened, extractor.ErrPaywalled) {
		t.Error("flattened sentinel not matched")
	}
	if extractor.MatchError(errors.New("something else"), extractor.ErrUnsupported) {
		t.Error("unrelated error matched")
	}
}

func TestHandshakeConstants(t *testing.T) {
	if extractor.Handshake.ProtocolVersion != extractor.ProtocolVersion ||
		extractor.Handshake.MagicCookieKey == "" || extractor.Handshake.MagicCookieValue == "" {
		t.Fatalf("handshake = %+v", extractor.Handshake)
	}
	if _, ok := extractor.PluginMap(nil)[extractor.PluginName]; !ok {
		t.Fatalf("PluginMap misses %q", extractor.PluginName)
	}
}

// --- host side ---------------------------------------------------------------

func pluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	install(t, pluginBin, filepath.Join(dir, filepath.Base(pluginBin)))
	return dir
}

// install puts an already built binary where a test needs it. A hard link
// costs nothing; copying is the fallback for the case where the two temporary
// directories are not on the same volume. A symbolic link is deliberately not
// used: creating one needs a privilege on Windows.
func install(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Link(src, dst); err == nil {
		return
	}
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("install %s: %v", filepath.Base(dst), err)
	}
}

// newHost builds the Host under test, for the suite's tool.
func newHost(t *testing.T, dirs []string, out io.Writer, level string) *extractor.Host {
	t.Helper()
	h, err := extractor.NewHost(tool, dirs, out, level)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	return h
}

func TestHostOpenAndExtract(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	paths := host.Discover()
	if len(paths) == 0 {
		t.Fatal("Discover found nothing")
	}
	inst, err := host.Open(paths[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer inst.Close()
	if inst.Info.Name != "testsite" || inst.Info.Version != "9.9.9" {
		t.Fatalf("Info = %+v", inst.Info)
	}
	claim, err := inst.Match("http://127.0.0.1:1/videos/1")
	if err != nil || !claim.Handles {
		t.Fatalf("Match = %+v, %v", claim, err)
	}
	claim, err = inst.Match("https://example.com/x")
	if err != nil || claim.Handles {
		t.Fatalf("Match(other host) = %+v, %v", claim, err)
	}
	inst.Close() // idempotent
}

func TestHostDiscover(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	// A plugin in each directory, same base name: the first must win.
	for _, d := range []string{dir1, dir2} {
		install(t, pluginBin, filepath.Join(d, pluginName("testsite")))
	}
	// Noise that must be ignored: a binary under another name, a file that
	// this system cannot run, and a directory named like a plugin.
	if err := os.WriteFile(filepath.Join(dir1, "not-a-plugin"+exeSuffix), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir1, tool.BinaryPrefix()+"unrunnable"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir1, pluginName("dir")), 0o755); err != nil {
		t.Fatal(err)
	}
	host := newHost(t, []string{dir1, dir2, filepath.Join(dir1, "missing")}, io.Discard, "debug")
	got := host.Discover()
	if len(got) != 1 {
		t.Fatalf("Discover = %v, want exactly one plugin", got)
	}
	if filepath.Dir(got[0]) != dir1 {
		t.Fatalf("Discover = %q, want the one in the first directory", got[0])
	}
}

// TestHostSearchesTheToolsOwnEnvironmentVariable proves the search path a Host
// inherits is the one named after its tool, not a fixed one.
func TestHostSearchesTheToolsOwnEnvironmentVariable(t *testing.T) {
	dir := t.TempDir()
	install(t, pluginBin, filepath.Join(dir, pluginName("testsite")))
	t.Setenv(tool.EnvPluginPath(), dir)
	// Another tool's variable must not be read.
	t.Setenv(extractor.Tool("othertool").EnvPluginPath(), t.TempDir())
	host := newHost(t, nil, nil, "")
	got := host.Discover()
	if len(got) == 0 || filepath.Dir(got[0]) != dir {
		t.Fatalf("Discover = %v, want the plugin in %s", got, dir)
	}
	// A plugin under another tool's prefix is not this tool's plugin.
	other := filepath.Join(t.TempDir(), "othertool-plugin-testsite"+exeSuffix)
	install(t, pluginBin, other)
	t.Setenv(tool.EnvPluginPath(), filepath.Dir(other))
	if got := newHost(t, nil, nil, "").Discover(); len(got) != 0 {
		t.Fatalf("Discover = %v, want nothing: the prefix is another tool's", got)
	}
}

func TestHostResolveFindsThePluginThatClaimsTheURL(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "off")
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	if inst.Info.Name != "testsite" {
		t.Fatalf("resolved %q", inst.Info.Name)
	}
}

func TestHostResolveWithoutAnyPlugin(t *testing.T) {
	host := newHost(t, []string{t.TempDir()}, nil, "")
	t.Setenv(tool.EnvPluginPath(), t.TempDir())
	_, err := host.Resolve("https://example.com/videos/1")
	if !errors.Is(err, extractor.ErrNoPlugin) {
		t.Fatalf("err = %v, want ErrNoPlugin", err)
	}
}

func TestHostResolveWhenNoPluginClaimsTheURL(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	_, err := host.Resolve("https://example.com/videos/1")
	if !errors.Is(err, extractor.ErrNoPlugin) {
		t.Fatalf("err = %v, want ErrNoPlugin", err)
	}
}

func TestHostSkipsABrokenPluginAndUsesTheGoodOne(t *testing.T) {
	dir := pluginDir(t)
	// A binary that is not an extractor plugin at all: the handshake must fail
	// without taking the working plugin down with it.
	broken := filepath.Join(dir, pluginName("broken"))
	install(t, notPluginBin, broken)
	host := newHost(t, []string{dir}, nil, "")
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	if inst.Info.Name != "testsite" {
		t.Fatalf("resolved %q", inst.Info.Name)
	}
	// Open on the broken binary must report the handshake failure.
	if _, err := host.Open(broken); err == nil {
		t.Fatal("opening a non-plugin binary succeeded")
	}
	if _, err := host.Open(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("opening a missing binary succeeded")
	}
}

// TestHostOpenReportsAPluginThatDispensesSomethingElse covers the one failure
// between a successful handshake and a working Extractor: a binary that is a
// plugin, but not one of ours.
func TestHostOpenReportsAPluginThatDispensesSomethingElse(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, pluginName("wrongname"))
	install(t, wrongPluginBin, bin)
	host := newHost(t, []string{dir}, nil, "")
	inst, err := host.Open(bin)
	if err == nil {
		inst.Close()
		t.Fatal("a plugin that dispenses something else was accepted")
	}
	if !strings.Contains(err.Error(), "dispense") || !strings.Contains(err.Error(), filepath.Base(bin)) {
		t.Fatalf("err = %v, want a dispense failure naming the binary", err)
	}
}

// TestResolveRanksClaimingPluginsByPriority covers what happens when more than
// one plugin says yes: the highest priority wins whatever the order they were
// discovered in, and the losers are shut down rather than leaked.
func TestResolveRanksClaimingPluginsByPriority(t *testing.T) {
	dir := t.TempDir()
	for _, site := range []string{"aaa", "bbb", "ccc"} {
		install(t, pluginBin, filepath.Join(dir, pluginName(site)))
	}
	// Every copy claims every URL, so only the priority separates them.
	// All three are negative, which is what keeps the host asking the next
	// one instead of stopping at the first claim; and the winner is neither
	// the first nor the last tried.
	t.Setenv(testplugin.EnvMatchAll, "1")
	t.Setenv(testplugin.EnvPriority, "aaa=-2,bbb=-1,ccc=-3")
	host := newHost(t, []string{dir}, nil, "")
	host.SetCachePath("")
	inst, err := host.Resolve("https://example.com/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	if inst.Info.Name != "bbb" {
		t.Fatalf("resolved %q, want the highest priority plugin", inst.Info.Name)
	}
	if inst.Info.Priority != -1 {
		t.Fatalf("priority = %d, want -1", inst.Info.Priority)
	}
	// The two that lost must be gone: what is still running would answer.
	if _, err := inst.Match("https://example.com/videos/1"); err != nil {
		t.Fatalf("the winner is not usable: %v", err)
	}
}

func TestHostListReportsBrokenPlugins(t *testing.T) {
	dir := pluginDir(t)
	broken := filepath.Join(dir, pluginName("broken"))
	install(t, notPluginBin, broken)
	host := newHost(t, []string{dir}, nil, "")
	infos, err := host.List()
	if err == nil {
		t.Error("List hid the broken plugin")
	}
	if len(infos) != 1 || infos[0].Name != "testsite" {
		t.Fatalf("List = %+v", infos)
	}
}

func TestHostOpenReportsAFailingInfo(t *testing.T) {
	t.Setenv(testplugin.EnvFailInfo, "1")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	_, err := host.Open(filepath.Join(pluginDir(t), filepath.Base(pluginBin)))
	if err == nil || !strings.Contains(err.Error(), "Info failed on purpose") {
		t.Fatalf("err = %v, want the plugin's Info failure", err)
	}
}

func TestHostResolveReportsAFailingMatch(t *testing.T) {
	t.Setenv(testplugin.EnvFailMatch, "1")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	_, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err == nil || !strings.Contains(err.Error(), "Match failed on purpose") {
		t.Fatalf("err = %v, want the plugin's Match failure", err)
	}
}

func TestInstanceExtractSucceedsOverARealPluginProcess(t *testing.T) {
	// The plugin process fetches this page itself, which proves the HTTP
	// config really crosses the RPC boundary.
	page := `{"id":"7","title":"Real Extraction","uploader":"Ann","duration":90000000000,
		"formats":[
			{"id":"http-360p","url":"https://cdn/360.mp4","ext":"mp4","protocol":"http","height":360},
			{"id":"http-1080p","url":"https://cdn/1080.mp4","ext":"mp4","protocol":"http","height":1080}
		]}`
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		io.WriteString(w, page)
	}))
	defer srv.Close()

	host := newHost(t, []string{pluginDir(t)}, nil, "")
	inst, err := host.Resolve(srv.URL + "/videos/real-7")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	m, err := inst.Extract(context.Background(), extractor.Request{
		URL:  srv.URL + "/videos/real-7",
		HTTP: httpx.Config{UserAgent: "host-e2e", Retries: 1},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if m.Title != "Real Extraction" || m.ID != "7" || m.Uploader != "Ann" {
		t.Fatalf("media = %+v", m)
	}
	if m.Duration != 90*time.Second {
		t.Errorf("Duration = %v", m.Duration)
	}
	if m.Site != "testsite" || m.WebpageURL == "" {
		t.Errorf("Site = %q, WebpageURL = %q", m.Site, m.WebpageURL)
	}
	// Extract sorts worst-first, so the last format is the best one.
	if n := len(m.Formats); n != 2 {
		t.Fatalf("got %d formats", n)
	}
	if m.Formats[0].Height != 360 || m.Formats[1].Height != 1080 {
		t.Fatalf("formats are not sorted worst-first: %+v", m.Formats)
	}
	if gotUA != "host-e2e" {
		t.Errorf("the plugin used User-Agent %q, want the host's", gotUA)
	}
}

func TestInstanceExtractRejectsAFormatlessMedia(t *testing.T) {
	t.Setenv(testplugin.EnvNoFormats, "1")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	_, err = inst.Extract(context.Background(), extractor.Request{URL: "http://127.0.0.1:9/videos/1"})
	if !extractor.MatchError(err, extractor.ErrNoFormats) {
		t.Fatalf("err = %v, want ErrNoFormats", err)
	}
}

func TestInstanceExtractReportsAPluginError(t *testing.T) {
	t.Setenv(testplugin.EnvNil, "1")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	if _, err := inst.Extract(context.Background(), extractor.Request{URL: "x"}); err == nil {
		t.Fatal("a nil media was accepted")
	}
}

func TestInstanceExtractHonoursTheContext(t *testing.T) {
	t.Setenv(testplugin.EnvSlow, "5s")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = inst.Extract(ctx, extractor.Request{URL: "http://127.0.0.1:9/videos/1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Extract waited %s for the plugin instead of giving up", elapsed)
	}
}

func copyExecutable(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}

// TestResolveUsesTheURLToPickThePlugin proves the point of the index: once a
// plugin has said which hosts it serves, the others are not started at all.
func TestResolveUsesTheURLToPickThePlugin(t *testing.T) {
	dir := pluginDir(t)
	marker := filepath.Join(dir, "was-launched")
	// A binary that sorts before the real plugin and leaves a trace when it
	// runs. It is not an extractor plugin, so its handshake fails.
	install(t, notPluginBin, filepath.Join(dir, pluginName("aaa-noisy")))
	t.Setenv(testplugin.EnvMarker, marker)
	cache := filepath.Join(t.TempDir(), "plugins.json")

	// First pass: nothing is known yet, so every plugin is asked.
	host := newHost(t, []string{dir}, nil, "")
	host.SetCachePath(cache)
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inst.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the first pass should have tried every plugin: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	// Second pass: the URL names the plugin, so the noisy one stays asleep.
	host2 := newHost(t, []string{dir}, nil, "")
	host2.SetCachePath(cache)
	inst2, err := host2.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst2.Close()
	if inst2.Info.Name != "testsite" {
		t.Fatalf("resolved %q", inst2.Info.Name)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a plugin that cannot serve this URL was started anyway")
	}
}

func TestResolveFallsBackWhenTheIndexIsWrong(t *testing.T) {
	dir := pluginDir(t)
	cache := filepath.Join(t.TempDir(), "plugins.json")
	host := newHost(t, []string{dir}, nil, "")
	host.SetCachePath(cache)
	// Warm the index up.
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inst.Close()
	// A host the index does not know still resolves, through the full pass.
	t.Setenv(testplugin.EnvMatchAll, "1")
	host2 := newHost(t, []string{dir}, nil, "")
	host2.SetCachePath(cache)
	inst2, err := host2.Resolve("https://elsewhere.example/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst2.Close()
	if inst2.Info.Name != "testsite" {
		t.Fatalf("resolved %q", inst2.Info.Name)
	}
}

func TestResolveWithACandidateThatRefusesTheURL(t *testing.T) {
	dir := pluginDir(t)
	cache := filepath.Join(t.TempDir(), "plugins.json")
	host := newHost(t, []string{dir}, nil, "")
	host.SetCachePath(cache)
	inst, err := host.Resolve("http://127.0.0.1:9/videos/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inst.Close()
	// The index says this plugin serves loopback, but Match refuses this
	// particular URL: the run must end on ErrNoPlugin, not on a wrong plugin.
	t.Setenv(testplugin.EnvFailMatch, "1")
	host2 := newHost(t, []string{dir}, nil, "")
	host2.SetCachePath(cache)
	if _, err := host2.Resolve("http://127.0.0.1:9/videos/1"); err == nil {
		t.Fatal("a refusing plugin was used anyway")
	}
}

func TestInstanceListOverARealPluginProcess(t *testing.T) {
	page := `{"ID":"c","Title":"A Creator","Uploader":"creator","Entries":[
		{"URL":"http://127.0.0.1/videos/1","ID":"1","Title":"One","Duration":60000000000,"Kind":"video"},
		{"URL":"http://127.0.0.1/shorts/2","ID":"2","Title":"Two","Kind":"short"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page)
	}))
	defer srv.Close()
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	host.SetCachePath("")
	inst, err := host.Resolve(srv.URL + "/list/creator")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	if !inst.Claim.Playlist {
		t.Fatalf("claim = %+v, want a playlist", inst.Claim)
	}
	pl, err := inst.List(context.Background(), extractor.Request{URL: srv.URL + "/list/creator"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if pl.Title != "A Creator" || len(pl.Entries) != 2 {
		t.Fatalf("playlist = %+v", pl)
	}
	if pl.Entries[0].Duration != time.Minute || pl.Entries[1].Kind != "short" {
		t.Fatalf("entries = %+v", pl.Entries)
	}
}

func TestInstanceListRejectsAnEmptyPlaylist(t *testing.T) {
	t.Setenv(testplugin.EnvEmptyList, "1")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	host.SetCachePath("")
	inst, err := host.Resolve("http://127.0.0.1:9/list/c")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	_, err = inst.List(context.Background(), extractor.Request{URL: "http://127.0.0.1:9/list/c"})
	if !extractor.MatchError(err, extractor.ErrEmptyPlaylist) {
		t.Fatalf("err = %v, want ErrEmptyPlaylist", err)
	}
}

func TestInstanceListReportsAPluginError(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	host.SetCachePath("")
	inst, err := host.Resolve("http://127.0.0.1:9/list/c")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	// The page is unreachable, so the plugin fails and says so.
	if _, err := inst.List(context.Background(), extractor.Request{URL: "http://127.0.0.1:1/list/c"}); err == nil {
		t.Fatal("an unreachable listing was accepted")
	}
}

func TestInstanceListHonoursTheContext(t *testing.T) {
	t.Setenv(testplugin.EnvSlow, "5s")
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	host.SetCachePath("")
	inst, err := host.Resolve("http://127.0.0.1:9/list/c")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer inst.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := inst.List(ctx, extractor.Request{URL: "http://127.0.0.1:9/list/c"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("List waited %s instead of giving up", elapsed)
	}
}

func TestListPrimesTheIndex(t *testing.T) {
	dir := pluginDir(t)
	cache := filepath.Join(t.TempDir(), "plugins.json")
	host := newHost(t, []string{dir}, nil, "")
	host.SetCachePath(cache)
	if _, err := host.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	body, err := os.ReadFile(cache)
	if err != nil {
		t.Fatalf("listing the plugins did not write the index: %v", err)
	}
	if !strings.Contains(string(body), "testsite") {
		t.Fatalf("index = %s", body)
	}
}

// TestPlaylistJSONNamesMatchMedia checks a listing and a video are printed in
// the same spelling: a script reading a host's JSON output should not have to
// know which of the two it asked about.
func TestPlaylistJSONNamesMatchMedia(t *testing.T) {
	encoded, err := json.Marshal(extractor.Playlist{
		ID: "someone", Title: "Someone", Uploader: "someone",
		WebpageURL: "https://example.com/c/someone",
		Entries: []extractor.Entry{{
			URL: "https://example.com/v/1", ID: "1", Title: "One",
			Duration: 90 * time.Second, Kind: "video", Size: 1 << 20,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "title", "uploader", "webpage_url", "entries"} {
		if _, ok := got[name]; !ok {
			t.Errorf("the listing states no %q: %s", name, encoded)
		}
	}
	entries, ok := got["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v", got["entries"])
	}
	entry := entries[0].(map[string]any)
	for _, name := range []string{"url", "id", "title", "duration", "kind", "size"} {
		if _, ok := entry[name]; !ok {
			t.Errorf("the entry states no %q: %s", name, encoded)
		}
	}
	// What a listing does not know is left out rather than stated as zero.
	bare, err := json.Marshal(extractor.Entry{URL: "https://example.com/v/2", ID: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "duration") || strings.Contains(string(bare), "size") {
		t.Errorf("a bare entry states what it does not know: %s", bare)
	}
}

// TestInfoSurvivesTheWire covers the boundary a plugin's own description
// crosses: it is another process, and what it answers is encoded with gob. A
// field that does not survive arrives zero, which reads exactly like a plugin
// that chose to say nothing — no error, no failure, and the host quietly
// decides for itself.
//
// That is not hypothetical. A host built before a field existed dropped it on
// arrival, so a ceiling the plugin stated was never applied and the download
// it was meant to protect went on failing. Written by reflection, so a field
// added later is covered without anyone remembering to come back here.
func TestInfoSurvivesTheWire(t *testing.T) {
	var sent extractor.Info
	v := reflect.ValueOf(&sent).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		name := v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.String:
			f.SetString("v-" + name)
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				t.Fatalf("field %s holds %s, which this test does not know how to fill: teach it",
					name, f.Type().Elem().Kind())
			}
			f.Set(reflect.ValueOf([]string{"v-" + name}))
		default:
			t.Fatalf("field %s is a %s, which this test does not know how to fill: teach it", name, f.Kind())
		}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(sent); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got extractor.Info
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(sent, got) {
		t.Fatalf("an Info did not survive the wire:\n sent %+v\n got  %+v", sent, got)
	}
	// Stated separately: DeepEqual on two zero values would pass while
	// saying nothing at all.
	out := reflect.ValueOf(got)
	for i := range out.NumField() {
		if out.Field(i).IsZero() {
			t.Fatalf("field %s arrived zero", out.Type().Field(i).Name)
		}
	}
}

// TestNothingIsLeftRunningWhenTheHostStops covers the plugin processes a host
// started outliving it.
//
// Only the holder of an Instance could end one, so a caller that stops between
// opening and closing — a worker taking a signal, most of all — left them
// running: two were found alive twelve hours after the process that started
// them had gone, reparented to init, holding their memory and their
// connections.
func TestNothingIsLeftRunningWhenTheHostStops(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	paths := host.Discover()
	if len(paths) == 0 {
		t.Fatal("Discover found nothing")
	}
	inst, err := host.Open(paths[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Deliberately never closed: that is the case this exists for.
	if _, err := inst.Match("https://example.com/watch/1"); err != nil {
		t.Fatalf("the plugin was not answering to begin with: %v", err)
	}

	if n := host.Shutdown(); n != 1 {
		t.Errorf("Shutdown ended %d plugins, want the 1 that was open", n)
	}
	if _, err := inst.Match("https://example.com/watch/1"); err == nil {
		t.Error("the plugin answered after the host was stopped, so it is still running")
	}
	// Twice is safe, and there is nothing left to end.
	if n := host.Shutdown(); n != 0 {
		t.Errorf("a second Shutdown ended %d plugins, want none left", n)
	}
}

// TestAnInstanceAlreadyClosedIsNotTheHostsToEnd covers the ordinary path
// still being the one that counts: a caller that closes what it opened leaves
// the host with nothing to do.
func TestAnInstanceAlreadyClosedIsNotTheHostsToEnd(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	paths := host.Discover()
	if len(paths) == 0 {
		t.Fatal("Discover found nothing")
	}
	inst, err := host.Open(paths[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	inst.Close()
	inst.Close() // twice is safe, and must not double-count
	if n := host.Shutdown(); n != 0 {
		t.Errorf("Shutdown ended %d plugins, want none: the caller had closed it", n)
	}
}

// TestAHostThatOpenedNothingStopsQuietly covers the host nobody used.
func TestAHostThatOpenedNothingStopsQuietly(t *testing.T) {
	host := newHost(t, []string{pluginDir(t)}, nil, "")
	if n := host.Shutdown(); n != 0 {
		t.Errorf("Shutdown ended %d plugins on a host that opened none", n)
	}
}
