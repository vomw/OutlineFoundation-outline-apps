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
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/things-go/go-socks5"
	"localhost/client/go/outline"
	"localhost/client/go/outline/connectivity"
)

var (
	version       = "dev"
	transportConf = ""
	socksAddr     = "127.0.0.1:1080"
	verbose       = false
	skipCheck     = false
)

type packetListenerWrapper struct {
	client *outline.Client
}

func (w *packetListenerWrapper) ListenPacket(ctx context.Context) (net.PacketConn, error) {
	return w.client.ListenPacket(ctx)
}

func fetchSSConf(input string) (string, error) {
	content := strings.TrimPrefix(input, "ssconf://")
	
	if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") || strings.Contains(content, ".") {
		url := content
		if !strings.HasPrefix(url, "http") {
			url = "https://" + url
		}
		log.Printf("Fetching dynamic config from %s", url)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("bad status: %s", resp.Status)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	
	decoded, err := base64.URLEncoding.DecodeString(content)
	if err == nil {
		return string(decoded), nil
	}
	decoded, err = base64.StdEncoding.DecodeString(content)
	if err == nil {
		return string(decoded), nil
	}
	
	return "", fmt.Errorf("invalid ssconf format")
}

func main() {
	flag.StringVar(&transportConf, "transport", "", "Shadowsocks transport config (JSON, YAML, ss:// or ssconf:// URL)")
	flag.StringVar(&socksAddr, "socks", "127.0.0.1:1080", "SOCKS5 listen address")
	flag.BoolVar(&verbose, "v", false, "Enable verbose logging")
	flag.BoolVar(&skipCheck, "skip-check", false, "Skip connectivity check for faster startup")
	showVersion := flag.Bool("version", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Outline CLI %s\n", version)
		os.Exit(0)
	}

	if transportConf == "" {
		fmt.Fprintln(os.Stderr, "Error: -transport is required")
		flag.Usage()
		os.Exit(1)
	}

	configText := transportConf
	if strings.HasPrefix(transportConf, "ssconf://") {
		var err error
		configText, err = fetchSSConf(transportConf)
		if err != nil {
			log.Fatalf("Failed to handle ssconf: %v", err)
		}
	}

	if verbose {
		log.Printf("Parsing config: %s", configText)
	}

	// Initialize Outline Client
	clientConfig := &outline.ClientConfig{}
	
	result := outline.InvokeMethod("ParseTunnelConfig", configText)
	if result.Error != nil {
		log.Fatalf("Failed to parse transport config: %v", result.Error.Message)
	}

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

	// Start Outline session
	if err := client.StartSession(); err != nil {
		log.Fatalf("Failed to start session: %v", err)
	}
	defer client.EndSession()

	// Create SOCKS5 server using github.com/things-go/go-socks5
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
		log.Printf("Outline CLI starting SOCKS5 proxy on %s (TCP/UDP, IPv4/IPv6)", socksAddr)
		if err := srv.ListenAndServe("tcp", socksAddr); err != nil {
			log.Fatalf("SOCKS5 server failed: %v", err)
		}
	}()

	// Delayed/Optional Connectivity Check in background
	if !skipCheck {
		go func() {
			time.Sleep(1 * time.Second)
			log.Println("Performing background connectivity check...")
			
			// TCP Check
			tcpErr := connectivity.CheckTCPConnectivity(client)
			if tcpErr != nil {
				log.Printf("Warning: TCP Connectivity check failed: %v", tcpErr)
			} else {
				log.Println("TCP Connectivity check: OK")
			}

			// UDP Check
			pl := &packetListenerWrapper{client}
			udpErr := connectivity.CheckUDPConnectivity(pl)
			if udpErr != nil {
				log.Printf("Warning: UDP Connectivity check failed: %v", udpErr)
			} else {
				log.Println("UDP Connectivity check: OK")
			}
		}()
	}

	<-sigCh
	log.Println("Shutting down...")
}
