#!/usr/bin/env bash
# 影策 open-ai-canvas · macOS Docker 一键更新脚本
# 作用等价于官方「在线热更新」，但适配 macOS（官方仅支持 Linux+systemd），
# 并且走本地源码构建，能保留 backend/internal/protocol/builtin.go 里的「红鸟」自定义渠道。
# 流程：检查环境 -> 拉代码 -> 保护红鸟定制 -> 备份数据库 -> 构建 -> 迁移+重建 -> 验证
set -Eeuo pipefail

APP_DIR="$HOME/open-ai-canvas"
BACKUP_DIR="$HOME/open-ai-canvas-backups"
PG_USER="open_ai_canvas"
PG_DB="open_ai_canvas"
PUBLIC_URL="https://yingce.cc.cd"

blue(){ echo -e "\n\033[1;36m==> $*\033[0m"; }
ok(){   echo -e "\033[1;32m[OK] $*\033[0m"; }
die(){  echo -e "\033[1;31m[失败] $*\033[0m"; exit 1; }

cd "$APP_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUP_DIR"
LOG="$BACKUP_DIR/update-$STAMP.log"
exec > >(tee -a "$LOG") 2>&1

DC=(docker compose --env-file .env -f docker-compose.deploy.yml -f docker-compose.build.yml)

# 0. 前置检查
docker info >/dev/null 2>&1 || die "Docker 没在运行，请先启动 Docker Desktop 后重试。"
[ -f docker-compose.deploy.yml ] || die "没找到部署目录 $APP_DIR"
command -v git >/dev/null || die "缺少 git"

BEFORE="$(tr -d '\r\n' < VERSION)"
blue "当前版本：$BEFORE"

# 1. 拉取远程引用
blue "检查远程更新…"
git fetch origin main
BEHIND="$(git rev-list --count HEAD..origin/main)"
if [ "$BEHIND" = "0" ]; then
  ok "已是最新版本（$BEFORE），无需更新。"
  "${DC[@]}" up -d --wait --remove-orphans
  blue "服务已确认在运行。日志：$LOG"
  exit 0
fi
echo "落后 $BEHIND 个提交，远程最新：$(git show -s --format='%h %s' origin/main | cut -c1-80)"

# 2. 保护本地「红鸟」定制
HN_PATH="backend/internal/protocol/builtin.go"
if ! git diff --quiet -- "$HN_PATH" 2>/dev/null; then
  cp "$HN_PATH" "$BACKUP_DIR/builtin.go.local-$STAMP.bak"
  git diff -- "$HN_PATH" > "$BACKUP_DIR/hongniao-adapter-$STAMP.patch"
  if git diff --name-only HEAD..origin/main | grep -qx "$HN_PATH"; then
    die "上游本次也修改了 $HN_PATH，可能与本地红鸟定制冲突，已中止（未做任何改动）。
定制备份在 $BACKUP_DIR/hongniao-adapter-$STAMP.patch，手动合并后再运行本脚本。"
  fi
  ok "已备份本地红鸟定制（上游未改该文件，可安全保留）"
fi

# 3. 更新前备份数据库
blue "备份数据库…"
"${DC[@]}" exec -T postgres pg_dump -U "$PG_USER" -d "$PG_DB" --no-owner \
  | gzip > "$BACKUP_DIR/yingce-db-pre-update-$STAMP.sql.gz"
[ -s "$BACKUP_DIR/yingce-db-pre-update-$STAMP.sql.gz" ] || die "数据库备份为空，已中止"
ok "数据库已备份：$BACKUP_DIR/yingce-db-pre-update-$STAMP.sql.gz"

# 4. 合并最新代码（本地未冲突改动会保留）
blue "合并最新代码…"
git pull --ff-only origin main
AFTER="$(tr -d '\r\n' < VERSION)"
grep -q "hongniao" "$HN_PATH" || die "更新后红鸟适配在源码中消失，已停止重建；可用 $BACKUP_DIR/builtin.go.local-$STAMP.bak 恢复。"
echo "版本：$BEFORE -> $AFTER"

# 5. 构建镜像
blue "构建镜像（首次约几分钟，走 goproxy.cn）…"
"${DC[@]}" build migrate backend web

# 6. 滚动重建（migrate 先迁移 schema，再 backend、web）
blue "重建容器并自动迁移数据库…"
"${DC[@]}" up -d --wait --remove-orphans

# 7. 验证
blue "验证服务…"
sleep 3
HEALTH="$(curl -s --max-time 15 http://127.0.0.1:3100/api/health || true)"
echo "$HEALTH" | grep -q '"status":"ok"' || die "本机健康检查未通过，请运行：cd $APP_DIR && ${DC[*]} logs --tail=80 backend"
echo "$HEALTH" | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print('运行版本:',d['build']['version'],'| schema:',d['schema']['current'],'/',d['schema']['expected'],'| checks:',d['checks'])" 2>/dev/null || echo "$HEALTH"
PUB="$(curl -s -A "Mozilla/5.0" -o /dev/null -w '%{http_code}' --max-time 20 "$PUBLIC_URL/" || echo 000)"
echo "公网 ${PUBLIC_URL} -> HTTP ${PUB}（200=正常；若代理没开可能显示 000，不影响本机）"

ok "更新完成：$BEFORE -> $AFTER"
echo "本次完整日志：$LOG"
