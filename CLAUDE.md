# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

Center CV Manager：运行在开发机上的中心服务器集中管理服务。Go (Gin) 后端 + Vue 3 (Element Plus) 前端。核心链路：登记中心服务器 SSH 信息 → 在开发机本地缓存 Git 源码（`data/sources`）并本地构建镜像推送到镜像仓库 → 发布时经 SSH 隧道让中心从仓库按 digest 拉取镜像、通过 SSH Unix Socket 转发直连远端 Docker Engine API 完成部署。远端不装 Agent、不落源码副本、不执行构建。

生产形态为 compose 三容器（不生成 EXE）：`center-cv-manager-web`（nginx：SPA 静态 + `/api` 反代到 `center-cv-manager-api:8099`，入口 `0.0.0.0:8099`）+ `center-cv-manager-api`（Go API，非 root 用户，挂载宿主 `/var/run/docker.sock` 供本地构建；模板运行时从文件系统读取，不再 go:embed）+ `center-cv-registry` sidecar（`network_mode: service:center-cv-manager-api`，统一经 `127.0.0.1:5000` 访问，OSS S3 兼容层存储、本地无状态）。单 Dockerfile 双 target（`web`/`api`），go-builder 只 `COPY cmd/ internal/`——前端改动不触发 Go 重编，反之亦然；前端发布只重建 web 容器，不打断后端运行中的任务。nginx 反代四个必须维持的配置（`web/nginx.conf`）：WebSocket 升级头（SSH 终端）、SSE 不缓冲（任务事件流）、`client_max_body_size 0`（模型上传代理回退）、24h 读写超时（长会话）。`make dev` 直跑时后端仍直接服务 `web/dist`（目录缺失则仅提供 API）。

## 常用命令

```powershell
# 默认构建/部署方式：Docker Compose
make build            # docker compose build
make up               # docker compose up -d --build（局域网 0.0.0.0:8099）
make down             # 停止（保留数据卷）；make logs 看日志；make clean 连带删镜像

# 仅构建前端（产物 web/dist，不入库；本地 go run 默认读取该目录）
make web

# 本地开发运行后端（http://127.0.0.1:8099；需先有 web/dist）
make dev              # go run ./cmd/server --listen 127.0.0.1:8099 --data ./data
# 资源目录可用 flag 覆盖：--web ./web/dist --templates ./internal/templates

# 测试（Go 全部 + 前端 vue-tsc 类型检查及构建）
make test             # go test ./... && cd web && npm ci && npm run build

# 运行单个包/单个测试
go test ./internal/provision/...
go test ./internal/build/ -run TestXxx

# 可选 SSH 只读集成测试（未设置环境变量时自动跳过）
# 设置 COMPOSE_TEST_HOST / COMPOSE_TEST_USER / COMPOSE_TEST_PASSWORD / COMPOSE_TEST_FILE 后：
go test ./internal/provision/ -run TestLiveCompose

# 前后端分离开发：后端跑 8099，另开终端
cd web && npm run dev   # Vite 5174，/api 代理到 127.0.0.1:8099
```

环境要求：Go 1.26、Node.js、Git；容器方式部署只需 Docker Engine 与 Compose。

## 架构

### 启动装配（cmd/server/main.go）

main.go 是唯一装配点：打开 SQLite（`data/manager.db`）→ 加载/创建 `data/master.key`（`secret.LoadOrCreate`）→ `templates.SetDirectory` 定位模板目录（校验 compose/nginx/monitoring/secrets/modules 五个子目录）→ 构造各 service 并注入 `api.New(...)`。前端以 `os.DirFS(webDir)` 传入，`api/server.go` 的 NoRoute 兜底返回 SPA 页面。

### 分层结构

