# PingDrop

跨局域网与公网的极简网页端「文本 + 图片/小文件暂存 + 大文件直传」互传工具（Go + Vue 3 单二进制、零数据库部署）。

## 开发与测试（本机 Node + OrbStack Docker Go）

```bash
# 确保 OrbStack 已启动
make test
make build
```

## Docker 部署

```bash
docker build -t ping-drop:latest .
docker run -d --name ping-drop -p 127.0.0.1:8080:8080 ping-drop:latest \
  --addr :8080 --trust-proxy --max-relay-mb 100
```

反代配置请参考 `deploy/nginx.conf.example`（须关闭 `proxy_request_buffering` 与 `proxy_buffering`）。
