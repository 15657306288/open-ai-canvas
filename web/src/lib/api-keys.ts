import { apiClient, request, type BackendEnvelope } from "@/services/api/request";

export type ApiKeyStatus = "active" | "disabled";

export type ApiKeyItem = {
    id: string;
    name: string;
    prefix: string;
    status: ApiKeyStatus;
    lastUsedAt: string | null;
    createdAt: string;
};

export type ApiKeyList = {
    items: ApiKeyItem[];
};

export type ApiKeyCreated = ApiKeyItem & {
    /** 明文 key，仅在创建时返回一次，之后不可再获取。 */
    key: string;
};

/** 当前用户已签发的 API Key（外部智能体/CLI 经网关调用画布时使用）。 */
export async function listApiKeys(): Promise<ApiKeyList> {
    return request(apiClient.get<BackendEnvelope<ApiKeyList>>("/api-keys"));
}

/** 创建 API Key：返回明文 key（仅此一次），请立即保存。 */
export async function createApiKey(name: string): Promise<ApiKeyCreated> {
    return request(apiClient.post<BackendEnvelope<ApiKeyCreated>>("/api-keys", { name }));
}

/** 删除（吊销）指定 API Key。 */
export async function deleteApiKey(id: string): Promise<{ id: string }> {
    return request(apiClient.delete<BackendEnvelope<{ id: string }>>(`/api-keys/${encodeURIComponent(id)}`));
}
