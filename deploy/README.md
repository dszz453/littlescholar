# 服务器侧部署杂项

## `ls-deploy-up.service`

开机自愈单元：`docker compose up -d` 再跑一次，把**没建上网络端点**的容器
重建并重新接入 `ls-net`。

### 为什么需要它

主机重启后出现过 `ls-core` / `ls-kid` / `ls-frontend` 一起 Restarting：

- `ls-core` 日志：`lookup postgres on [fd9d:...]:53: network is unreachable`
- `ls-kid` 日志：`nginx: [emerg] host not found in upstream "core-server"`

表面像"postgres 挂了"，实际 postgres 是 healthy 的。真因是这三个容器的
**网络端点没建上**（`docker inspect` 里 `EndpointID` 为空、`ls-net` 成员
里查不到它们），于是容器名互相解析不到。

`restart: unless-stopped` 只能重启容器，**不会**重建网络端点，所以会一直
循环。手工 `docker compose up -d --force-recreate` 能恢复，但每次重启都要
人工介入，于是加了这个单元。

### 安装

```sh
sudo cp deploy/ls-deploy-up.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ls-deploy-up.service
```

### 验证

```sh
systemctl is-enabled ls-deploy-up.service   # enabled
systemctl is-active  ls-deploy-up.service   # active
docker ps --format '{{.Names}} {{.Status}}' | grep ls-
```

注意：它 `After=docker.service`，并 `sleep 20` 再执行，避免和 docker 自己
拉起容器的动作打架。

---

## `heal-net.sh` + `ls-heal.service` / `.timer`

网络别名自愈，每 2 分钟检查一次。**和上面那个是同一族故障的第二个变种。**

### 为什么需要它（2026-10-26 线上实录）

现象和上次几乎一样，但真因不同：

| | 上一次 | 这一次 |
|---|---|---|
| `EndpointID` | 空（端点没建上） | **正常** |
| `NetworkSettings.Aliases` | 正常 | **`[]`（空）** |
| `ls-core` 容器名解析 | 失败 | **成功** |
| `core-server` 解析 | 失败 | **失败** |
| 表现 | 三容器一起 Restarting | 容器全 healthy，页面能打开，但所有 `/api/` 返回 **502** |

前端 nginx 的上游写的是 compose 服务名 `core-server`，别名一丢就解析不到：

```
[error] core-server could not be resolved (3: Host not found)
```

用户看到的是"登录不上 / 连不上数据库"，而后端日志里清清楚楚写着
`数据库连接成功，迁移完成` —— **只要看到「连不上数据库」先别查 pg**，
先 `curl http://127.0.0.1:8090/api/health`；502 就是 nginx 到后端的这一段
断了，不是 pg。

关键事实：**容器名解析（`ls-core`）和网络别名（`core-server`）是两套记录**。
别名会被 docker 在恢复容器时清空，容器名不会。所以 nginx 的上游现在
统一用 `ls-core`（`frontend-admin/nginx.conf`、`frontend-kid/nginx.conf`）。

### 它做什么

```
aliases 里没有 core-server
  → docker network disconnect ls-deploy_ls-net ls-core
  → docker network connect --alias core-server --alias ls-core ls-deploy_ls-net ls-core
  → 校验 ls-frontend 能解析 core-server
```

不重启任何容器；nginx 的 `resolver 127.0.0.11 valid=10s` 会在 10 秒内自己
重新解析，所以补完别名约 10 秒后 `/api/` 自然恢复。
启动 90 秒内的容器不动（避免和 docker 恢复网络的动作抢跑）。

### 安装

```sh
sudo cp deploy/heal-net.sh /vol1/1000/ls-deploy/heal-net.sh
sudo chmod +x /vol1/1000/ls-deploy/heal-net.sh
sudo cp deploy/ls-heal.service deploy/ls-heal.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ls-heal.timer
```

### 验证

```sh
systemctl list-timers ls-heal.timer --no-pager
cat /vol1/1000/ls-deploy/heal-net.log     # 只在真的补过别名时才有内容
```

故障注入验证（制造一次别名丢失，看它能不能自己补回来）：

```sh
sudo docker network disconnect ls-deploy_ls-net ls-core
sudo docker network connect    ls-deploy_ls-net ls-core
docker inspect ls-core --format '{{range $k,$v := .NetworkSettings.Networks}}{{$v.Aliases}}{{end}}'   # []
curl -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/api/health                                # 502
sudo systemctl start ls-heal.service
sleep 12; curl -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/api/health                      # 200
```