- `internal/api` — Gin 路由与 handler，统一 envelope 响应（`{code, message, data}`）。除 `/health`、`/auth/*` 外全部走 `requireAuth()` Cookie Session 中间件；所有变更类路由（发布、初始化、迁移、模块操作、凭据、用户管理 `/users`、任务停止/重试/删除、SSH 终端 WebSocket）再套 `requireAdmin()`，查看用户（viewer）只能访问数据 GET、`detect` 与 `/settings/status`。其中用户管理写操作（新建/删除/改角色/重置密码）与任务记录删除（`DELETE /tasks/:id`）再套 `requireSuperAdmin()`，仅内建 `admin` 账号可执行，不随管理权限下放。路由注册集中在 `server.go` 的 `Router()`；`terminal.go` 提供 WebSocket SSH 终端（`GET /centers/:id/terminal`）。统一发布（`/releases`）的编排逻辑也在这一层。
- `internal/auth` — 多用户登录与 Session 管理：`data/auth.json` 存用户数组（bcrypt 哈希、role=admin/viewer、每人独立首登改密标记，旧单用户格式自动迁移），12 小时 Cookie、连续失败按 IP 锁定 5 分钟。Session 绑定用户名且每次请求重新解析用户：删号立即踢下线、角色变更即时生效；改密/重置只吊销该用户的其他会话。用户 CRUD 保护：内建 `admin` 是超级管理员（加载时强制 role=admin，任何人不允许删除或降级）、不能删除当前登录账号自己。
- `internal/centerlock` — 进程内按中心 ID 的互斥锁（`Acquire`/`AcquireContext`/`Within`），串行化对同一中心的状态变更操作，不同中心互不阻塞。deploy、centeraction、baseimage、provision、modelasset 均使用。
- `internal/alerting` — 全局飞书机器人 Webhook 凭据（加密存储），provision 等流程渲染通知时经它取凭据并自动带入中心名称；`SendText` 供管理端直发文本消息（统一发布在 `api/release_notify.go` 的开始/结束通知中使用，尽力而为、失败不影响发布）。
- `internal/domain` — 跨包共享的模型（Center、Source、Task、Checkout、`Deployment`/`ComposeModule`、凭据等），service 间通过 `Repository` / `CenterResolver` 等小接口解耦。
- `internal/centerpath` — 中心侧目录布局：默认工作区 `~/.local/share/center-manager`（首次初始化把 `~` 解析为真实家目录后存绝对路径）；旧 `/opt/center` 布局沿用 `/data` 路径不迁移。`ShellQuote` 只展开可信路径的前导 `~/`，不计算任意环境变量。
- `internal/modular` — 模块化部署的共享边界：`Deployment`（五个默认模块 infra/logging/business/gateway/monitoring 的定义、依赖、InitServices、ReadyURLs）、`Command()` 构造每个模块的 Compose 命令（单一 `-f`、显式 `-p` 项目名、`--env-file module.env`，并 unset 环境中的 `COMPOSE_PROJECT_NAME` 等隐式覆盖，固定 `COMPOSE_REMOVE_ORPHANS=false`）、`Parse`/`Inspect`/`Wait` 就绪检查、`PlanRevision` 配置指纹。
- 各业务包（均以 service 为入口）：
  - `center` — 中心服务器 CRUD、SSH/Docker 连通性测试、host key 指纹管理；其他包通过它解析 Center 配置。
  - `source` — Git 仓库登记、ls-remote 查版本；发布选择锁定 commit，仅在需要构建时自动准备 checkout 到本地 `sources/` 目录，并校验分支漂移。
  - `build` — 本地镜像构建与推送（`BuildLocal`/`PushRef`，`LocalDocker` 经 FromEnv/挂载 sock 获得宿主引擎；双平台变体检测 `DetectBuildVariants`）；平台探测 `DetectCenterAccelerator` 经临时 SSH 连接读取中心 GPU 运行时。产物打 source/commit/accelerator 标签。
  - `deploy` — 部署计划（Check）与执行（Run）：比对远端镜像（要求镜像带匹配 source/commit 的构建标签，防止同名旧镜像蒙混过关），只修改原始 YAML 中匹配服务的镜像及版本标签（不回写 `compose config` 展开结果）。重启范围只限本次发布涉及的服务：legacy 中心 `up -d --force-recreate --no-deps` 只重建映射到的服务（中间件、网关、监控不动——`--force-recreate` 会连带重建依赖，必须配 `--no-deps`）；模块化中心只跑有映射服务的模块、整模块 `up -d --force-recreate --no-deps`。`Run` 返回实际重建的服务名，供发布后核验收窄范围。
  - `provision` — 中心初始化与模块管理：`PrepareModuleFiles`（只写配置不启容器）、按 中间件 → 日志 → 业务 → 网关 → 监控 顺序启动、`ResumeModules`（缺模型/业务镜像时停在等待状态，由初始化任务续跑剩余模块）、模块 Compose/module.env 读取保存（revision 并发控制 + 目标机备份历史 + 保存前捕获已发布基线）、Compose 整体发布与发布状态（`compose_publish.go`，指纹记录在目标机 `.center-manager/published/`）、legacy 同目录多 Compose 文件识别（`compose_files.go`）与迁移（`migration.go`，只读预检 + 执行 + 回退）。
  - `centeraction` — 容器/compose 的启停、重启、清理等运维动作。
  - `baseimage` — 阿里云 OSS 基础镜像包目录与中心升级（签名 URL 下载、ETag 缓存判断）。
  - `modelasset` — 模型版本管理：OSS 上传（签名 URL 直传 + 流式代理回退、逐文件 SHA-256）、不可变 Manifest 发布（`schema_version: 1`，按 ONNX/登临/NVIDIA 多格式）与中心工作区 `data/models` 增量同步（签名 URL、ETag、断点续传、扁平符号链接入口）。
  - `registry` — 共享镜像仓库（registry:3 + OSS S3 兼容后端）：接入地址存 `registry_settings` 表（清空即关闭），`StorageConfig` 把 OSS 凭据渲染成数据卷内 `registry/config.yml`（AK 需七项 oss 权限、`forcepathstyle: false`，registry:2 与 OSS 不兼容勿降级）；`ListVersions` 经 catalog/tags/config blob（307 到 OSS）聚合版本与平台标签、按 digest 缓存。发布时分发经 `sshx.RemoteForward` 反向隧道让中心按 digest 拉取并 retag 成与远端构建同名的本地标签——层流量由 registry 307 到 OSS 签名 URL 直下，不经过管理端。仓库内标签用 `build.ReleaseRegistryTag`（含平台后缀，防 nvidia/denglin 同 tag 互相覆盖）。
