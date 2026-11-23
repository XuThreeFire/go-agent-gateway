#!/usr/bin/env bash
# 仅构建/启动当前机器上的 Compose 服务，不访问任何远程服务器。
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
docker compose up -d --build web
