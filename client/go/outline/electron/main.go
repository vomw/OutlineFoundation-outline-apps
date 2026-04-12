// Copyright 2019 The Outline Authors
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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/armon/go-socks5"
	"localhost/client/go/outline"
	"localhost/client/go/outline/configregistry"
	"localhost/client/go/outline/connectivity"
	"localhost/client/go/outline/platerrors"
)

// Exit codes. Must be kept in sync with definitions in "go_vpn_tunnel.ts"
const (
	exitCodeSuccess = 0
	exitCodeFailure = 1
)

var logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

// The result JSON containing two error strings when "--checkConnectivity".
type CheckConnectivityResult struct {
	TCPErrorJson string `json:"tcp"`
	UDPErrorJson string `json:"udp"`
}

var args struct {
	adapterIndex *int

	keyID        *string
	clientConfig *string

	socks5Addr   *string
	logLevel          *string
	checkConnectivity *bool
	version           *bool
}

var version string // Populated at build time through `-X main.version=...`

func main() {
	// Windows Network Adapter Index (still kept for potential binding)
	args.adapterIndex = flag.Int("adapterIndex", -1, "Windows network adapter index for proxy connection")

	// Proxy client config
	args.keyID = flag.String("keyID", "", "The ID of the key being used")
	args.clientConfig = flag.String("client", "", "A JSON object containing the client config, UTF8-encoded")

	// SOCKS5 config
	args.socks5Addr = flag.String("socks5Addr", "127.0.0.1:1080", "Address to listen on for SOCKS5 proxy")

	// Check connectivity of clientConfig and exit
	args.checkConnectivity = flag.Bool("checkConnectivity", false, "Check the proxy TCP and UDP connectivity and exit.")

	// Misc
	args.logLevel = flag.String("logLevel", "info", "Logging level: debug|info|warn|error|none")
	args.version = flag.Bool("version", false, "Print the version and exit.")

	flag.Parse()

	if *args.version {
		fmt.Println(version)
		os.Exit(exitCodeSuccess)
	}

	setLogLevel(*args.logLevel)

	if len(*args.clientConfig) == 0 {
		printErrorAndExit(platerrors.PlatformError{Code: platerrors.InvalidConfig, Message: "client config missing"}, exitCodeFailure)
	}

	clientConfig := outline.ClientConfig{}
	if *args.adapterIndex >= 0 {
		tcp, udp, err := newBaseDialersWithAdapter(*args.adapterIndex)
		if err != nil {
			printErrorAndExit(err, exitCodeFailure)
		}
		clientConfig.TransportParser = configregistry.NewDefaultTransportProvider(tcp, udp)
	}
	result := clientConfig.New(*args.keyID, *args.clientConfig)
	if result.Error != nil {
		printErrorAndExit(result.Error, exitCodeFailure)
	}
	client := result.Client

	if *args.checkConnectivity {
		tcpErr := connectivity.CheckTCPConnectivity(client)
		output := CheckConnectivityResult{
			TCPErrorJson: marshalErrorToJSON(tcpErr),
		}
		jsonBytes, err := json.Marshal(output)
		if err != nil {
			printErrorAndExit(err, exitCodeFailure)
		}
		fmt.Println(string(jsonBytes))
		os.Exit(exitCodeSuccess)
	}

	if err := client.StartSession(); err != nil {
		printErrorAndExit(platerrors.PlatformError{
			Code:    platerrors.SetupSystemVPNFailed,
			Message: "failed start backend client",
			Cause:   platerrors.ToPlatformError(err),
		}, exitCodeFailure)
	}
	defer client.EndSession()

	// Start SOCKS5 server
	conf := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return client.DialStream(ctx, addr)
		},
	}
	srv, err := socks5.New(conf)
	if err != nil {
		printErrorAndExit(err, exitCodeFailure)
	}

	go func() {
		if err := srv.ListenAndServe("tcp", *args.socks5Addr); err != nil {
			logger.Error("SOCKS5 server failed", "err", err)
		}
	}()

	// This message is used in TypeScript to determine whether the server has been started successfully
	// We keep "tun2socks running" to minimize changes in Electron for now, but mark it as SOCKS5
	logger.Info("tun2socks running (SOCKS5 mode)...", "addr", *args.socks5Addr)

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	sig := <-osSignals
	logger.Debug("Received signal", "signal", sig)
}

func setLogLevel(level string) {
	slvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		slvl = slog.LevelDebug
	case "info":
		slvl = slog.LevelInfo
	case "warn":
		slvl = slog.LevelWarn
	case "error":
		slvl = slog.LevelError
	case "none":
		logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
		return
	}
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slvl}))
}

func marshalErrorToJSON(e error) string {
	pe := platerrors.ToPlatformError(e)
	if pe == nil {
		return ""
	}
	errJson, err := platerrors.MarshalJSONString(pe)
	if err != nil {
		return string(pe.Code)
	}
	return errJson
}

func printErrorAndExit(e error, exitCode int) {
	fmt.Fprintln(os.Stderr, marshalErrorToJSON(e))
	os.Exit(exitCode)
}