- `internal/sshx` — SSH 连接与 `DockerClient`：核心是把 `http.Transport` 的 DialContext 换成 `sshClient.Dial("unix", socket)`，从而让 Docker SDK 走 SSH 隧道访问远端 docker.sock；`RequestRemoteForward` 基于既有连接申请远端回环端口转发（镜像分发隧道）。改动 Docker/SSH 相关逻辑先看这里。
- `internal/task` — 异步任务管理器：`Submit(kind, params, executor)` 持久化任务、内存广播事件，API 层通过 SSE（`GET /tasks/:id/events`）推给前端。所有长操作（下载/构建/部署/初始化/升级/迁移/模块操作）都包装成 task，executor 内用 `Handle` 写日志和进度。
- `internal/store` — modernc.org/sqlite（纯 Go、无 CGO）。
- `internal/secret` — AES-256-GCM 凭据加密，密钥 `data/master.key`。
- `internal/templates` — 模板目录从文件系统读取（非 go:embed）：`compose/`（legacy 单文件变体）与 `modules/`（模块化五目录）+ nginx/monitoring/secrets。`Render` 渲染 legacy 模板，`RenderModules` 按平台（nvidia/denglin）渲染模块文件集，动态生成 Worker、Prometheus 采集目标、宿主机挂载路径；渲染出的 Compose 都要过 `validate.go` 的运行时假设校验（host 网络禁 ports、RABBITMQ_NODENAME、`service_healthy` 依赖必须有健康检查）。模板源头是 `C:\Users\henry.qi\Documents\yl\center-cv-docker` 仓库（模块化目录 business/infra/gateway/logging/monitoring 一一对应 `modules/`；根目录三份 docker-compose 与 gateway/nginx 对应 legacy `compose/`、`nginx/`；同步时保留管理端适配：image.repository 标签、`./monitoring|./nginx|./secrets` 挂载路径、`RABBITMQ_NODENAME`、`CADVISOR_HEALTHCHECK_URL`；上游的 chute-verification-service 服务定义不进入管理端 Compose 模板——不强制部署，监控侧引用（就绪探活/告警正则/filebeat 输入）保留；legacy business 变体与 `feishu-webhook-url.example` 是管理端自有版本不跟随上游）。

