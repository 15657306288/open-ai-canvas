const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1", "[::1]"]);

export function assertTokenForHost(host: string, token: string, envName: string): void {
    if (!LOOPBACK_HOSTS.has(host.trim().toLowerCase()) && !token) {
        throw new Error(`${envName} must be configured when listening on a non-loopback host`);
    }
}
