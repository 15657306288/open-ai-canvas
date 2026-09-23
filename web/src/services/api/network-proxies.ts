import { http } from "@/services/api/request";

export type NetworkProxy = {
    id: string;
    name: string;
    protocol: "http" | "https" | "socks5";
    host: string;
    port: number;
    username: string;
    password?: string;
    createdAt: string;
    updatedAt: string;
};

export async function listNetworkProxies() {
    return http.get<{ proxies: NetworkProxy[] }>("/network-proxies");
}

export async function createNetworkProxy(input: Omit<NetworkProxy, "id" | "createdAt" | "updatedAt">) {
    return http.post<{ proxy: NetworkProxy }>("/network-proxies", input);
}

export async function updateNetworkProxy(id: string, input: Partial<Omit<NetworkProxy, "id" | "createdAt" | "updatedAt">>) {
    return http.patch<{ proxy: NetworkProxy }>(`/network-proxies/${id}`, input);
}

export async function deleteNetworkProxy(id: string) {
    return http.delete<{ ok: boolean }>(`/network-proxies/${id}`);
}

export async function testNetworkProxy(id: string) {
    return http.post<{ ok: boolean; ip?: string; latencyMs?: number; message?: string }>(`/network-proxies/${id}/test`);
}

export async function assignNetworkProxy(input: { poolType: "doubao"; ids: string[]; proxyId?: string }) {
    return http.post<{ affected: number }>("/network-proxies/assign", input);
}

