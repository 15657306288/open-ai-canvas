#!/usr/bin/env bash
# 从画布服务器重新拉取渠道/模型/价格/设置快照，覆盖本目录的 json/ sql/ config/ meta.json
#
#   ./scripts/pull.sh                    # 默认走 Tailscale
#   CANVAS_SSH_HOST=root@1.2.3.4 CANVAS_SSH_PORT=22 ./scripts/pull.sh
set -euo pipefail

HOST="${CANVAS_SSH_HOST:-root@100.73.133.67}"
PORT="${CANVAS_SSH_PORT:-11890}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REMOTE_DIR="/root/_state_export"
REMOTE_TGZ="/root/yingce-state-snapshot.tar.gz"
STAMP="$(date +%Y%m%d-%H%M%S)"

SSH_OPTS=(-p "$PORT" -o StrictHostKeyChecking=no -o IPQoS=none -o ConnectTimeout=15)

echo "==> 上传导出脚本"
scp "${SSH_OPTS[@]}" "$ROOT/scripts/export-state.py" "$HOST:/root/export-state.py"

echo "==> 在服务器导出快照"
ssh "${SSH_OPTS[@]}" "$HOST" "python3 /root/export-state.py $REMOTE_DIR && \
  mkdir -p $REMOTE_DIR/config && \
  cp /opt/commerce-brain/app/deploy/Caddyfile $REMOTE_DIR/config/Caddyfile 2>/dev/null || true; \
  cp /etc/systemd/system/canvas-bridge-18080.service $REMOTE_DIR/config/canvas-bridge-18080.service 2>/dev/null || true; \
  python3 - <<'PY'
import json
d = json.load(open('/opt/open-ai-canvas/data/backend/plugin_registry.json'))
keys = ('id', 'source', 'packagePath', 'packageSha256', 'updatedAt', 'version', 'enabled')
out = [{k: it.get(k) for k in keys} for it in d]
json.dump(out, open('$REMOTE_DIR/config/plugin-registry-slim.json', 'w'), ensure_ascii=False, indent=1, sort_keys=True)
PY
  gzip -9 -f $REMOTE_DIR/sql/channels-data.sql && \
  tar -czf $REMOTE_TGZ -C $REMOTE_DIR . && ls -l $REMOTE_TGZ"

echo "==> 下载快照"
TMP="$(mktemp -d)"
scp "${SSH_OPTS[@]}" "$HOST:$REMOTE_TGZ" "$TMP/snapshot.tar.gz"

echo "==> 展开到 $ROOT"
rm -rf "$ROOT/json" "$ROOT/sql" "$ROOT/config" "$ROOT/meta.json"
tar -xzf "$TMP/snapshot.tar.gz" -C "$ROOT"
rm -rf "$TMP"

echo
echo "完成。改动概览："
git -C "$ROOT/../.." status --short -- deploy/yingce-state || true
echo "快照时间：$(python3 -c "import json;print(json.load(open('$ROOT/meta.json'))['generated_at'])")"
echo "（生成于 $STAMP 的拉取动作）"
