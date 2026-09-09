#!/usr/bin/env bash
# 启动 AIgo Docker Compose 部署（首次运行会生成部署配置模板）。

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")" && pwd)"
DEPLOY_DIR="$PROJECT_ROOT/deploy"
ENV_FILE="$DEPLOY_DIR/production.env"
ENV_TEMPLATE="$DEPLOY_DIR/production.env.example"

ensure_docker_ready() {
  command -v docker >/dev/null 2>&1 || {
    echo "未找到 Docker 命令，请先安装 Docker Desktop 或 Docker Engine。" >&2
    exit 1
  }

  if docker info >/dev/null 2>&1; then
    return
  fi

  if [ "$(uname -s)" = "Darwin" ] && command -v open >/dev/null 2>&1; then
    echo "正在启动 Docker Desktop，最多等待 60 秒..."
    open -a Docker
    for attempt in {1..30}; do
      if docker info >/dev/null 2>&1; then
        return
      fi
      sleep 2
    done
  fi

  echo "Docker 守护进程未就绪，请启动 Docker 后重试。" >&2
  exit 1
}

ensure_deploy_config() {
  if [ -r "$ENV_FILE" ]; then
    return
  fi

  cp "$ENV_TEMPLATE" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  echo "已创建 $ENV_FILE。"
  echo "请检查数据库、域名和 Compose Secrets；实时 AI 地址/模型可在超级管理员登录后从“AI 服务配置”页面填写。" >&2
  exit 1
}

ensure_secrets() {
  local required_secret
  for required_secret in postgres_password jwt_secret admin_password; do
    if [ ! -s "$DEPLOY_DIR/secrets/$required_secret" ]; then
      echo "正在初始化 Compose Secrets..."
      "$DEPLOY_DIR/init-secrets.sh"
      return
    fi
  done
}

ensure_docker_ready
ensure_deploy_config
ensure_secrets

echo "检查 Docker Compose 配置..."
"$DEPLOY_DIR/compose.sh" config --quiet

echo "构建并启动 AIgo（PostgreSQL、迁移、应用、Caddy、备份）..."
"$DEPLOY_DIR/compose.sh" up -d --build

echo ""
echo "当前容器状态："
"$DEPLOY_DIR/compose.sh" ps
echo ""
echo "启动完成。首次构建或拉取镜像需要一些时间；可用以下命令查看日志："
echo "  ./deploy/compose.sh logs -f --tail=200 app caddy postgres backup"
