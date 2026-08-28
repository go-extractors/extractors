// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/go-extractors/extractors/media"
)

// ErrNoPlugin means no discovered plugin claimed the URL.
var ErrNoPlugin = errors.New("extractor: no plugin handles this URL")

// Host discovers, launches and talks to plugin processes on behalf of one
// tool. Everything application-specific about it comes from that tool name:
// see Tool.
type Host struct {
	tool      Tool
	dirs      []string
	logger    hclog.Logger
	cachePath string
	index     *pluginIndex

	// live is every plugin process this host has started and not yet been
	// told to let go of. Without it a host knows nothing of what it has
	// launched: only the holder of an Instance could end one, so a caller
	// that stops between opening and closing — a worker taking a signal,
	// most of all — leaves its plugins running, reparented to init. Two of
	// them were found alive twelve hours after the process that started
	// them had gone.
	mu   sync.Mutex
	live map[*goplugin.Client]struct{}
}

// NewHost builds a Host for tool, searching dirs first and then everything
// Tool.PluginDirs names. logOutput may be nil to silence plugin logs; level is
// an hclog level name, and an unknown one means "error".
//
// An invalid tool name — an empty one included — is an error rather than a
// default: see Tool.
func NewHost(tool Tool, dirs []string, logOutput io.Writer, level string) (*Host, error) {
	if err := tool.Validate(); err != nil {
		return nil, err
	}
	if logOutput == nil {
		logOutput = io.Discard
	}
	lvl := hclog.LevelFromString(level)
	if lvl == hclog.NoLevel {
		lvl = hclog.Error
	}
	return &Host{
		tool: tool,
		dirs: append(append([]string{}, dirs...), tool.PluginDirs()...),
		logger: hclog.New(&hclog.LoggerOptions{
			Name:   string(tool),
			Output: logOutput,
			Level:  lvl,
		}),
		cachePath: tool.CachePath(),
		index:     newIndex(),
	}, nil
}

// Tool reports the tool this Host serves.
func (h *Host) Tool() Tool { return h.tool }

// SetCachePath overrides where the plugin index is kept. An empty path
// disables it, which is what a test wants.
func (h *Host) SetCachePath(p string) { h.cachePath = p }

// Discover lists the plugin executables found in the search path. The result
// is deduplicated by base name: the first directory wins, so a local build
// shadows an installed one.
func (h *Host) Discover() []string {
	var out []string
	prefix := h.tool.BinaryPrefix()
	seen := map[string]bool{}
	for _, dir := range h.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) || seen[name] {
				continue
			}
			path := filepath.Join(dir, name)
			if !executable(path) {
				continue
			}
			seen[name] = true
			out = append(out, path)
		}
	}
	return out
}

// executable reports whether path is a binary the host could start. What
// counts as one is per-operating-system: see isExecutable.
func executable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return isExecutable(path, fi)
}

// Instance is a live plugin process.
type Instance struct {
	Path string
	Info Info
	// Claim is what the plugin answered for the URL Resolve was given.
	Claim  Claim
	ext    Extractor
	client *goplugin.Client
	// host is who to tell when this instance is done with, so a shutdown
	// does not go looking for a process that has already ended.
	host *Host
}

// Close terminates the plugin process. It is safe to call twice.
func (i *Instance) Close() {
	if i.client != nil {
		i.client.Kill()
		i.host.forget(i.client)
		i.client = nil
	}
}

// Match asks the plugin whether it claims rawURL, and what it points at.
func (i *Instance) Match(rawURL string) (Claim, error) { return i.ext.Match(rawURL) }

// List enumerates a listing URL, honouring ctx the way Extract does.
func (i *Instance) List(ctx context.Context, req Request) (*Playlist, error) {
	type outcome struct {
		pl  *Playlist
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		pl, err := i.ext.List(req)
		ch <- outcome{pl, err}
	}()
	select {
	case <-ctx.Done():
		i.Close()
		return nil, fmt.Errorf("extractor: %s: %w", i.Info.Name, ctx.Err())
	case o := <-ch:
		if o.err != nil {
			return nil, o.err
		}
		if len(o.pl.Entries) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrEmptyPlaylist, req.URL)
		}
		return o.pl, nil
	}
}

// Extract runs the extraction, honouring ctx: if the caller gives up, the
// plugin process is killed rather than left blocking on a socket read.
func (i *Instance) Extract(ctx context.Context, req Request) (*media.Media, error) {
	type outcome struct {
		m   *media.Media
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		m, err := i.ext.Extract(req)
		ch <- outcome{m, err}
	}()
	select {
	case <-ctx.Done():
		i.Close()
		return nil, fmt.Errorf("extractor: %s: %w", i.Info.Name, ctx.Err())
	case o := <-ch:
		if o.err != nil {
			return nil, o.err
		}
		if len(o.m.Formats) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrNoFormats, req.URL)
		}
		o.m.Sort()
		return o.m, nil
	}
}

