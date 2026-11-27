# PingDrop

跨局域网与公网的极简网页端「文本 + 图片/小文件暂存 + 大文件直传」互传工具（Go + Vue 3 单二进制、零数据库部署）。

## 核心特性

- **单二进制内嵌前端**：`embed.FS` 打包 Vue 3 前端静态资源，编译时使用 `CGO_ENABLED=0` 静态链接，无外部运行时依赖。
- **纯内存零数据库**：时间线消息与小文件全部驻留内存并按 TTL 自动过期清理，单空间默认最多保留 50 条。
- **直连 + 流式中转兜底**：支持基于 WebRTC 的大文件点对点直传，以及基于 `io.Pipe` 的 HTTPS 零内存堆积流式中转。

## 本地开发与构建

前置环境：本机安装 Node.js / `pnpm`，以及 Docker（如 OrbStack）。

```bash
# 确保 Docker / OrbStack 已启动
make dev          # 启动开发服务，本机端口 :18080 映射到容器 :8080
make test         # 运行前端类型检查与单测 (test-web) + Go vet + Go 竞态单测 (test-go)
make build        # 构建当前宿主机架构对应的 Linux 静态二进制 -> bin/ping-drop
make build-linux  # 交叉编译 linux/amd64 静态二进制 -> bin/ping-drop-linux-amd64
```

> 说明：`make build` 是在 `golang:1.23` Linux 容器中执行的，产物始终为 **Linux 静态可执行文件**（非 macOS 可执行文件）。在 Apple Silicon（`arm64`）Mac 上执行 `make build` 会生成 `linux/arm64` 二进制；若要部署到常见的 `x86_64` 云服务器，请使用 `make build-linux`。

## Linux 单二进制部署

先在目标服务器上执行 `uname -m` 确认 CPU 架构，再选择对应的构建产物上传：

- 输出 `x86_64`：本地执行 `make build-linux`，部署 `bin/ping-drop-linux-amd64`
- 输出 `aarch64` / `arm64`：本地执行 `make build`，部署 `bin/ping-drop`

```bash
# 上传到服务器后赋予执行权限
chmod +x ./ping-drop-linux-amd64

# 直接启动（或配合 systemd / Supervisor 托管）
./ping-drop-linux-amd64 --addr 127.0.0.1:8080 --trust-proxy --max-relay-mb 100
```

服务提供健康检查端点：`GET /healthz`。

## Docker 部署

```bash
# 本机与服务器同架构时直接构建
docker build -t ping-drop:latest .

# 若在 Apple Silicon Mac 上为 x86_64 服务器构建镜像
docker build --platform linux/amd64 -t ping-drop:latest .

docker run -d --name ping-drop -p 127.0.0.1:8080:8080 ping-drop:latest \
  --addr :8080 --trust-proxy --max-relay-mb 100
```

## 反向代理配置（Nginx）

参考 [deploy/nginx.conf.example](deploy/nginx.conf.example)，部署时需注意以下要点：

1. **关闭请求与响应缓冲**：必须配置 `proxy_request_buffering off;` 与 `proxy_buffering off;`，确保大文件流式中转零内存堆积。
2. **启用 WebSocket 转发**：配置 `proxy_http_version 1.1;` 及 `Upgrade` / `Connection` 请求头。
3. **匹配上传体积与超时**：`client_max_body_size` 应大于等于 `--max-relay-mb`（示例配置为 `110m`），并适当调高 `proxy_read_timeout` / `proxy_send_timeout`（示例中为 `3600s`）。
4. **信任代理头**：后端启动时需加上 `--trust-proxy`（通过 `X-Forwarded-For` 等请求头识别真实客户端 IP）。

## 运行参数（CLI Flag / 环境变量）

支持通过命令行 Flag 或环境变量配置（**命令行 Flag 优先级高于环境变量**）：

| CLI Flag | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | `PINGDROP_ADDR` | `:8080` | HTTP 与 WebSocket 监听地址 |
| `--trust-proxy` | `PINGDROP_TRUST_PROXY` | `false` | 信任来自回环/内网地址的代理头 |
| `--proxy-ip-header` | `PINGDROP_PROXY_IP_HEADER` | 空 | 显式指定单个代理客户端 IP 请求头 |
| `--allowed-origins` | `PINGDROP_ALLOWED_ORIGINS` | 空 | 允许的 WebSocket Origin 列表（逗号分隔） |
| `--lan-shared-timeline` | `PINGDROP_LAN_SHARED_TIMELINE` | `false` | 在默认局域网空间启用共享时间线 |
| `--ttl` | `PINGDROP_TTL` | `30m` | 内存时间线条目过期时间（单空间默认最多保留 50 条） |
| `--room-grace` | `PINGDROP_ROOM_GRACE` | `10m` | 空房间宽限销毁时间 |
| `--max-rooms` | `PINGDROP_MAX_ROOMS` | `1000` | 全局最大活跃房间数 |
| `--max-timeline-file-mb` | `PINGDROP_MAX_TIMELINE_FILE_MB` | `20` | 时间线单个文件（含图片）上限（MB） |
| `--max-space-blob-mb` | `PINGDROP_MAX_SPACE_BLOB_MB` | `60` | 单个空间文件总内存上限（MB） |
| `--max-blob-memory-mb` | `PINGDROP_MAX_BLOB_MEMORY_MB` | `256` | 全局文件内存池上限（MB） |
| `--max-relay-mb` | `PINGDROP_MAX_RELAY_MB` | `100` | 单文件 HTTPS 流式中转上限（MB，设为 `0` 禁用中转） |
| `--ice-servers` | `PINGDROP_ICE_SERVERS` | `[{"urls":"stun:stun.l.google.com:19302"}]` | WebRTC `RTCIceServer` JSON 数组 |
