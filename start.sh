#!/bin/bash
# AIgo 一键启动脚本
# 用法: ./start.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PG_DATA="/opt/homebrew/var/postgresql@16"
mkdir -p "$SCRIPT_DIR/output"

backend_port=8080
frontend_port=5173

listener_pid() {
  local port="$1"
  if ! command -v lsof >/dev/null 2>&1; then
    return 0
  fi
  lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | head -1
}

listener_command() {
  local pid="$1"
  ps -p "$pid" -o command= 2>/dev/null | sed 's/^[[:space:]]*//'
}

fail_if_port_is_busy() {
  local port="$1"
  local label="$2"
  local pid
  pid="$(listener_pid "$port")"
  if [ -n "$pid" ]; then
    echo "  ❌ $label端口 $port 已被进程占用（PID: $pid）：$(listener_command "$pid")" >&2
    echo "     如果这是旧的 AIgo 进程，请先执行 ./end.sh；只想停止该进程可执行 kill -TERM $pid。" >&2
    return 1
  fi
  return 0
}

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

# 确保 aigo 数据库存在（使用当前系统用户连接，默认连 postgres 库）
PG_USER="$(whoami)"
psql -U "$PG_USER" -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='aigo'" | grep -q 1 || {
  echo "  📦 创建 aigo 数据库..."
  psql -U "$PG_USER" -d postgres -c "CREATE DATABASE aigo;"
  echo "  ✅ 数据库 aigo 已创建"
}

# 2. 启动后端
echo ""
echo "[2/3] 启动后端 Go 服务..."
cd "$SCRIPT_DIR"
if curl -fsS "http://127.0.0.1:$backend_port/health/ready" > /dev/null 2>&1; then
  echo "  ✅ 后端已在运行 (端口: $backend_port；如需加载新代码，请先执行 ./end.sh)"
else
  fail_if_port_is_busy "$backend_port" "后端" || exit 1
  go build -o aigo ./cmd/aigo
  echo "  ⏳ 执行版本化数据库迁移..."
  if ! ./aigo migrate > output/migrate.log 2>&1; then
    echo "  ❌ 数据库迁移失败，后端不会启动。最近日志："
    tail -30 "$SCRIPT_DIR/output/migrate.log" 2>/dev/null || true
    exit 1
  fi
  echo "  ✅ 数据库迁移完成"
  nohup ./aigo serve > output/server.log 2>&1 &
  BACKEND_PID=$!
  echo "  ⏳ 后端进程已创建 (PID: $BACKEND_PID)，等待 HTTP 就绪..."

  # 等待后端就绪
  BACKEND_READY=0
  for i in {1..10}; do
    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      break
    fi
    if curl -fsS "http://127.0.0.1:$backend_port/health/ready" > /dev/null 2>&1; then
      BACKEND_READY=1
      break
    fi
    sleep 0.5
  done
  if [ "$BACKEND_READY" -ne 1 ]; then
    echo "  ❌ 后端启动失败，前端不会继续启动。最近日志："
    tail -30 "$SCRIPT_DIR/output/server.log" 2>/dev/null || true
    exit 1
  fi
  echo "  ✅ 后端已启动 (PID: $BACKEND_PID, 端口: $backend_port)"
fi

# 3. 启动前端
echo ""
echo "[3/3] 启动前端 Vite 开发服务器..."
cd "$SCRIPT_DIR/web"
if curl -fsS "http://127.0.0.1:$frontend_port/" > /dev/null 2>&1; then
  echo "  ✅ 前端已在运行 (端口: $frontend_port)"
else
  fail_if_port_is_busy "$frontend_port" "前端" || exit 1
  nohup npm run dev -- --host 127.0.0.1 --port "$frontend_port" > "$SCRIPT_DIR/output/frontend.log" 2>&1 &
  FRONTEND_PID=$!
  FRONTEND_READY=0
  for i in {1..10}; do
    if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
      break
    fi
    if curl -fsS "http://127.0.0.1:$frontend_port/" > /dev/null 2>&1; then
      FRONTEND_READY=1
      break
    fi
    sleep 0.5
  done
  if [ "$FRONTEND_READY" -ne 1 ]; then
    echo "  ❌ 前端启动失败。最近日志："
    tail -30 "$SCRIPT_DIR/output/frontend.log" 2>/dev/null || true
    exit 1
  fi
  echo "  ✅ 前端已启动 (PID: $FRONTEND_PID, 端口: $frontend_port)"
fi

echo ""
echo "=============================="
echo "  🚀 AIgo 启动完成！"
echo "=============================="
echo ""
echo "  前端地址: http://localhost:5173"
echo "  后端地址: http://localhost:$backend_port"
echo ""
echo "  日志文件:"
echo "    后端:   output/server.log"
echo "    迁移:   output/migrate.log"
echo "    前端:   output/frontend.log"
echo "    数据库: output/pg.log"
echo ""
echo "  停止服务: ./end.sh"
echo ""
