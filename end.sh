#!/bin/bash
# AIgo 一键停止脚本
# 用法: ./end.sh

echo "=============================="
echo "  AIgo 服务停止"
echo "=============================="

# 1. 停止前端
echo ""
echo "[1/3] 停止前端..."
if pkill -f "vite" 2>/dev/null; then
  echo "  ✅ 前端已停止"
else
  echo "  ⏭  前端未在运行"
fi

# 2. 停止后端
echo ""
echo "[2/3] 停止后端..."
if pkill -f "aigo serve" 2>/dev/null; then
  echo "  ✅ 后端已停止"
else
  echo " ⏭  后端未在运行"
fi

# 3. 停止 PostgreSQL
echo ""
echo "[3/3] 停止 PostgreSQL..."
if pg_isready -q 2>/dev/null; then
  pg_ctl stop -D /opt/homebrew/var/postgresql@16 -m fast -w > /dev/null 2>&1
  echo "  ✅ PostgreSQL 已停止"
else
  echo "  ⏭  PostgreSQL 未在运行"
fi

echo ""
echo "=============================="
echo "  🛑 AIgo 已全部停止"
echo "=============================="
echo ""