// Open launches one plugin binary and dispenses its Extractor.
func (h *Host) Open(path string) (*Instance, error) {
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: Handshake,
		Plugins:         PluginMap(nil),
		Cmd:             exec.Command(path),
		Logger:          h.logger,
		// net/rpc only: no protobuf toolchain needed to build a plugin.
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolNetRPC},
	})
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("extractor: handshake with %s: %w", filepath.Base(path), err)
	}
	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("extractor: dispense %s from %s: %w", PluginName, filepath.Base(path), err)
	}
	// Dispense hands back what our own dispense table built, and PluginMap
	// only ever builds an ExtractorPlugin, whose Client is an Extractor.
	// The plugin process has no say in this type, so there is no case to
	// handle here.
	inst := &Instance{Path: path, ext: raw.(Extractor), client: client, host: h}
	h.track(client)
	info, err := inst.ext.Info()
	if err != nil {
		inst.Close()
		return nil, fmt.Errorf("extractor: %s: %w", filepath.Base(path), err)
	}
	inst.Info = info
	return inst, nil
}

// List launches every discovered plugin just long enough to read its Info. It
// also fills the index, so listing the plugins is what primes the lookup that
// later picks one from a URL alone.
// track records a plugin process this host started.
func (h *Host) track(c *goplugin.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.live == nil {
		h.live = map[*goplugin.Client]struct{}{}
	}
	h.live[c] = struct{}{}
}

// forget drops one that has already been ended.
func (h *Host) forget(c *goplugin.Client) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.live, c)
}

// Shutdown ends every plugin process this host started and still holds, and
// reports how many there were. It is safe to call twice, and safe to call
// while instances are still open: each of those is ended too.
//
// A caller that opens instances and then stops for a reason of its own has no
// other way to be sure nothing is left behind, and what is left behind does
// not die with its parent — it is reparented to init and stays, holding its
// memory and its connections.
func (h *Host) Shutdown() int {
	h.mu.Lock()
	live := h.live
	h.live = nil
	h.mu.Unlock()
	for c := range live {
		c.Kill()
	}
	return len(live)
}

func (h *Host) List() ([]Info, error) {
	var (
		infos []Info
		errs  []error
	)
	h.index = h.loadIndex()
	for _, path := range h.Discover() {
		inst, err := h.Open(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		h.remember(path, inst.Info)
		infos = append(infos, inst.Info)
		inst.Close()
	}
	h.saveIndex(h.index)
	return infos, errors.Join(errs...)
}

// Resolve returns the plugin that claims rawURL. The caller owns the returned
// Instance and must Close it.
//
// The URL alone usually names the plugin: each one declares the hosts it
// serves, and those are remembered in an index next to the binaries. Only the
// plugins whose patterns match are started; if none does, every plugin is
// asked, which is also how the index gets filled in the first place. Plugins
// that fail to start are reported only when nothing matched, so one broken
// plugin cannot break a download another plugin could serve.
func (h *Host) Resolve(rawURL string) (*Instance, error) {
	paths := h.Discover()
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no %s* binary in %s",
			ErrNoPlugin, h.tool.BinaryPrefix(), strings.Join(h.dirs, string(os.PathListSeparator)))
	}
	h.index = h.loadIndex()
	if likely := h.index.candidates(paths, rawURL); len(likely) > 0 {
		if inst, err := h.tryPaths(likely, rawURL); err == nil {
			return inst, nil
		}
	}
	inst, err := h.tryPaths(paths, rawURL)
	h.saveIndex(h.index)
	return inst, err
}

// tryPaths starts the given plugins in order and returns the one that claims
// the URL with the highest priority, remembering what each said it serves. It
// stops early on a plugin of priority zero or more: those know a site, and no
// catch-all should outrank them.
func (h *Host) tryPaths(paths []string, rawURL string) (*Instance, error) {
	var (
		errs []error
		best *Instance
	)
	for _, path := range paths {
		inst, err := h.Open(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		h.remember(path, inst.Info)
		claim, err := inst.Match(rawURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("extractor: %s: %w", inst.Info.Name, err))
			inst.Close()
			continue
		}
		if !claim.Handles {
			inst.Close()
			continue
		}
		inst.Claim = claim
		if best == nil || inst.Info.Priority > best.Info.Priority {
			if best != nil {
				best.Close()
			}
			best = inst
		} else {
			inst.Close()
		}
		if best.Info.Priority >= 0 {
			return best, nil
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, errors.Join(append(errs, fmt.Errorf("%w: %s", ErrNoPlugin, rawURL))...)
}
