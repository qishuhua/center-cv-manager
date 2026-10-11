FROM node:24-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/center-cv-manager ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates git openssh-client tzdata \
    && addgroup -S manager \
    && adduser -S -G manager manager \
    && mkdir -p /app/data \
    && chown -R manager:manager /app
WORKDIR /app
COPY --from=go-builder /out/center-cv-manager /usr/local/bin/center-cv-manager
COPY --from=web-builder /src/web/dist/ /app/web/
COPY internal/templates/compose/ /app/templates/compose/
COPY internal/templates/nginx/ /app/templates/nginx/
COPY internal/templates/monitoring/ /app/templates/monitoring/
COPY internal/templates/secrets/ /app/templates/secrets/
COPY internal/templates/modules/ /app/templates/modules/
USER manager
EXPOSE 8099
VOLUME ["/app/data"]
ENTRYPOINT ["center-cv-manager"]
CMD ["--listen", "0.0.0.0:8099", "--data", "/app/data", "--web", "/app/web", "--templates", "/app/templates"]
