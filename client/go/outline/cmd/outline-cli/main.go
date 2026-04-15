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
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/things-go/go-socks5"
	"localhost/client/go/outline"
	"localhost/client/go/outline/connectivity"
)

var (
	version    = "dev"
	transport  = ""
	socksAddr  = "127.0.0.1:1080"
	verbose    = false
)

func main() {
	flag.StringVar(&transport, "transport", "", "Shadowsocks transport config (JSON, YAML, or ss:// URL)")
	flag.StringVar(&socksAddr, "socks", "127.0.0.1:1080", "SOCKS5 listen address")
	flag.BoolVar(&verbose, "v", false, "Enable verbose logging")
	showVersion := flag.Bool("version", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Outline CLI %s\n", version)
		os.Exit(0)
	}

	if transport == "" {
		fmt.Fprintln(os.Stderr, "Error: -transport is required")
		flag.Usage()
		os.Exit(1)
	}

	// Initialize Outline Client
	clientConfig := &outline.ClientConfig{}
	
	// doParseTunnelConfig handles various formats and returns a JSON string
	result := outline.InvokeMethod("ParseTunnelConfig", transport)
	if result.Error != nil {
		log.Fatalf("Failed to parse transport config: %v", result.Error.Message)
	}

	// The value returned by ParseTunnelConfig is a JSON string of firstHopAndTunnelConfigJSON
	var parsed struct {
		Client string `json:"client"`
	}
	if err := json.Unmarshal([]byte(result.Value), &parsed); err != nil {
		log.Fatalf("Failed to parse normalized config: %v", err)
	}

	clientResult := clientConfig.New("", parsed.Client)
	if clientResult.Error != nil {
		log.Fatalf("Failed to create Outline client: %v", clientResult.Error.Message)
	}
	client := clientResult.Client

	// Test connectivity
	if verbose {
		log.Println("Checking connectivity...")
		err := connectivity.CheckTCPConnectivity(client)
		if err != nil {
			log.Printf("Warning: Connectivity check failed: %v", err)
		} else {
			log.Println("Connectivity check successful")
		}
	}

	// Start Outline session
	if err := client.StartSession(); err != nil {
		log.Fatalf("Failed to start session: %v", err)
	}
	defer client.EndSession()

	// Create SOCKS5 server
	socks5Logger := log.New(os.Stdout, "[SOCKS5] ", log.LstdFlags)
	if !verbose {
		socks5Logger.SetOutput(io.Discard)
	}

	srv := socks5.NewServer(
		socks5.WithLogger(socks5.NewLogger(socks5Logger)),
		socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			if verbose {
				log.Printf("Dialing %s %s", network, addr)
			}
			return client.DialStream(ctx, addr)
		}),
	)

	// Listen for signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Outline CLI starting SOCKS5 proxy on %s", socksAddr)
		if err := srv.ListenAndServe("tcp", socksAddr); err != nil {
			log.Fatalf("SOCKS5 server failed: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")
}
