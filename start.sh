#!/bin/bash
# AIgo 一键启动脚本
# 用法: ./start.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PG_DATA="/opt/homebrew/var/postgresql@16"

echo "=============================="
echo "  AIgo 服务启动"
echo "=============================="

# 1. 启动 PostgreSQL
echo ""
echo "[1/3] 启动 PostgreSQL..."
if pg_isready -q 2>/dev/null; then
  echo "  ✅ PostgreSQL 已在运行"
else
  pg_ctl -D "$PG_DATA" start -l "$SCRIPT_DIR/output/pg.log" -w
  echo "  ✅ PostgreSQL 已启动"
fi

# 等待 PostgreSQL 就绪
for i in {1..10}; do
  if pg_isready -q 2>/dev/null; then
    break
  fi
  sleep 0.5
done

# 确保 aigo 数据库存在
psql -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='aigo'" | grep -q 1 || {
  echo "  📦 创建 aigo 数据库..."
  psql -U postgres -c "CREATE DATABASE aigo;"
  echo "  ✅ 数据库 aigo 已创建"
}

# 2. 启动后端
echo ""
echo "[2/3] 启动后端 Go 服务..."
cd "$SCRIPT_DIR"
go build -o aigo ./cmd/aigo
nohup ./aigo serve > output/server.log 2>&1 &
BACKEND_PID=$!
echo "  ✅ 后端已启动 (PID: $BACKEND_PID, 端口: 8080)"

# 等待后端就绪
for i in {1..10}; do
  if curl -s http://127.0.0.1:8080/api/stats > /dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

# 3. 启动前端
echo ""
echo "[3/3] 启动前端 Vite 开发服务器..."
cd "$SCRIPT_DIR/web"
nohup npm run dev > "$SCRIPT_DIR/output/frontend.log" 2>&1 &
FRONTEND_PID=$!
echo "  ✅ 前端已启动 (PID: $FRONTEND_PID, 端口: 5173)"

echo ""
echo "=============================="
echo "  🚀 AIgo 启动完成！"
echo "=============================="
echo ""
echo "  前端地址: http://localhost:5173"
echo "  后端地址: http://localhost:8080"
echo ""
echo "  日志文件:"
echo "    后端:   output/server.log"
echo "    前端:   output/frontend.log"
echo "    数据库: output/pg.log"
echo ""
echo "  停止服务: ./end.sh"
echo ""
