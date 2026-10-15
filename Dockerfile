# ---- 前端：构建 SPA，交给 nginx 独立服务（target: web）----
FROM node:24-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM nginx:1.28-alpine AS web
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=web-builder /src/web/dist/ /usr/share/nginx/html/

# ---- 后端：Go 构建（target: api）----
# 只拷贝 Go 实际依赖的路径：前端改动不会击穿 go-builder 的层缓存，反之亦然。
FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/center-cv-manager ./cmd/server

FROM alpine:3.23 AS api
RUN apk add --no-cache ca-certificates git openssh-client tzdata \
    && addgroup -S manager \
    && adduser -S -G manager manager \
    && mkdir -p /app/data \
    && chown -R manager:manager /app
WORKDIR /app
COPY --from=go-builder /out/center-cv-manager /usr/local/bin/center-cv-manager
COPY internal/templates/compose/ /app/templates/compose/
COPY internal/templates/nginx/ /app/templates/nginx/
COPY internal/templates/monitoring/ /app/templates/monitoring/
COPY internal/templates/secrets/ /app/templates/secrets/
COPY internal/templates/modules/ /app/templates/modules/
USER manager
EXPOSE 8099
VOLUME ["/app/data"]
ENTRYPOINT ["center-cv-manager"]
# 前端由 web 容器（nginx）提供；API 容器不携带静态文件，--web 指向的目录
# 缺失时仅提供 API（make dev 直跑时仍可直接服务 web/dist）。
CMD ["--listen", "0.0.0.0:8099", "--data", "/app/data", "--templates", "/app/templates"]
