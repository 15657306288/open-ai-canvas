import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const panel = readFileSync(new URL("../src/components/canvas/canvas-local-agent-panel.tsx", import.meta.url), "utf8");
const handler = readFileSync(new URL("../../canvas-agent/src/modules/canvas-agent-http.ts", import.meta.url), "utf8");

test("persistent completion waits for semantic execution instead of raw model completion", () => {
    expect(panel).toContain('event.type === "run.completed" || (event.type === "turn.completed" && !event.runId)');
    expect(panel).toContain('event.type === "run.failed" || event.type === "turn.failed"');
    expect(panel).toContain("event.agentRunId === activeRunIdRef.current");
    expect(panel.indexOf("await startAgentRun(")).toBeLessThan(panel.indexOf('fetchAgentJson<AgentTurnResponse>("/agent/codex/turn"'));
});

test("prefetch and planned execution preserve the persistent run correlation", () => {
    expect(handler).toMatch(/executeReadOnlySteps\(\s*plan,\s*\(name, input\) => session.callTool\(name, input\),\s*turnEmit,/);
    expect(handler).toMatch(/runCodexTurn\(\s*withAgentPrompt\(prompt, plan, readOnlyExecution\),\s*turnEmit,/);
    expect(handler).toContain("onExecutionEnd: () => session.finishAgentRun?.()");
});