### 模块化部署模型

- 中心两类形态由 `center.Deployment`（SQLite `deployment_json`）区分：`nil` 为 legacy 兼容模式（继续管理原单 Compose，含同目录多 Compose 文件识别 `compose_files.go`）；非 nil 为模块化（五个独立 Compose 项目 `center-cv-<模块>`，入口 `deploy/<模块>/compose.yml` + `module.env`）。升级数据库不会触碰远端容器。
- 初始化：初始化服务（rabbitmq-init、elasticsearch-init 等）必须成功退出，常驻服务须 healthy，业务额外检查应用 readyz。缺模型或业务镜像时停在"等待业务发布"（`waiting_business`），保留已完成模块；发布业务后用"初始化 / 继续失败步骤"（`POST /centers/:id/initialize` → `Initialize` + `ResumeModules`）续跑网关和监控，失败步骤可手动重试。
- Compose 操作铁律：每次命令只有一个 `-f` 和独立项目名；不用 `--remove-orphans`（删除服务不会自动清理容器或卷）；Compose 一律整体发布——`up -d`（配置变化加 `--force-recreate`），内容未变的重启用 `restart --no-deps` 并跳过 init 服务，API 层直接拒绝按服务清单操作。
- 配置编辑：保存前先在目标机 `docker compose config` 校验，再备份原文件并原子替换；文件内容 hash 作 revision 防并发覆盖，远端手工修改会被检测；历史备份保存在目标机，不会自动部署。
- 迁移：`POST /centers/:id/migration/plan` 只读预检（模块、受影响容器、复用卷）；`POST /centers/:id/migration` 执行（保留旧镜像版本、绝对挂载路径，旧命名卷 `external: true` 复用实际卷名）；`rollback: true` 回退。仅精确移除旧项目容器，不删数据卷。
- 模块操作 API（`/centers/:id/modules/...`）：`plan` 生成操作范围与配置版本，`actions` 提交时绑定计划 revision（权限由 requireAdmin 角色把关，不再二次验证密码），异步执行；配置变化后原计划拒绝执行。`publish-plan`/`publish` 是整体发布入口（模块化模块与 legacy Compose 通用）：绑定发布计划 revision，内容未变需 `confirm_unchanged` 才整体重启；执行前先把发布状态标为失败，全部服务就绪且 `config --hash` 与运行容器一致后才记为已发布。
- 发布后处理（`api.finishModuleInitialization` → `provision.RecordBusinessPublication`）：核验业务 Compose 已整体应用（`config --hash` 与运行容器逐一匹配）后才写入发布状态；不匹配则报错并保留未发布状态。软件发布传入本次实际重建的服务名，只核验这些服务（其他服务不因发布重启也不参与核验）；模型发布与整栈重建路径不传参，核验业务模块/全部服务。

### 前端（web/）

