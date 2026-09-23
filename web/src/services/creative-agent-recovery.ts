import { nanoid } from "nanoid";
import type { AiConfig } from "@/stores/use-config-store";
import type { ResponseInputMessage, ResponseToolCall } from "./api/image";
import { CREATIVE_SCENARIOS, normalizeCreativeQuestions, type CreativeScenarioId } from "@/lib/creation/creative-agent-contract";
import { assertCreativeBriefSpecifications, mergeCreativeBrief, normalizeCreativeProposal, type CreativeAgentState } from "@/lib/creation/creative-agent-state";
import { parseCreativeToolArguments } from "@/lib/creation/creative-agent-tools";

type Reply = { content: string; toolCalls: ResponseToolCall[] };
type Context = { config: AiConfig; state: CreativeAgentState; latestInput: string };

function validate(reply: Reply, context: Context) {
    const call = reply.toolCalls.find((item) => item.function.name === "creative_respond");
    if (!call) return;
    const input = parseCreativeToolArguments(call.function.arguments);
    const scene = typeof input.scenario === "string" && Object.hasOwn(CREATIVE_SCENARIOS, input.scenario) ? input.scenario as CreativeScenarioId : context.state.scene;
    const brief = mergeCreativeBrief(context.state.brief, input.brief, scene, context.latestInput);
    const questions = normalizeCreativeQuestions(input.questions, scene, brief, { interactionId: "validation", revision: 1 }, context.state.references.map((asset) => ({ id: asset.id, label: asset.title })));
    if (questions.questions.length) return;
    if (Array.isArray(input.questions) && input.questions.length && !input.proposal) throw new Error("问题均为已知或无效字段。请使用已知答案继续规划，不要重复提问；若缺少其他关键信息，请提出具体的新问题。");
    if (input.proposal) {
        const proposal = normalizeCreativeProposal(input.proposal, "validation", 1, context.config, context.state.references);
        assertCreativeBriefSpecifications(proposal, brief);
    }
}

function recoveryQuestion(context: Context, error: string): Reply {
    const assetIssue = /引用|素材|图片节点/.test(error);
    const modelIssue = /模型|能力|尺寸|比例|时长/.test(error);
    const field = `recovery.choice-${nanoid(8)}`;
    const message = assetIssue ? "这份方案的素材依赖仍未能可靠绑定。已有作品保留，请选择下一步，我会按你的选择继续规划。"
        : modelIssue ? "当前可用生成能力还不能可靠满足这份方案。请决定接下来如何安排，已确认的目标会保留。"
        : "我尝试修正了方案，但它仍未达到可执行条件。已有内容保留，你可以先完善创意，或补充调整要求后继续。";
    const choices = assetIssue
        ? [{ id: "choose", label: "先让我选择参考素材", description: "助手下一轮列出当前真实可用素材供选择。" }, { id: "text", label: "先完善文字方案", description: "先讨论内容，不创建或生成媒体。" }]
        : [{ id: "text", label: "先完善文字方案", description: "保留创意和已确认需求，暂不进入媒体制作。" }, { id: "alternatives", label: "说明可行替代方案", description: "列出差异，由我决定是否修改目标。" }];
    const input = { scenario: context.state.scene, message, brief: [], questions: [{ field, type: "single", title: "接下来希望怎样推进？", required: true, allowCustom: true, options: choices }] };
    return { content: message, toolCalls: [{ id: `recovery-${nanoid()}`, type: "function", function: { name: "creative_respond", arguments: JSON.stringify(input) } }] };
}

// Correct rejected arguments before presentation. This loop never executes canvas or media tools.
export async function recoverCreativeResponse(initial: Reply, messages: ResponseInputMessage[], context: Context,
    request: (messages: ResponseInputMessage[]) => Promise<Reply>, onProgress: (attempt: number, error: string) => void): Promise<Reply> {
    let reply = initial;
    let protocol = [...messages];
    for (let attempt = 0; attempt <= 2; attempt++) {
        let failure: string;
        try { validate(reply, context); return reply; }
        catch (error) { failure = error instanceof Error ? error.message : "交互格式无效"; }
        onProgress(attempt + 1, failure);
        if (attempt === 2) return recoveryQuestion(context, failure);
        protocol = [...protocol,
            ...reply.toolCalls.map((call): ResponseInputMessage => ({ type: "function_call", call_id: call.id, name: call.function.name, arguments: call.function.arguments, ...(call.thoughtSignature ? { thoughtSignature: call.thoughtSignature } : {}) })),
            ...reply.toolCalls.map((call): ResponseInputMessage => ({ role: "tool", tool_call_id: call.id, content: JSON.stringify({ status: call.function.name === "creative_respond" ? "validation_failed" : "cancelled", error: failure, executed: false }) })),
            { role: "user", content: "这是程序校验反馈，不是用户修改需求。未创建节点或消费媒体报价。请自行修复参数，保留已知要求与真实引用；不让用户填写技术字段。缺少真实素材、事实或能力时，用creative_respond.questions主动提问，选项来自当前上下文且允许自由补充。不再仅说‘已修复’或‘请确认’，必须返回可校验的完整方案或可回答问题。仍无法组成媒体方案时可先提供纯文字创意并提问下一步，不虚构媒体配置。" },
        ];
        // Only proposal/question tools are supplied to the repair model; there are no writable tools.
        try {
            reply = await request(protocol);
        } catch (error) {
            if (error instanceof Error && error.name === "AbortError") throw error;
            onProgress(3, error instanceof Error ? error.message : "修正请求失败");
            return recoveryQuestion(context, failure);
        }
        reply = { ...reply, toolCalls: reply.toolCalls.filter((call) => call.function.name === "creative_respond") };
        if (!reply.toolCalls.length && !reply.content.trim()) return recoveryQuestion(context, failure);
    }
    return reply;
}
