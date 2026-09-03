#!/usr/bin/env bash
# 双击即可更新影策（会自动打开“终端”窗口）
cd "$(dirname "$0")"
clear
echo "============================================"
echo "  影策 open-ai-canvas 一键更新"
echo "  中途不要关闭窗口；想取消直接关窗口即可"
echo "============================================"
bash ./update-yingce.sh
STATUS=$?
echo
if [ "$STATUS" -eq 0 ]; then
  echo "✅ 完成，浏览器刷新 https://yingce.cc.cd（建议 Cmd+Shift+R）"
else
  echo "❌ 更新未完成，请把本窗口里的红色文字截图发来"
fi
echo
echo "按任意键关闭窗口…"
read -n1 -s
