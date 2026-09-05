import { apiClient, request, type BackendEnvelope } from "./request";

export type AgentConfirmationStatus = "pending" | "approved" | "rejected" | "expired";

export type AgentConfirmation = {
    id: string;
    tool: string;
    modelKey: string;
    amountMicrocredits: number;
    promptSummary: string;
    status: AgentConfirmationStatus;
    createdAt: string;
    expiresAt: string;
};

export type AgentConfirmationList = {
    items: AgentConfirmation[];
};

/** 当前用户待确认的外部 Agent 生成请求（网关 reserve 冻结后挂起，等用户在网站确认）。 */
export async function listPendingAgentConfirmations(): Promise<AgentConfirmationList> {
    return request(apiClient.get<BackendEnvelope<AgentConfirmationList>>("/agent-confirmations"));
}

/** 批准生成：网关轮询到 approved 后才会真正执行生成并结算。 */
export async function approveAgentConfirmation(id: string): Promise<AgentConfirmation> {
    return request(apiClient.post<BackendEnvelope<AgentConfirmation>>(`/agent-confirmations/${encodeURIComponent(id)}/approve`));
}

/** 拒绝生成：网关退款，绝不执行生成。 */
export async function rejectAgentConfirmation(id: string): Promise<AgentConfirmation> {
    return request(apiClient.post<BackendEnvelope<AgentConfirmation>>(`/agent-confirmations/${encodeURIComponent(id)}/reject`));
}
