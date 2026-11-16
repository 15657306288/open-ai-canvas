#!/usr/bin/env bash
# 把本目录的快照灌进目标画布数据库（覆盖式：先 TRUNCATE 这几张表）
#
#   ./scripts/apply.sh --yes                  # 应用到默认生产容器
#   DB_CONTAINER=open-ai-canvas-postgres ./scripts/apply.sh --yes
#
# 注意：api_key 在快照里是空的，灌入后渠道全部丢失凭据，
#       必须在后台「渠道管理」里重新填写，或用你本地的密钥备份补齐。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DB_CONTAINER="${DB_CONTAINER:-open-ai-canvas-postgres}"
DB_USER="${DB_USER:-open_ai_canvas}"
DB_NAME="${DB_NAME:-open_ai_canvas}"

if [ "${1:-}" != "--yes" ]; then
  echo "这会清空 $DB_CONTAINER 的 channel_*/model_channels/system_settings 并覆盖为快照内容。"
  echo "确认请重跑：./scripts/apply.sh --yes"
  exit 1
fi

SQL="$ROOT/sql/channels-data.sql.gz"
[ -f "$SQL" ] || { echo "缺少 $SQL，先跑 ./scripts/pull.sh"; exit 1; }

echo "==> 备份当前数据到 /root/pre-apply-$(date +%Y%m%d-%H%M%S).sql.gz"
docker exec "$DB_CONTAINER" pg_dump -U "$DB_USER" -d "$DB_NAME" \
  -t public.channel_websites -t public.model_channels -t public.channel_models \
  -t public.channel_model_price_tiers -t public.logical_models -t public.system_settings \
  --data-only | gzip -9 > "/root/pre-apply-$(date +%Y%m%d-%H%M%S).sql.gz"

echo "==> 应用快照"
gunzip -c "$SQL" | docker exec -i "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1

echo "==> 清缓存并核对行数"
docker exec open-ai-canvas-redis redis-cli INCR canvas:logical-model-route-catalog:version >/dev/null
docker exec "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -P pager=off -A -F ' | ' -c "
SELECT 'channel_websites' t, count(*) FROM channel_websites
UNION ALL SELECT 'model_channels', count(*) FROM model_channels
UNION ALL SELECT 'channel_models', count(*) FROM channel_models
UNION ALL SELECT 'price_tiers', count(*) FROM channel_model_price_tiers
UNION ALL SELECT 'system_settings', count(*) FROM system_settings;"
echo
echo "完成。记得重新填写渠道 API Key。"
