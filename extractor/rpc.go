// Copyright (c) 2026, the go-extractors authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package extractor

import (
	"fmt"
	"net/rpc"

	"github.com/go-extractors/extractors/media"
	goplugin "github.com/hashicorp/go-plugin"
)

// PluginName is the key under which the extractor is dispensed.
const PluginName = "extractor"

// ProtocolVersion must be bumped whenever the Extractor interface changes in a
// way the other side cannot tolerate. go-plugin refuses a mismatch, which is
// the whole point of the handshake.
const ProtocolVersion = 2

// Handshake is shared by the host and every plugin. A binary that does not
// print this cookie is not an extractor plugin, and go-plugin says so instead
// of hanging.
//
// It names the protocol, not the tool: a plugin is written against this
// framework and can therefore be installed under any host's plugin prefix. The
// tool name decides where plugins live and how they are named, never whether
// they can talk to each other.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "GO_EXTRACTORS_PLUGIN",
	MagicCookieValue: "b6f1e2a4-go-extractors-plugin-v2",
}

// PluginMap is the dispense table used on both sides.
func PluginMap(impl Extractor) map[string]goplugin.Plugin {
	return map[string]goplugin.Plugin{PluginName: &ExtractorPlugin{Impl: impl}}
}

// ExtractorPlugin adapts an Extractor to go-plugin's net/rpc transport. The
// gob transport keeps the plugin free of any protobuf toolchain, and the
// Extractor surface is small enough not to need gRPC streaming.
type ExtractorPlugin struct {
	Impl Extractor
}

// Server is called in the plugin process.
func (p *ExtractorPlugin) Server(*goplugin.MuxBroker) (any, error) {
	if p.Impl == nil {
		return nil, fmt.Errorf("extractor: plugin served with a nil implementation")
	}
	return &rpcServer{impl: p.Impl}, nil
}

// Client is called in the host process.
func (p *ExtractorPlugin) Client(_ *goplugin.MuxBroker, c *rpc.Client) (any, error) {
	return &rpcClient{client: c}, nil
}

// rpcServer exposes the Extractor with net/rpc method shapes.
type rpcServer struct {
	impl Extractor
}

func (s *rpcServer) Info(_ Empty, resp *Info) error {
	info, err := s.impl.Info()
	if err != nil {
		return err
	}
	*resp = info
	return nil
}

func (s *rpcServer) Match(rawURL string, resp *Claim) error {
	claim, err := s.impl.Match(rawURL)
	if err != nil {
		return err
	}
	*resp = claim
	return nil
}

func (s *rpcServer) List(req Request, resp *Playlist) error {
	pl, err := s.impl.List(req)
	if err != nil {
		return err
	}
	if pl == nil {
		return fmt.Errorf("extractor: plugin returned no playlist and no error")
	}
	*resp = *pl
	return nil
}

func (s *rpcServer) Extract(req Request, resp *media.Media) error {
	m, err := s.impl.Extract(req)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("extractor: plugin returned no media and no error")
	}
	*resp = *m
	return nil
}

// rpcClient is the host-side Extractor talking to the plugin process.
type rpcClient struct {
	client *rpc.Client
}

func (c *rpcClient) Info() (Info, error) {
	var out Info
	if err := c.client.Call("Plugin.Info", Empty{}, &out); err != nil {
		return Info{}, fmt.Errorf("extractor: Info: %w", err)
	}
	return out, nil
}

func (c *rpcClient) Match(rawURL string) (Claim, error) {
	var out Claim
	if err := c.client.Call("Plugin.Match", rawURL, &out); err != nil {
		return Claim{}, fmt.Errorf("extractor: Match: %w", err)
	}
	return out, nil
}

func (c *rpcClient) List(req Request) (*Playlist, error) {
	var out Playlist
	if err := c.client.Call("Plugin.List", req, &out); err != nil {
		return nil, fmt.Errorf("extractor: List: %w", err)
	}
	return &out, nil
}

func (c *rpcClient) Extract(req Request) (*media.Media, error) {
	var out media.Media
	if err := c.client.Call("Plugin.Extract", req, &out); err != nil {
		return nil, fmt.Errorf("extractor: Extract: %w", err)
	}
	return &out, nil
}

// serve is the seam onto go-plugin, so the configuration Serve builds can be
// inspected without handing the process over to a plugin server.
var serve = goplugin.Serve

// Serve runs impl as an extractor plugin. It is the whole body of a plugin's
// main: it never returns until the host closes the connection.
func Serve(impl Extractor) {
	serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         PluginMap(impl),
	})
}
