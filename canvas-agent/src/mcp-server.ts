import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

import { agentFetch } from "./agent-fetch.js";
import { AGENT_PROMPT, CONFIG_DIR, loadConfig, type CanvasAgentConfig, VERSION } from "./config.js";
import { registerDreaminaMcp } from "./modules/dreamina-mcp.js";
import { toolDescriptions, toolInputSchemas, toolNames, type ToolName } from "./schemas.js";
import { channelCatalogPath, createJsonCatalogProvider } from "./channel-catalog.js";
import { createChannelGenerateClient } from "./channel-generate.js";
import { registerChannelTools, type ChannelToolContext } from "./channel-tools.js";

type CanvasAgentToolResponse = { ok?: boolean; result?: unknown; error?: string };

/** [connector] P0-B-4 渠道工具上下文单例（catalog provider 只 fs.watch 一次，
 *  stdio 与各 HTTP MCP session 共享同一目录数据源与生成客户端）。 */
let channelToolContext: ChannelToolContext | undefined;
export function getChannelToolContext(): ChannelToolContext {
    if (!channelToolContext) {
        const catalog = createJsonCatalogProvider(channelCatalogPath(CONFIG_DIR));
        channelToolContext = { catalog, generate: createChannelGenerateClient(catalog) };
    }
    return channelToolContext;
}

export async function startMcpServer(options: { canvasOnly?: boolean } = {}) {
    const config = loadConfig(true);
    const server = new McpServer({ name: "canvas-agent", version: VERSION }, { instructions: AGENT_PROMPT });
    registerMcpTools(server, config, {
        canvasOnly: options.canvasOnly ?? process.argv.slice(3).includes("--canvas-only"),
    });
    await server.connect(new StdioServerTransport());
}

export function registerMcpTools(server: McpServer, config: CanvasAgentConfig, options: { canvasOnly?: boolean } = {}) {
    toolNames.forEach((name) => registerCanvasTool(server, config, name));
    if (!options.canvasOnly) {
        registerDreaminaMcp(server, config);
        // [connector] P0-B-4 渠道/模型连接器工具（目录自更新）
        registerChannelTools(server, getChannelToolContext());
    }
}

function registerCanvasTool(server: McpServer, config: CanvasAgentConfig, name: ToolName) {
    const schema = toolInputSchemas[name];
    server.registerTool(name, { description: toolDescriptions[name], inputSchema: schema.shape }, async (input: unknown) => {
        const result = await postCanvasAgentTool(config, name, schema.parse(input));
        return { content: [{ type: "text" as const, text: JSON.stringify(result, null, 2) }] };
    });
}

async function postCanvasAgentTool(config: CanvasAgentConfig, name: ToolName, input: unknown) {
    // [connector] P0-A-4：经 agentFetch 走 keepalive + 超时（POST 非只读不重试，避免重复副作用）
    const res = await agentFetch(`${config.url}/api/tools`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-canvas-agent-token": config.token },
        body: JSON.stringify({ name, input }),
        timeoutMs: 30_000,
    });
    const body = (await res.json()) as CanvasAgentToolResponse;
    if (!body.ok) throw new Error(body.error || "tool call failed");
    return body.result;
}

export { postDreaminaCliTool } from "./modules/dreamina-mcp.js";
