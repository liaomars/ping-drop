# Stage 1: 构建前端产物
FROM node:22-alpine AS web-builder
WORKDIR /src/web
RUN npm install -g pnpm@10.24.0
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: 静态编译单二进制（CGO_ENABLED=0）
FROM golang:1.23-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ping-drop ./cmd/ping-drop

# Stage 3: 极简运行时镜像
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /out/ping-drop /usr/local/bin/ping-drop
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/ping-drop"]
CMD ["--addr", ":8080", "--trust-proxy"]
