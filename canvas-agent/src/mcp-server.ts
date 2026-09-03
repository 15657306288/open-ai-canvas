import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

import { agentFetch } from "./agent-fetch.js";
import { AGENT_PROMPT, loadConfig, type CanvasAgentConfig, VERSION } from "./config.js";
import { registerDreaminaMcp } from "./modules/dreamina-mcp.js";
import { toolDescriptions, toolInputSchemas, toolNames, type ToolName } from "./schemas.js";

type CanvasAgentToolResponse = { ok?: boolean; result?: unknown; error?: string };

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
    if (!options.canvasOnly) registerDreaminaMcp(server, config);
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
