#!/bin/bash
# ============================================================
# 启动影策画布 Runtime（/mcp 门面，http://127.0.0.1:17371）
# 供 Codex 等外部 agent 通过 Streamable HTTP 调用画布 50 个工具
# 已运行则直接提示跳过；未运行则后台拉起并写日志
# L2：监听 0.0.0.0 开放局域网，权威 Host 含本机局域网 IP；/mcp 带 Bearer 鉴权
# ============================================================
CANVAS_DIR="/Users/linmengjiang/open-ai-canvas/canvas-agent"
LOG_FILE="/Users/linmengjiang/.infinite-canvas/runtime.log"
LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo "127.0.0.1")

if lsof -nP -iTCP:17371 -sTCP:LISTEN >/dev/null 2>&1; then
  osascript -e 'display notification "影策 Runtime 已在运行 (127.0.0.1:17371)" with title "影策"'
  echo "影策 Runtime 已在运行：http://127.0.0.1:17371/mcp"
  exit 0
fi

cd "$CANVAS_DIR" || exit 1
CANVAS_HOST=0.0.0.0 \
CANVAS_AUTHORITY="127.0.0.1:17371,${LAN_IP}:17371" \
nohup node dist/index.js >> "$LOG_FILE" 2>&1 &
sleep 2

if lsof -nP -iTCP:17371 -sTCP:LISTEN >/dev/null 2>&1; then
  osascript -e 'display notification "影策 Runtime 已启动 (http://127.0.0.1:17371/mcp)" with title "影策"'
  echo "影策 Runtime 已启动：http://127.0.0.1:17371/mcp"
  echo "局域网接入：http://${LAN_IP}:17371/mcp（需 Bearer Token）"
  echo "日志：$LOG_FILE"
else
  echo "启动失败，请查看日志：$LOG_FILE"
  tail -20 "$LOG_FILE" 2>/dev/null
  exit 1
fi
