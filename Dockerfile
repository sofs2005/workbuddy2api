# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wb2api ./cmd/server

# alpine:3.20 已于 2026-04-01 EOL，apk 源下线后构建会失败，保持使用仍在支持期的分支
FROM alpine:3.22
RUN apk add --no-cache wget ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir -p /app/auths /app/data \
 && chown -R app:app /app
USER app
WORKDIR /app
COPY --from=build /out/wb2api /app/wb2api
# 内置默认配置：config.json 含密钥不入库，CI 从仓库构建时用它兜底。
# 实际部署请挂载真实 config.json 覆盖此文件（docker-compose.yml 已默认挂载）。
COPY config.example.json /app/config.json
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/app/wb2api", "-config", "/app/config.json"]
