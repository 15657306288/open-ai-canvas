#!/bin/bash
# ============================================================
# 启动影策 L3 服务器三件套（Mac 当服务器）
#   1) Broker 中继    :17800  （云端/服务器侧队列转发）
#   2) Runtime+bridge :17371  （画布 Runtime，主动外连 Broker，零入站端口）
#   3) MCP 网关       :17801  （对外标准 MCP 端点，外部 agent 只配一个 URL）
# 每个端口已在运行则自动跳过（幂等），可反复执行。
# 外部 agent（Codex/豆包/WorkBuddy 等任意 MCP 客户端）接入：
#   URL = http://<本机IP>:17801/mcp   Bearer Token = ${GATEWAY_TOKEN}
# 局域网其他电脑可直接用本机局域网 IP 访问（如 http://192.168.31.244:17801/mcp）。
# ============================================================

CANVAS_DIR="/Users/linmengjiang/open-ai-canvas/canvas-agent"
RUNTIME_LOG="/Users/linmengjiang/.infinite-canvas/runtime.log"
BROKER_LOG="/Users/linmengjiang/.infinite-canvas/broker.log"
GATEWAY_LOG="/Users/linmengjiang/.infinite-canvas/gateway.log"

# 凭据（本地开发默认值；对外部署请修改为强随机值）
BRIDGE_TOKEN="yingce-bridge-2026"     # broker 与各 bridge 共享凭据
GATEWAY_TOKEN="gateway-2026"          # 外部 agent 接入网关的 Bearer

# Runtime 自身 masterToken（/mcp 与 /api/tools 鉴权）
RUNTIME_TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.infinite-canvas/canvas-agent.json')).get('token',''))" 2>/dev/null)
LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo "127.0.0.1")

echo "== 影策 L3 服务器 =="
echo "本机 IP: $LAN_IP   Runtime Token: ${RUNTIME_TOKEN:0:8}..."

# 1) Broker
if lsof -nP -iTCP:17800 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "[1/3] Broker 已在运行 :17800"
else
  echo "[1/3] 启动 Broker :17800 ..."
  (cd "$CANVAS_DIR" && CANVAS_BROKER_HOST=0.0.0.0 CANVAS_BROKER_PORT=17800 nohup node dist/bridge/broker-server.js >> "$BROKER_LOG" 2>&1 &)
  sleep 1
  lsof -nP -iTCP:17800 -sTCP:LISTEN >/dev/null 2>&1 && echo "      Broker 已启动" || { echo "      Broker 启动失败：$BROKER_LOG"; tail -10 "$BROKER_LOG"; }
fi

# 2) Runtime（带 bridge 外连）
if lsof -nP -iTCP:17371 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "[2/3] Runtime 已在运行 :17371（注意：须带 CANVAS_BRIDGE_SERVER 启动，否则不会注册到 Broker）"
else
  echo "[2/3] 启动 Runtime+bridge :17371 ..."
  (cd "$CANVAS_DIR" && \
    CANVAS_HOST=0.0.0.0 \
    CANVAS_AUTHORITY="127.0.0.1:17371,${LAN_IP}:17371" \
    CANVAS_BRIDGE_SERVER="http://127.0.0.1:17800" \
    CANVAS_BRIDGE_TOKEN="$BRIDGE_TOKEN" \
    CANVAS_BRIDGE_ID="mac-17371" \
    nohup node dist/index.js >> "$RUNTIME_LOG" 2>&1 &)
  sleep 3
  lsof -nP -iTCP:17371 -sTCP:LISTEN >/dev/null 2>&1 && echo "      Runtime 已启动" || { echo "      Runtime 启动失败：$RUNTIME_LOG"; tail -10 "$RUNTIME_LOG"; }
fi

# 3) MCP 网关
if lsof -nP -iTCP:17801 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "[3/3] 网关已在运行 :17801"
else
  echo "[3/3] 启动 MCP 网关 :17801 ..."
  (cd "$CANVAS_DIR" && \
    CANVAS_GATEWAY_PORT=17801 \
    CANVAS_GATEWAY_HOST=0.0.0.0 \
    CANVAS_GATEWAY_TOKEN="$GATEWAY_TOKEN" \
    CANVAS_BROKER_URL="http://127.0.0.1:17800" \
    CANVAS_BRIDGE_ID="mac-17371" \
    CANVAS_SCHEMA_RUNTIME_URL="http://127.0.0.1:17371" \
    CANVAS_SCHEMA_RUNTIME_TOKEN="$RUNTIME_TOKEN" \
    nohup node dist/bridge/gateway-server.js >> "$GATEWAY_LOG" 2>&1 &)
  sleep 2
  lsof -nP -iTCP:17801 -sTCP:LISTEN >/dev/null 2>&1 && echo "      网关已启动" || { echo "      网关启动失败：$GATEWAY_LOG"; tail -10 "$GATEWAY_LOG"; }
fi

echo
echo "== 外部 Agent 接入配置（把下面 URL + Token 配进任意 MCP 客户端）=="
echo "  本机:        http://127.0.0.1:17801/mcp"
echo "  局域网其他电脑: http://${LAN_IP}:17801/mcp"
echo "  Bearer Token: ${GATEWAY_TOKEN}"
echo
echo "== 服务状态 =="
echo "  Broker : http://127.0.0.1:17800/  （在线 bridge 列表）"
echo "  Runtime: http://127.0.0.1:17371/health"
echo "  网关   : http://127.0.0.1:17801/mcp"
