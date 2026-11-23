#!/usr/bin/env bash
# 只创建本项目专用的可丢弃测试环境，不连接现有数据库或绑定宿主端口。
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif [[ -x bin/docker-compose ]]; then
  compose=("$PWD/bin/docker-compose")
else
  echo '缺少 Docker Compose，请安装 Docker Desktop 或 Compose 插件。' >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo 'Docker 引擎未启动或不可达；请先启动 Docker Desktop。尚未创建测试容器。' >&2
  exit 1
fi
compose+=(-p "go-agent-gateway-tests-$$" -f compose.test.yaml)
cleanup() { "${compose[@]}" down --volumes --remove-orphans >/dev/null; }
trap cleanup EXIT
"${compose[@]}" run --rm tests
