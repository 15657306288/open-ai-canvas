#!/bin/bash
# ============================================================
# 影策 L3 服务器（launchd 托管版 · Mac 当服务器）
# 三件套：Broker(17800) + Runtime+bridge(17371) + MCP网关(17801)
# 开机自启 + 崩溃自动重启 + bridge 断线自动重注册
#
# 双击运行 = 查看状态；命令行可传参：
#   start / stop / restart / status / logs
# 集中配置：~/.infinite-canvas/l3.env（改 token/端口后 restart 生效）
# ============================================================
exec ~/.infinite-canvas/l3-manage.sh "${1:-status}"
