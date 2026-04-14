// Copyright 2021 The Outline Authors
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

import {platform} from 'os';

import {powerMonitor} from 'electron';

import {pathToEmbeddedTun2socksBinary} from './app_paths';
import {checkUDPConnectivity, checkUDPConnectivityWindows} from './go_helpers';
import {ChildProcessHelper, ProcessTerminatedSignalError} from './process';
import {VpnTunnel} from './vpn_tunnel';
import {TunnelStatus} from '../web/app/outline_server_repository/vpn';

const IS_LINUX = platform() === 'linux';
const IS_WINDOWS = platform() === 'win32';

const SOCKS5_ADDR = '127.0.0.1:1080';

// Establishes a local SOCKS5 proxy using outline-go-socks5-proxy.
//
// |App (SOCKS5)| <-> |outline-go-socks5-proxy| <-> |Outline proxy|
//
export class GoVpnTunnel implements VpnTunnel {
  private readonly socks5Proxy: GoSocks5Proxy;
  private isDebugMode = false;

  // See #resumeListener.
  private disconnected = false;

  private isUdpEnabled = false;

  private readonly onAllHelpersStopped: Promise<void>;
  private resolveAllHelpersStopped: () => void;

  private reconnectingListener?: () => void;

  private reconnectedListener?: () => void;

  constructor(
    readonly keyId: string,
    readonly clientConfig: string
  ) {
    this.socks5Proxy = new GoSocks5Proxy(keyId);

    // This promise, tied to both helper process' exits, is key to the instance's
    // lifecycle:
    //  - once any helper fails or exits, stop them all
    //  - once *all* helpers have stopped, we're done
    this.onAllHelpersStopped = new Promise(resolve => {
      this.resolveAllHelpersStopped = resolve;
    });
  }

  // Turns on verbose logging for the managed processes. Must be called before launching the
  // processes
  enableDebugMode() {
    this.isDebugMode = true;
    this.socks5Proxy.enableDebugMode();
  }

  // Fulfills once all helpers have started successfully.
  async connect(checkProxyConnectivity: boolean) {
    if (IS_WINDOWS) {
      // Windows: when the system suspends, the proxy might need restart.
      powerMonitor.on('suspend', this.suspendListener.bind(this));
      powerMonitor.on('resume', this.resumeListener.bind(this));
    }

    if (checkProxyConnectivity) {
      try {
        if (IS_WINDOWS) {
          this.isUdpEnabled = await checkUDPConnectivityWindows(
            this.clientConfig,
            undefined,
            this.isDebugMode
          );
        } else {
          this.isUdpEnabled = await checkUDPConnectivity(
            this.clientConfig,
            this.isDebugMode
          );
        }
      } catch (e) {
        console.warn(`Connectivity check failed: ${e.message}. Proceeding anyway.`);
        this.isUdpEnabled = true;
      }
    }
    console.log(`UDP support: ${this.isUdpEnabled}`);

    await this.startSocks5Proxy();
  }

  networkChanged(status: TunnelStatus, _gatewayIndex?: string) {
    if (status === TunnelStatus.CONNECTED) {
      if (this.reconnectedListener) {
        this.reconnectedListener();
      }

      // Test whether UDP availability has changed; since it won't change 99% of the time, do this
      // *after* we've informed the client we've reconnected.
      void this.updateUdpAndRestartProxy();
    } else if (status === TunnelStatus.RECONNECTING) {
      if (this.reconnectingListener) {
        this.reconnectingListener();
      }
    } else {
      console.error(
        `unknown network change status ${status}`
      );
    }
  }

  private async suspendListener() {
    await this.socks5Proxy.stop();
    console.log('stopped SOCKS5 proxy in preparation for suspend');
  }

  private async resumeListener() {
    if (this.disconnected) {
      // NOTE: Cannot remove resume listeners - Electron bug?
      console.error(
        'resume event invoked but this tunnel is terminated - doing nothing'
      );
      return;
    }

    console.log('restarting SOCKS5 proxy after resume');
    await this.updateUdpAndRestartProxy();
  }

  private startSocks5Proxy(): Promise<void> {
    if (IS_WINDOWS) {
      return this.socks5Proxy.startWindows(
        this.clientConfig,
        this.isUdpEnabled
      );
    } else {
      return this.socks5Proxy.start(this.clientConfig, this.isUdpEnabled);
    }
  }

  private async updateUdpAndRestartProxy() {
    try {
      if (IS_WINDOWS) {
        this.isUdpEnabled = await checkUDPConnectivityWindows(
          this.clientConfig,
          undefined,
          this.isDebugMode
        );
      } else {
        this.isUdpEnabled = await checkUDPConnectivity(
          this.clientConfig,
          this.isDebugMode
        );
      }
      console.log(`UDP support now ${this.isUdpEnabled}`);
    } catch (e) {
      console.warn('connectivity check failed:', e);
      // Keep existing UDP setting or default to true
      this.isUdpEnabled = true;
    }

    // Restart proxy.
    try {
      await this.socks5Proxy.stop();
    } catch {
      // Ignore the errors
    }
    await this.startSocks5Proxy();
  }

