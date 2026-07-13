#!/usr/bin/env bash
# DanmakuFlow 双实例集成测试
#
# 一键启动完整系统并运行集成测试:
#   bash scripts/integration-test.sh
#
# 仅运行集成测试（假设系统已在运行）:
#   bash scripts/integration-test.sh --skip-build
#
# 清理（不删除卷）:
#   bash scripts/integration-test.sh --down
#
# 清理（删除全部）:
#   bash scripts/integration-test.sh --clean

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

log() { echo -e "\033[1;34m[INFO]\033[0m $*"; }
ok()  { echo -e "\033[1;32m[OK]\033[0m $*"; }
err() { echo -e "\033[1;31m[ERR]\033[0m $*"; }

SKIP_BUILD=false
CMD="${1:-}"

if [ "$CMD" = "--skip-build" ]; then
    SKIP_BUILD=true
elif [ "$CMD" = "--down" ]; then
    log "停止服务（保留数据卷）..."
    docker compose down
    exit 0
elif [ "$CMD" = "--clean" ]; then
    log "停止服务并清除数据卷..."
    docker compose down -v
    exit 0
fi

if ! $SKIP_BUILD; then
    log "构建并启动 Docker 服务..."
    docker compose up --build -d

    log "等待服务就绪（最长 60 秒）..."
    for i in $(seq 1 60); do
        if curl -sf http://localhost:8080/healthz >/dev/null 2>&1; then
            ok "Nginx 就绪"
            break
        fi
        if [ "$i" -eq 60 ]; then
            err "服务启动超时"
            docker compose logs --tail=20
            exit 1
        fi
        sleep 1
    done

    # 额外等待 MySQL 表创建和 Redis 订阅建立
    sleep 3
fi

log "运行集成测试..."
go run ./scripts/integration.go
EXIT_CODE=$?

if [ $EXIT_CODE -eq 0 ]; then
    ok "全部测试通过"
else
    err "测试失败 (exit=$EXIT_CODE)"
    log "服务日志:"
    docker compose logs --tail=20
fi

exit $EXIT_CODE
