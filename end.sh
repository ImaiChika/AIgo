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
# 注意顺序：
# 1) 先 launchctl bootout 移除 launchd 守护（brew services 安装的 plist 带
#    KeepAlive=true，若直接 pg_ctl stop 会被 launchd 立即重启，且 pg_ctl -w
#    会永久等待卡死；brew services stop 在部分 Homebrew 版本存在 bug 会报
#    undefined method 'stop_timeout'）
# 2) 再 pg_ctl stop -W（显式不等待；pg_ctl 默认会等待，不能只靠省略 -w）
# 3) 兜底 pkill（快速模式无响应时）
echo ""
echo "[3/3] 停止 PostgreSQL..."
if pg_isready -q 2>/dev/null; then
  launchctl bootout gui/$(id -u)/homebrew.mxcl.postgresql@16 2>/dev/null
  pg_ctl stop -D /opt/homebrew/var/postgresql@16 -m fast -W > /dev/null 2>&1 || true
  # 等待最多 5 秒确认
  for i in 1 2 3 4 5; do
    pg_isready -q 2>/dev/null || break
    sleep 1
  done
  if pg_isready -q 2>/dev/null; then
    # 兜底：直接终止残留进程
    pkill -f "postgres -D /opt/homebrew/var/postgresql@16" 2>/dev/null
    sleep 1
  fi
  if pg_isready -q 2>/dev/null; then
    echo "  ⚠ PostgreSQL 仍在运行，请手动执行: launchctl bootout gui/\$(id -u)/homebrew.mxcl.postgresql@16"
  else
    echo "  ✅ PostgreSQL 已停止"
  fi
else
  echo "  ⏭  PostgreSQL 未在运行"
fi

echo ""
echo "=============================="
echo "  🛑 AIgo 已全部停止"
echo "=============================="
echo ""