  // Use #onceDisconnected to be notified when the tunnel terminates.
  async disconnect() {
    if (this.disconnected) {
      return;
    }

    if (IS_WINDOWS) {
      powerMonitor.removeListener('suspend', this.suspendListener.bind(this));
      powerMonitor.removeListener('resume', this.resumeListener.bind(this));
    }

    try {
      await this.socks5Proxy.stop();
    } catch (e) {
      if (!(e instanceof ProcessTerminatedSignalError)) {
        console.error(`could not stop SOCKS5 proxy: ${e.message}`);
      }
    }

    this.resolveAllHelpersStopped();
    this.disconnected = true;
  }

  // Fulfills once all helper processes have stopped.
  get onceDisconnected() {
    return this.onAllHelpersStopped;
  }

  // Sets an optional callback for when the proxy is attempting to re-connect.
  onReconnecting(newListener: () => void | undefined) {
    this.reconnectingListener = newListener;
  }

  // Sets an optional callback for when the proxy successfully reconnects.
  onReconnected(newListener: () => void | undefined) {
    this.reconnectedListener = newListener;
  }
}

// GoSocks5Proxy is a Go program that listens for SOCKS5 requests
// and relays it to a Outline proxy server.
class GoSocks5Proxy {
  // Resolved when proxy prints "tun2socks running" to stdout
  // Call `monitorStarted` to set this field
  private whenStarted: Promise<void>;
  private stopRequested = false;
  private readonly process: ChildProcessHelper;

  constructor(readonly keyId: string) {
    this.process = new ChildProcessHelper(pathToEmbeddedTun2socksBinary());
  }

  /**
   * Starts proxy process, and waits for it to launch successfully.
   * Success is confirmed when the phrase "tun2socks running" is detected in the `stdout`.
   * Otherwise, an error containing a JSON-formatted message will be thrown.
   * @param isUdpEnabled Indicates whether the remote Outline server supports UDP.
   */
  start(clientConfig: string, isUdpEnabled: boolean): Promise<void> {
    return this.startWithPlatformSpecificArgs(clientConfig, isUdpEnabled, []);
  }

  /**
   * Starts proxy process with Windows specific CLI arguments.
   */
  startWindows(
    clientConfig: string,
    isUdpEnabled: boolean
  ): Promise<void> {
    const args: string[] = [];
    args.push('-socks5Addr', SOCKS5_ADDR);
    return this.startWithPlatformSpecificArgs(clientConfig, isUdpEnabled, args);
  }

  private startWithPlatformSpecificArgs(
    clientConfig: string,
    isUdpEnabled: boolean,
    args: string[]
  ): Promise<void> {
    args.push('-keyID', this.keyId);
    args.push('-client', clientConfig);
    args.push('-logLevel', this.process.isDebugModeEnabled ? 'debug' : 'info');
    // Note: dnsFallback is not directly applicable to pure SOCKS5 mode but kept for Go compatibility if needed
    if (!isUdpEnabled) {
      args.push('-dnsFallback');
    }

    const whenProcessEnded = this.launchWithAutoRestart(args);

    // Either started successfully, or terminated exceptionally
    return Promise.race([this.whenStarted, whenProcessEnded]);
  }

  private monitorStarted(): Promise<void> {
    return (this.whenStarted = new Promise(resolve => {
      this.process.onStdOut = (data?: string | Buffer) => {
        // We still monitor for "tun2socks running" which is the success signal from Go
        if (data?.toString().includes('tun2socks running')) {
          console.debug('[socks5Proxy] - started');
          this.process.onStdOut = null;
          resolve();
        }
      };
    }));
  }

  private async launchWithAutoRestart(args: string[]): Promise<void> {
    console.debug('[socks5Proxy] - starting SOCKS5 proxy ...', args);
    let restarting = false;
    let lastError: Error | null = null;
    do {
      if (restarting) {
        console.warn('[socks5Proxy] - exited unexpectedly; restarting ...');
      }
      restarting = false;
      this.monitorStarted()
        .then(() => {
          restarting = true;
        })
        .catch(e => {
          console.error('[socks5Proxy] - failed to monitor start:', e);
        });
      try {
        lastError = null;
        await this.process.launch(args, false);
        console.info('[socks5Proxy] - exited with no errors');
      } catch (e) {
        console.error('[socks5Proxy] - terminated due to:', e);
        lastError = e;
      }
    } while (!this.stopRequested && restarting);
    if (lastError) {
      throw lastError;
    }
  }

  stop() {
    this.stopRequested = true;
    return this.process.stop();
  }

  enableDebugMode() {
    this.process.isDebugModeEnabled = true;
  }
}
