# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

Center CV Manager：运行在开发机上的中心服务器集中管理服务。Go (Gin) 后端 + Vue 3 (Element Plus) 前端，编译为单一二进制。核心链路：登记中心服务器 SSH 信息 → 在开发机本地缓存 Git 源码（`data/sources`）→ 通过 SSH Unix Socket 转发直连远端 Docker Engine API 完成构建/部署。远端不装 Agent、不落源码副本。

## 常用命令

```powershell
# 完整构建（前端 + 同步 embed 目录 + Go 编译）
make build            # 产物 build/center-cv-manager.exe

# 仅构建前端并同步到 cmd/server/web_dist
make web

# 本地开发运行后端（http://127.0.0.1:8099）
make dev              # 等价于 go run ./cmd/server --listen 127.0.0.1:8099 --data ./data

# 测试（Go 全部 + 前端 vue-tsc 类型检查及构建）
make test             # 等价于 go test ./... && cd web && npm run build

# 运行单个包/单个测试
go test ./internal/build/...
go test ./internal/build/ -run TestXxx

# 前后端分离开发：后端跑 8099，另开终端
cd web && npm run dev

# Docker 方式运行（局域网 0.0.0.0:8099）
docker compose up -d --build
```

环境要求：Go 1.26、Node.js、Git。

## 架构

### 启动装配（cmd/server/main.go）

main.go 是唯一装配点：打开 SQLite（`data/manager.db`）→ 加载/创建 `data/master.key`（`secret.LoadOrCreate`）→ 构造各 service 并注入 `api.New(...)`。前端产物通过 `//go:embed web_dist` 嵌入二进制，`api/server.go` 的 NoRoute 兜底返回 SPA 页面。

### 分层结构

- `internal/api` — Gin 路由与 handler，统一 envelope 响应（`{code, message, data}`）。除 `/health`、`/auth/*` 外全部走 `requireAuth()` Cookie Session 中间件。路由注册集中在 `server.go` 的 `Router()`。
- `internal/auth` — 登录与 Session 管理：bcrypt 密码、12 小时 Cookie、首登强制改密（可跳过）、连续失败锁定 5 分钟；凭据存 `data/` 下的 JSON 文件而非 SQLite。
- `internal/centerlock` — 进程内按中心 ID 的互斥锁（`Acquire`/`AcquireContext`），串行化对同一中心的状态变更操作，不同中心互不阻塞。deploy、centeraction、baseimage、provision、modelasset 均使用。
- `internal/alerting` — 全局飞书机器人 Webhook 凭据（加密存储），provision 等流程渲染通知时经它取凭据并自动带入中心名称。
- `internal/domain` — 跨包共享的模型（Center、Source、Task、Checkout、凭据等），service 间通过 `Repository` / `CenterResolver` 等小接口解耦。
- 各业务包（均以 service 为入口）：
  - `center` — 中心服务器 CRUD、SSH/Docker 连通性测试、host key 指纹管理；其他包通过它解析 Center 配置。
  - `source` — Git 仓库登记、ls-remote 查版本；发布选择锁定 commit，仅在需要构建时自动准备 checkout 到本地 `sources/` 目录，并校验分支漂移。
  - `build` — 用 Docker SDK 调远端 Build API，构建上下文从本地 checkout 生成 tar 流经 SSH 隧道推送。
  - `deploy` — 部署计划（Check）与执行（Run）：比对远端镜像，直接更新中心唯一的 Compose 文件中的镜像与版本标签；不创建或叠加额外 Compose 配置，SSH 启动时清理孤儿容器。
  - `provision` — 中心初始化：上传 compose 模板、检测 CPU/GPU、分配 worker 资源。
  - `centeraction` — 容器/compose 的启停、重启、清理等运维动作。
  - `baseimage` — 阿里云 OSS 基础镜像包目录与中心升级（签名 URL 下载、ETag 缓存判断）。
  - `modelasset` — 模型版本管理：OSS 上传（签名 URL 直传 + 流式代理回退、逐文件 SHA-256）、不可变 Manifest 发布（`schema_version: 1`，按 ONNX/登临/NVIDIA 多格式）与中心工作区 `data/models` 增量同步（签名 URL、ETag、断点续传）。
- `internal/sshx` — SSH 连接与 `DockerClient`：核心是把 `http.Transport` 的 DialContext 换成 `sshClient.Dial("unix", socket)`，从而让 Docker SDK 走 SSH 隧道访问远端 docker.sock。改动 Docker/SSH 相关逻辑先看这里。
- `internal/task` — 异步任务管理器：`Submit(kind, params, executor)` 持久化任务、内存广播事件，API 层通过 SSE（`GET /tasks/:id/events`）推给前端。所有长操作（下载/构建/部署/初始化/升级）都包装成 task，executor 内用 `Handle` 写日志和进度。
- `internal/store` — modernc.org/sqlite（纯 Go、无 CGO）。
- `internal/secret` — AES-256-GCM 凭据加密，密钥 `data/master.key`。
- `internal/templates` — go:embed 的远端配置模板：compose（business/denglin/nvidia 三种加速器变体）+ 完整监控栈（prometheus/grafana/filebeat/rabbitmq/mtail 等）+ `secrets/grafana-logs.htpasswd`（nginx /elasticsearch/ 反代的 Basic Auth 文件，缺失会导致远端 nginx 启动失败）。按中心 accelerator 选择模板。模板源头是 `D:\yl\center-cv-docker` 仓库（旧源 center-cv-deployer 已废弃）。

### 前端（web/）

Vue 3 + TypeScript + Vite + Element Plus，单页 `src/App.vue`，API 封装在 `src/api.ts`，类型在 `src/types.ts`。`npm run build` 会先跑 `vue-tsc --noEmit` 类型检查。构建产物必须同步到 `cmd/server/web_dist` 才会被 Go embed（`make web` 已包含此步骤；web_dist 内容被 .gitignore 忽略，仅有 `.keep`）。

## 关键约定

- 凭据（SSH 密码/私钥/私钥口令、Git token、OSS secret、飞书 Webhook）入库前必须经 `secret.Cipher` 加密，API 返回时带 `omitempty` 脱敏；`data/master.key` 与 `manager.db` 必须一起备份且不可入库。
- 修改 API 时注意 `/api/v1` 前缀、envelope 包装、`requireAuth` 保护范围；新增长耗时操作应走 `task.Manager` + SSE，而不是同步阻塞。
- 远端命令执行统一用 `runSSH` + `shellQuote`（单引号转义），新增 SSH 命务必沿用，避免注入。
- 任何对同一中心的状态变更（部署、容器操作、初始化、升级、模型发布）入口须先 `centerlock.Acquire(centerID)`，避免并发改坏远端状态。
- 数据目录默认 `./data`，源码缓存在 `data/sources`，两者均可通过 flag 覆盖。
- 默认只监听 `127.0.0.1`；局域网监听需显式 `--listen 0.0.0.0:8099`（compose 已配置）。
- 首次登录 admin/admin（bcrypt 存储，12 小时 Cookie Session），支持跳过强制改密。