Vue 3 + TypeScript + Vite + Element Plus。UI 对标 Grafana 暗色风格：`index.html` 挂 `class="dark"`，`main.ts` 引入 `element-plus/theme-chalk/dark/css-vars.css`，Grafana 配色与 Element 变量覆盖集中在 `style.css` 顶部的 `html.dark` 块（画布 `#111217`、面板 `#181B1F`、主色橙 `#FF780A`、链接蓝 `#33A2E5`、成功绿 `#73BF69`、告警黄 `#FF9830`、错误红 `#E02F44`；主按钮橙底深色文字）。新增样式应引用这些变量或同系颜色，不要引入浅色背景。入口 `src/App.vue`，主要组件：`ModuleManager.vue`（模块/Compose 列表与整体发布状态、Compose/module.env 编辑、部署/重启/停止、迁移）、`ModelCatalog.vue`/`ModelUploadDialog.vue`（模型上传与发布）、`TerminalPage.vue`（xterm WebSocket SSH 终端）、`UserManagement.vue`（管理员用户管理）、`YamlEditor.vue`（CodeMirror 暗色 YAML）。API 封装在 `src/api.ts`，类型在 `src/types.ts`。`npm run build` 会先跑 `vue-tsc --noEmit` 类型检查。

## 关键约定

- 凭据（SSH 密码/私钥/私钥口令、Git token、OSS secret、飞书 Webhook）入库前必须经 `secret.Cipher` 加密，API 返回时带 `omitempty` 脱敏；`data/master.key` 与 `manager.db` 必须一起备份且不可入库。
- 修改 API 时注意 `/api/v1` 前缀、envelope 包装、`requireAuth` 保护范围；新增长耗时操作应走 `task.Manager` + SSE，而不是同步阻塞。
- 远端命令执行统一用 `runSSH` + `shellQuote`（单引号转义，provision 包内）或 `centerpath.ShellQuote`（可信路径含 `~/` 前缀），新增 SSH 命务必沿用，避免注入。
- 任何对同一中心的状态变更（部署、容器操作、初始化、升级、模型发布、模块操作、迁移）入口须先 `centerlock.Acquire(centerID)`，避免并发改坏远端状态。
- 禁止用 `--remove-orphans` 或 `down -v` 实现迁移或清理；清理旧容器必须按 Compose project 标签精确匹配。
- 数据目录默认 `./data`，源码缓存在 `data/sources`，两者均可通过 flag 覆盖。
- 默认只监听 `127.0.0.1`；局域网监听需显式 `--listen 0.0.0.0:8099`（compose 已配置）。
- 首次登录 admin/admin（bcrypt 存储，12 小时 Cookie Session），支持跳过强制改密。仅管理员可授予权限、发布软件/模型、执行重启等变更操作；普通用户只读，新增路由时按 `requireAdmin()` 分组归类。`auth.json` 必须与 `master.key` 一起备份。

- 配置更新只走 SSH/编辑器修改后重新发布，或重置后重新初始化。已登记中心初始化不更新内置模板；配置文件指纹包括模块目录及只读配置挂载，排除运行数据与发布记录。重置只删除受管 Compose 容器，并要求重新选择软件与模型。
- 软件发布硬依赖镜像仓库，源码不再发送到中心构建（中心远端构建路径已删除，`build` 包只保留本地构建/推送与平台探测）。两段式：**构建发布**在代码仓库页（`POST /sources/:id/publish`）——本地构建全部平台变体（Dockerfile.nvidia/Dockerfile.denglin 都成功才推送；只有通用 Dockerfile 时一次构建打 nvidia/denglin/cpu 三标签）；**发布**只查仓库已有版本——中心详情页（`GET /centers/:id/software/registry-versions` 按显卡平台过滤、`POST /centers/:id/software/release`）与统一发布页（`GET /registry/versions` 全量清单）都从镜像版本选择，`prepareDeploymentItems` 校验版本已入库、`checkUnifiedRelease` 逐中心校验平台标签存在，发布编排只按 digest 分发不构建。中心侧本地镜像名保持 `ReleaseImageTag`（不带平台后缀）——分发时 pull-by-digest 后 retag，`deploy.Check` 的标签校验口径不变；共享仓库内的引用必须用带平台后缀的 `ReleaseRegistryTag`。
