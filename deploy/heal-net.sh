#!/bin/sh
# 小书童 · 容器网络别名自愈
#
# 症状：主机重启后 docker 恢复容器，ls-core 在 NetworkSettings 里的
#       compose 别名被清成 []，只剩容器名。前端 nginx 配的上游是
#       core-server → 解析不到 → 所有 /api/ 返回 502，
#       页面表现为「连不上服务器 / 数据库」。
# 判据：ls-core 网络别名里必须含 core-server。
# 动作：disconnect + connect --alias 补回，不重启任何容器。
set -u

NET=ls-deploy_ls-net
NAME=ls-core
LOG=/vol1/1000/ls-deploy/heal-net.log

log() { echo "$(date '+%F %T') $*" >> "$LOG" 2>/dev/null; }

# 容器不存在或没在跑 → 交给 compose / restart 策略，别插手
docker inspect "$NAME" >/dev/null 2>&1 || exit 0
[ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ] || exit 0

# 启动 90 秒内不碰：docker 可能还在恢复网络，抢跑会打断它
started=$(docker inspect -f '{{.State.StartedAt}}' "$NAME" 2>/dev/null)
start_ts=$(date -d "$started" +%s 2>/dev/null || echo 0)
now_ts=$(date +%s)
if [ "$start_ts" -gt 0 ] && [ $((now_ts - start_ts)) -lt 90 ]; then
    exit 0
fi

aliases=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$v.Aliases}}{{end}}' "$NAME" 2>/dev/null)
case "$aliases" in
    *core-server*) exit 0 ;;
esac

log "别名缺失（当前: ${aliases:-空}），开始补回"
docker network disconnect "$NET" "$NAME" >/dev/null 2>&1
if docker network connect --alias core-server --alias ls-core "$NET" "$NAME" >/dev/null 2>&1; then
    log "已补回 core-server + ls-core"
    sleep 3
    if docker exec ls-frontend getent hosts core-server >/dev/null 2>&1; then
        log "校验通过：前端已能解析 core-server"
    else
        log "校验失败：前端仍解析不到 core-server"
    fi
else
    log "补别名失败！ls-core 可能已脱离 $NET，需人工处理"
fi
