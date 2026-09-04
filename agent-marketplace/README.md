# 影策画布 · Agent 插件市场（agent-marketplace）

让 Codex / Cursor / Claude Code 等任意兼容 **MCP（Model Context Protocol）** 的 Agent，
通过「装插件 + 一次登录」接入影策画布：读写可视化画布、搭建工作流、调用多渠道文本/图片/视频模型。

- 接入协议：MCP Streamable HTTP + **OAuth 2.1**（RFC 9728 / 8414 / 7591，PKCE 授权码）
- 服务端点：`https://yingce.cc.cd/mcp`（MCP）、`https://yingce.cc.cd/authorize`（授权）、`/token`、`/register`、`/.well-known/*`（发现）
- 分发方式：原生插件市场（marketplace），无需手工改配置、无需长期保存密钥

## 支持的 Agent

| Agent | 清单目录 | 安装指南 | 状态 |
|---|---|---|---|
| Codex（OpenAI） | `.agents/plugins/` | [docs/codex.md](docs/codex.md) | ✅ 已支持 |
| Cursor | `.cursor-plugin/` | docs/cursor.md | ⏳ 规划中 |
| Claude Code / Desktop | `.claude-plugin/` | docs/claude.md | ⏳ 规划中 |

## 目录结构

```
agent-marketplace/
├── .agents/plugins/marketplace.json   # Codex 市场清单（市场名 yingce）
├── plugins/yingce/
│   ├── .codex-plugin/plugin.json      # 插件元数据（名称/版本/能力/默认提示）
│   ├── .mcp.json                      # MCP 连接（url + oauth_resource + scopes）
│   └── skills/yingce-canvas/SKILL.md  # 教 Agent 如何正确使用画布的工作流知识
└── docs/                              # 各平台安装指南与统一安装契约
```

> 本目录是 **runtime-only 分发物**：只包含安装清单、MCP 连接声明与使用技能，
> 不含服务端实现。服务端在 `canvas-agent/src/bridge/gateway-*.ts`。

## Codex 快速安装（三步）

```bash
# 1) 添加市场（--sparse 只取 agent-marketplace 子目录）
codex plugin marketplace add 15657306288/open-ai-canvas --ref feat/universal-agent-connector --sparse agent-marketplace
# 2) 安装插件
codex plugin add yingce@yingce
# 3) 浏览器登录授权（按提示在授权页粘贴你的客户 API Key）
codex mcp login yingce
```

完整步骤、网络注意事项与验证见 [docs/codex.md](docs/codex.md)；
任何 Agent 自动安装都必须遵守 [docs/installation-contract.md](docs/installation-contract.md)。
