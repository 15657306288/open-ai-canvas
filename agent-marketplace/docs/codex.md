# 在 Codex 中安装影策画布插件

适用于 Codex CLI（建议 v0.150+，需支持 `plugin marketplace` 与 `mcp login`）。

## 0. 前置条件

- 已安装 Codex CLI：`codex --version`
- 你已从影策运营方获得一个**客户 API Key**（形如 `ak_…`）。它只在浏览器授权页粘贴一次，Codex 本地只保存换得的短期 OAuth 令牌，不会保存该 Key。
- **网络（重要）**：若你的电脑开着 Clash / Surge 等系统代理，MCP 的流式长连接可能被代理中断（表现为 `Reconnecting... waiting for network`）。让影策与模型中转域名直连即可（写进 `~/.zshrc` 后重开终端）：
  ```bash
  export NO_PROXY="yingce.cc.cd,.cc.cd,a6api.com,.a6api.com,localhost,127.0.0.1,::1"
  export no_proxy="$NO_PROXY"
  ```

## 1. 添加插件市场

```bash
codex plugin marketplace add 15657306288/open-ai-canvas \
  --ref feat/universal-agent-connector --sparse agent-marketplace
```

本地开发/离线时可直接指向本地目录：`codex plugin marketplace add ./agent-marketplace`。

## 2. 安装插件

```bash
codex plugin add yingce@yingce
```

验证：`codex plugin list` 中应出现 `yingce@yingce  installed, enabled  0.1.0`。

## 3. 登录授权（OAuth）

```bash
codex mcp login yingce
```

- 命令会打开浏览器（或打印一个授权 URL）。
- 在「影策画布 · 授权」页粘贴你的客户 API Key（`ak_…`），点击**同意授权**。
- 浏览器出现 `Authentication complete. You may close this window.`、命令行出现
  `Successfully logged in to MCP server 'yingce'.` 即成功。
- 访问令牌默认 1 小时过期，由 Codex 用 refresh_token 自动续期，无需重复登录。

验证登录态：`codex mcp get yingce`，`transport: streamable_http`、`url: https://yingce.cc.cd/mcp`。

## 4. 完整重启 Codex

退出当前所有 Codex 会话/进程后重新打开，确保插件与 MCP 连接在新会话加载。

## 5. 新任务只读验证

在一个**全新任务**里让 Codex 调用只读工具验证，例如：

> 调用 yingce 的 channel_list 工具，告诉我有哪些渠道。

应返回 3 个渠道：a6api 文本、artbox 视频、红鸟。随后再让它 `canvas_get_context` 概述当前画布。

## 6. 能力简介

- 画布读写：`canvas_*`（读上下文、建节点/工作流、生成图片/视频/音频、改/删/连线）
- 项目资产：`project_*`
- 多渠道模型：`channel_*`（列渠道、列模型、发起与轮询生成）、`model_*`
- 详细使用规范见插件内 `skills/yingce-canvas/SKILL.md`，Codex 安装后会自动加载。

## 卸载

```bash
codex plugin remove yingce
codex plugin marketplace remove yingce
```

## 常见问题

| 现象 | 处理 |
|---|---|
| `Reconnecting... waiting for network` | 系统代理掐断流式连接，按第 0 步配置 NO_PROXY 后重开终端 |
| 授权页提示 Key 无效/停用 | 联系运营方确认 Key 已启用、未超日配额、余额充足 |
| 调用返回 402 余额不足 | 在运营方处充值（P2 计费：成功调用才扣费） |
| 模型不调用工具、工具名拼错 | 换用 function-calling 更稳定的模型（如 GPT / Claude 类） |
