// Removed in SOCKS5 mode.
export class RoutingDaemon {
  constructor() {
    throw new Error('RoutingDaemon is disabled in SOCKS5 mode.');
  }
  async start() { return ''; }
  async stop() { }
  get onceDisconnected() { return Promise.resolve(); }
  set onNetworkChange(_: any) {}
}

export async function installRoutingServices(): Promise<void> {
  // Nothing to install in SOCKS5 mode.
}
