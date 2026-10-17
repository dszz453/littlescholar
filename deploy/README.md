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
