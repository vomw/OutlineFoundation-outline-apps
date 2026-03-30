// Copyright 2024 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registry

import (
	"context"
	"errors"
	"net"

	"localhost/client/go/configyaml"
	"localhost/client/go/outline/config"
	"golang.getoutline.org/sdk/transport"
)

// newTypeParser is a wrapper around [configyaml.NewTypeParser] that allows us to centralize the registration
// of subparsers that should apply to all supported types.
func newTypeParser[T any](fallbackHandler func(context.Context, configyaml.ConfigNode) (T, error)) *configyaml.TypeParser[T] {
	parser := configyaml.NewTypeParser(fallbackHandler)

	// Registrations that should apply to all supported type.
	parser.RegisterSubParser("first-supported", config.NewFirstSupportedSubParser(parser.Parse))

	return parser
}

// NewDefaultTransportProvider provider a [TransportPair].
func NewDefaultTransportProvider(directSD transport.StreamDialer, directPD transport.PacketDialer) *configyaml.TypeParser[*config.TransportPair] {
	var streamEndpoints *configyaml.TypeParser[*config.Endpoint[transport.StreamConn]]
	var packetEndpoints *configyaml.TypeParser[*config.Endpoint[net.Conn]]

	var directWrappedSD *config.Dialer[transport.StreamConn]
	if directSD != nil {
		directWrappedSD = &config.Dialer[transport.StreamConn]{ConnectionProviderInfo: config.ConnectionProviderInfo{ConnType: config.ConnTypeDirect, FirstHop: ""}, Dial: directSD.DialStream}
	}
	streamDialers := newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.Dialer[transport.StreamConn], error) {
		switch input.(type) {
		case nil:
			// An absent config implicitly means direct access.
			return directWrappedSD, nil
		case string:
			// Parse URL-style config.
			return config.ParseShadowsocksStreamDialer(ctx, input, streamEndpoints.Parse)
		default:
			return nil, errors.New("parser not specified")
		}
	})

	var directWrappedPD *config.Dialer[net.Conn]
	if directPD != nil {
		directWrappedPD = &config.Dialer[net.Conn]{ConnectionProviderInfo: config.ConnectionProviderInfo{ConnType: config.ConnTypeDirect, FirstHop: ""}, Dial: directPD.DialPacket}
	}
	packetDialers := newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.Dialer[net.Conn], error) {
		switch input.(type) {
		case nil:
			// An absent config implicitly means direct access.
			return directWrappedPD, nil
		case string:
			// Parse URL-style config.
			return config.ParseShadowsocksPacketDialer(ctx, input, packetEndpoints.Parse)
		default:
			return nil, errors.New("parser not specified")
		}
	})

	directWrappedPL := &config.PacketListener{ConnectionProviderInfo: config.ConnectionProviderInfo{ConnType: config.ConnTypeDirect, FirstHop: ""}, PacketListener: &transport.UDPListener{}}
	packetListeners := newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.PacketListener, error) {
		switch input.(type) {
		case nil:
			// An absent config implicitly means UDP.
			return directWrappedPL, nil
		default:
			return nil, errors.New("parser not specified")
		}
	})

	streamEndpoints = newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.Endpoint[transport.StreamConn], error) {
		// TODO: perhaps only support string here to force the struct to have an explicit parser.
		return config.ParseDirectDialerEndpoint(ctx, input, streamDialers.Parse)
	})

	packetEndpoints = newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.Endpoint[net.Conn], error) {
		return config.ParseDirectDialerEndpoint(ctx, input, packetDialers.Parse)
	})

	transports := newTypeParser(func(ctx context.Context, input configyaml.ConfigNode) (*config.TransportPair, error) {
		// If parser directive is missing, parse as Shadowsocks for backwards-compatibility.
		return config.ParseShadowsocksTransport(ctx, input, streamEndpoints.Parse, packetEndpoints.Parse)
	})

	// Stream endpoints.
	streamEndpoints.RegisterSubParser("dial", config.NewDialEndpointSubParser(streamDialers.Parse))
	streamEndpoints.RegisterSubParser("websocket", config.NewWebsocketStreamEndpointSubParser(streamEndpoints.Parse))

	// Packet endpoints.
	packetEndpoints.RegisterSubParser("dial", config.NewDialEndpointSubParser(packetDialers.Parse))
	packetEndpoints.RegisterSubParser("websocket", config.NewWebsocketPacketEndpointSubParser(streamEndpoints.Parse))

	// Stream dialers.
	streamDialers.RegisterSubParser("block", config.NewBlockDialerSubParser[transport.StreamConn]())
	streamDialers.RegisterSubParser("direct", func(ctx context.Context, input map[string]any) (*config.Dialer[transport.StreamConn], error) {
		return directWrappedSD, nil
	})
	streamDialers.RegisterSubParser("iptable", config.NewIPTableStreamDialerSubParser(streamDialers.Parse))
	streamDialers.RegisterSubParser("shadowsocks", config.NewShadowsocksStreamDialerSubParser(streamEndpoints.Parse))

	// Packet dialers.
	packetDialers.RegisterSubParser("block", config.NewBlockDialerSubParser[net.Conn]())
	packetDialers.RegisterSubParser("direct", func(ctx context.Context, input map[string]any) (*config.Dialer[net.Conn], error) {
		return directWrappedPD, nil
	})
	packetDialers.RegisterSubParser("shadowsocks", config.NewShadowsocksPacketDialerSubParser(packetEndpoints.Parse))

	// Packet listeners.
	packetListeners.RegisterSubParser("direct", func(ctx context.Context, input map[string]any) (*config.PacketListener, error) {
		return directWrappedPL, nil
	})
	packetListeners.RegisterSubParser("shadowsocks", config.NewShadowsocksPacketListenerSubParser(packetEndpoints.Parse))

	// Transport pairs.
	transports.RegisterSubParser("tcpudp", config.NewTCPUDPTransportPairSubParser(streamDialers.Parse, packetListeners.Parse))
	transports.RegisterSubParser("basic-access", config.NewProxylessTransportPairSubParser(streamDialers.Parse))

	return transports
}
