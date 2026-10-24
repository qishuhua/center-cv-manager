# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

Center CV Manager：运行在开发机上的中心服务器集中管理服务。Go (Gin) 后端 + Vue 3 (Element Plus) 前端。核心链路：登记中心服务器 SSH 信息 → 在开发机本地缓存 Git 源码（`data/sources`）并本地构建镜像推送到镜像仓库 → 发布时经 SSH 隧道让中心从仓库按 digest 拉取镜像、通过 SSH Unix Socket 转发直连远端 Docker Engine API 完成部署。远端不装 Agent、不落源码副本、不执行构建。

生产形态为 compose 七容器（不生成 EXE）：`center-cv-manager-web`（nginx：SPA 静态 + `/api` 反代到 `center-cv-manager-api:8099`，入口 `0.0.0.0:8099`）+ `center-cv-manager-api`（Go API，非 root 用户，挂载宿主 `/var/run/docker.sock` 供本地构建——sock 常为 root:root 0660，容器经 `group_add: ["0"]` 取得组权限；模板运行时从文件系统读取，不再 go:embed）+ `center-cv-registry` sidecar（`network_mode: service:center-cv-manager-api`，统一经 `127.0.0.1:5000` 访问，OSS S3 兼容层存储、本地无状态）+ 开发机统一监控四容器（前身独立仓库 center-cv-monitor：`prometheus`/`alertmanager`/`grafana`/`dashboard-generator`，静态配置在仓库 `monitoring/` 目录只读挂载，Grafana 入口 `0.0.0.0:3000`，Prometheus/Alertmanager 仅回环；指标与日志保存在中心本地，开发机只拉每中心一条健康信号、发失联告警）。监控相关卷全部 external（不随 compose 自动创建、`down -v` 不可能误删；卷清单与 `docker volume create` 命令维护在根 README「Docker 运行」一节，用户先建卷再启动 compose，**新增卷必须同步加进那份清单**，否则 `docker compose up` 报 external volume not found）。单 Dockerfile 双 target（`web`/`api`），go-builder 只 `COPY cmd/ internal/`——前端改动不触发 Go 重编，反之亦然；前端发布只重建 web 容器，不打断后端运行中的任务。nginx 反代四个必须维持的配置（`web/nginx.conf`）：WebSocket 升级头（SSH 终端）、SSE 不缓冲（任务事件流）、`client_max_body_size 0`（模型上传代理回退）、24h 读写超时（长会话）。`make dev` 直跑时后端仍直接服务 `web/dist`（目录缺失则仅提供 API）。

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

main.go 是唯一装配点：打开 SQLite（`data/manager.db`）→ 加载/创建 `data/master.key`（`secret.LoadOrCreate`）→ `templates.SetDirectory` 定位模板目录（校验 compose/monitoring/secrets/modules 四个子目录）→ 构造各 service 并注入 `api.New(...)`。前端以 `os.DirFS(webDir)` 传入，`api/server.go` 的 NoRoute 兜底返回 SPA 页面。

### 分层结构

- `internal/api` — Gin 路由与 handler，统一 envelope 响应（`{code, message, data}`）。除 `/health`、`/auth/*` 外全部走 `requireAuth()` Cookie Session 中间件；所有变更类路由（发布、初始化、迁移、模块操作、凭据、用户管理 `/users`、任务停止/删除、SSH 终端 WebSocket）再套 `requireAdmin()`，查看用户（viewer）只能访问数据 GET、`detect` 与 `/settings/status`。其中用户管理写操作（新建/删除/改角色/重置密码）与任务记录删除（`DELETE /tasks/:id`）再套 `requireSuperAdmin()`，仅内建 `admin` 账号可执行，不随管理权限下放。任务不提供重试：失败任务参数快照可能已过时，一律回对应入口重新提交，运行中任务可停止。路由注册集中在 `server.go` 的 `Router()`；`terminal.go` 提供 WebSocket SSH 终端（`GET /centers/:id/terminal`）。统一发布（`/releases`）的编排逻辑也在这一层。
- `internal/auth` — 多用户登录与 Session 管理：`data/auth.json` 存用户数组（bcrypt 哈希、role=admin/viewer、每人独立首登改密标记，旧单用户格式自动迁移），12 小时 Cookie、连续失败按 IP 锁定 5 分钟。Session 绑定用户名且每次请求重新解析用户：删号立即踢下线、角色变更即时生效；改密/重置只吊销该用户的其他会话。用户 CRUD 保护：内建 `admin` 是超级管理员（加载时强制 role=admin，任何人不允许删除或降级）、不能删除当前登录账号自己。
- `internal/centerlock` — 进程内按中心 ID 的互斥锁（`Acquire`/`AcquireContext`/`Within`），串行化对同一中心的状态变更操作，不同中心互不阻塞。deploy、centeraction、baseimage、provision、modelasset 均使用。
- `internal/alerting` — 全局飞书机器人 Webhook 凭据（加密存储），provision 等流程渲染通知时经它取凭据并自动带入中心名称；`SendText` 供管理端直发文本消息（统一发布、模型发布与中心软件切换在 `api/release_notify.go` 的开始/结束通知中使用；中心初始化与暂停/恢复经 `api/center_notify.go` 发开始/结束消息（初始化完成附带实际发布的业务镜像与模型版本清单），重置不发；均尽力而为、失败不影响任务；构建发布不发通知）。中心「告警控制」（`test_mode`）开启后，该中心的全部管理端通知静默：单中心操作经 `notificationSender`（返回 nil sender），批量发布把开启告警控制的中心从通知名单与兜底轮询中剔除；中心侧监控告警不受影响。
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
  - `registry` — 共享镜像仓库（registry:3 + OSS S3 兼容后端）：接入地址存 `registry_settings` 表（清空即关闭），存储侧零凭据——`SaveStorageConfig` 渲染 config.yml 时 OSS 凭据、Bucket 与 Endpoint 全部取自模型仓库的全局 OSS 配置（`oss_credentials`），镜像仓库只贡献共享 Bucket 内的存储前缀（默认 `registry/`，与 center-images/、center-models/ 并列）；`ListVersions` 经 catalog/tags/config blob（307 到 OSS）聚合版本与平台标签、按 digest 缓存。发布时分发经 `sshx.RemoteForward` 反向隧道让中心按 digest 拉取并 retag 成与远端构建同名的本地标签，校验 RepoDigest 后摘除 digest 拉取引用（中心镜像不挂多引用）——层流量由 registry 307 到 OSS 签名 URL 直下，不经过管理端。中心业务镜像删除（`DELETE /centers/:id/software/images`）按镜像 ID force 执行：分发残留或多版本同 ID 会让镜像挂多引用，非 force 会被 daemon 拒绝；前置已校验来源标签与容器占用。仓库内标签 = 版本主体 + 平台后缀（`build.ReleaseRegistryTag`，防 nvidia/denglin 同 tag 互相覆盖）。
  - `monitor` — 开发机统一监控集成（前身独立仓库 center-cv-monitor，静态配置在顶层 `monitoring/` 由 compose 只读挂载，是本项目正式配置——模板改动由生成器签名比对自动生效，无外部拷贝约定）：从中心表渲染 prometheus file_sd 清单（`data/monitor/targets/centers.json`，compose 挂专用卷）与两个 secret 文件（飞书 Webhook 复用 `alerting` 全局凭据解密落盘、日志查询密码与中心网关模板 `grafana-logs.htpasswd` 固定对应，测试防漂移）。`Sync` 在启动时渲染一次，此后由 `center.Service` 的 `SetMonitorSync` 变更钩子覆盖中心记录全部写路径（保存/删除/硬件检测/运行时更新/重置——含 provision/deploy 内部的 UpdateRuntime/UpdateHardware），无定时轮询；告警凭据保存在 API 层显式触发。prometheus file_sd（1 分钟）与 dashboard-generator（15 秒）轮询消费无需通知，唯一例外是 Webhook 变更——alertmanager 的 `url_file` 仅配置加载时读取，API 经本地 Docker 引擎对 `center-cv-alertmanager` 容器发 SIGHUP 重载。`Status` 查开发机 Prometheus 汇总各中心健康信号（`GET /monitoring/status`，viewer 可读，监控栈不可达时降级 `stack_online=false` 不报错）。拉取目标由 `center.Host + center.ConsolePort` 推导（`centerTarget`，与运行详情中心 Grafana 入口同址——中心网关在同一入口暴露 /prometheus/ 与 /elasticsearch/，路径由生成器拼接），**不设独立的监控地址字段，完成硬件检测的 nvidia/denglin 中心零配置自动纳入**；渲染时 site=中心 ID 前 8 位短 slug（`siteSlug`，Grafana 数据源/仪表盘 uid 有 40 字符上限，完整 uuid 放不下；同批冲突跳过），gpu_vendor=检测平台；**告警控制中心完全不写入清单——不探测、无仪表盘，关闭告警控制后自动恢复**（与发布通知静默同口径）。
- `internal/sshx` — SSH 连接与 `DockerClient`：核心是把 `http.Transport` 的 DialContext 换成 `sshClient.Dial("unix", socket)`，从而让 Docker SDK 走 SSH 隧道访问远端 docker.sock；`RequestRemoteForward` 基于既有连接申请远端回环端口转发（镜像分发隧道）。改动 Docker/SSH 相关逻辑先看这里。
- `internal/task` — 异步任务管理器：`Submit(kind, params, executor)` 持久化任务、内存广播事件，API 层通过 SSE（`GET /tasks/:id/events`）推给前端。所有长操作（下载/构建/部署/初始化/升级/迁移/模块操作）都包装成 task，executor 内用 `Handle` 写日志和进度。
- `internal/store` — modernc.org/sqlite（纯 Go、无 CGO）。
- `internal/secret` — AES-256-GCM 凭据加密，密钥 `data/master.key`。
- `internal/templates` — 模板目录从文件系统读取（非 go:embed）：`compose/`（legacy 单文件变体）与 `modules/`（模块化五目录）+ monitoring/secrets。**nginx 配置只有一份源文件** `modules/gateway/nginx/nginx.conf`（含任务入口 create_task 反代与 chute 的 /fall-events/ 反代），legacy 渲染经 FileSpec 落地为 `nginx/nginx.conf` 供 compose 相对挂载，不再有 nginx/nginx.conf 与 nginx.full.conf 两个变体。`Render` 渲染 legacy 模板，`RenderModules` 按平台（nvidia/denglin）渲染模块文件集，动态生成 Worker、Prometheus 采集目标、宿主机挂载路径；渲染出的 Compose 都要过 `validate.go` 的运行时假设校验（host 网络禁 ports、RABBITMQ_NODENAME、`service_healthy` 依赖必须有健康检查）。模板源头是 `C:\Users\henry.qi\Documents\yl\center-cv-docker` 仓库（模块化目录 business/infra/gateway/logging/monitoring 一一对应 `modules/`；根目录三份 docker-compose 对应 legacy `compose/`；同步时保留管理端适配：image.repository 标签、`./monitoring|./nginx|./secrets` 挂载路径、`RABBITMQ_NODENAME`、`CADVISOR_HEALTHCHECK_URL`；chute-verification-service（错分审核）**始终部署**在 business 模板——共享鉴权用插值默认兜底（`templates.ChuteServiceBlock` 与两份模板保持一致），实际取值只写 business module.env（`ApplyMisSortModuleEnv` 托管 CV_WMS_URL/CV_MIS_AUTH/CV_CAMERA_POLL_ENABLED，关闭时移除并回落轮询默认 0，清理旧 CV_ORGAN_CODE）；ingress 恢复配置轮询、任务发布与 chute 设备列表同步，`EnsureIngressPollEnv` 补齐存量环境；chute 只保留共享鉴权，不读取 WMS/机构编码/轮询变量；alarm-service 使用 WMS 地址与共享鉴权上报结果，`EnsureAlarmWMSEnv` 补齐存量环境，软件名登记为 `chute-verification-service` 走构建发布链路；business 模块另含 `center-alarm-service`（转码上报拆分产物：消费 `cv.alarm.report`、素材直传 OSS + 业务告警回调，8093 回环、与转码共享 media 挂载、日志文件无扩展名需 filebeat/mtail 显式条目，软件名 `center-alarm-service` 走构建发布链路，转码块的 `CV_ENV_IS_PRO` 已随上游拆分删除）；legacy business 变体与 `feishu-webhook-url.example` 是管理端自有版本不跟随上游）。

### 模块化部署模型

- 中心两类形态由 `center.Deployment`（SQLite `deployment_json`）区分：`nil` 为 legacy 兼容模式（继续管理原单 Compose，含同目录多 Compose 文件识别 `compose_files.go`）；非 nil 为模块化（五个独立 Compose 项目 `center-cv-<模块>`，入口 `deploy/<模块>/compose.yml` + `module.env`）。升级数据库不会触碰远端容器。
- 初始化：初始化服务（rabbitmq-init、elasticsearch-init 等）必须成功退出，常驻服务须 healthy，业务额外检查应用 readyz。**模型为可选**：初始化对话框勾选「部署模型」才随初始化发布模型；不勾选时业务镜像照常分发部署，中心停在"等待业务发布"（waiting_business，保留已完成模块，视为初始化成功），之后在版本发布页发布模型（纯模型发布会在模型就绪后续跑剩余模块），或再经初始化入口继续。软件发布"中心已有健康模型"的防呆仅对普通发布生效，无模型初始化显式豁免。
- Compose 操作铁律：每次命令只有一个 `-f` 和独立项目名；不用 `--remove-orphans`（删除服务不会自动清理容器或卷）；Compose 一律整体发布——`up -d`（配置变化加 `--force-recreate`），内容未变的重启用 `restart --no-deps` 并跳过 init 服务，API 层直接拒绝按服务清单操作。
- 配置编辑：保存前先在目标机 `docker compose config` 校验，再备份原文件并原子替换；文件内容 hash 作 revision 防并发覆盖，远端手工修改会被检测；历史备份保存在目标机，不会自动部署。
- 迁移：`POST /centers/:id/migration/plan` 只读预检（模块、受影响容器、复用卷）；`POST /centers/:id/migration` 执行（保留旧镜像版本、绝对挂载路径，旧命名卷 `external: true` 复用实际卷名）；`rollback: true` 回退。仅精确移除旧项目容器，不删数据卷。
- 模块操作 API（`/centers/:id/modules/...`）：`plan` 生成操作范围与配置版本，`actions` 提交时绑定计划 revision（权限由 requireAdmin 角色把关，不再二次验证密码），异步执行；配置变化后原计划拒绝执行。`actions` 支持 deploy/restart/stop（模块管理页「Compose 配置列表」按模块状态暴露为 启动/重启/暂停，供单模块调试——stop 是 `compose stop` 容器保留，与整体暂停中心的 `down` 移除容器不同；停止不硬性拦截依赖方——网关只是反代业务、停业务仅致其外部访问 502，属可接受调试场景，`plan` 返回运行中依赖方清单（`Dependents`）由前端确认框提示后果；启动会校验依赖就绪）。**网关独立于业务**：`NewDeployment` 不再设置 gateway→business 依赖（网关只是反代，不构成业务就绪前置；启动顺序仍为列表顺序 中间件 → 日志 → 业务 → 网关 → 监控，只是网关不等待业务），legacy→模块化迁移计划（`provision.PlanMigration`）同口径摘除，存量中心由 store 一次性迁移 `gateway_independent_v1` 清理 deployment_json 中的该依赖。整模块启停成功后中心级状态自动对齐：ready 下全部启用模块 stopped 转 paused，paused 下全部启用模块 healthy 转 ready（`applyModule` 的 `wholeModule` 判定，部分运行的中间状态不改动）。`publish-plan`/`publish` 是整体发布入口（模块化模块与 legacy Compose 通用）：绑定发布计划 revision，内容未变需 `confirm_unchanged` 才整体重启；执行前先把发布状态标为失败，全部服务就绪且 `config --hash` 与运行容器一致后才记为已发布。
- 发布后处理（`api.finishModuleInitialization` → `provision.RecordBusinessPublication`）：核验业务 Compose 已整体应用（`config --hash` 与运行容器逐一匹配）后才写入发布状态；不匹配则报错并保留未发布状态。软件发布传入本次实际重建的服务名，只核验这些服务（其他服务不因发布重启也不参与核验）；模型发布与整栈重建路径不传参，核验业务模块/全部服务。
- 错分检测（WMS）：chute-verification-service（错分审核）**始终部署**，中心 `wms_url`/开关只控制 chute 落格核验的 WMS 取值同步——启用判定 = `wms_url` 非空且 `mis_sort_enabled` 为 true；**关闭开关时 `wms_url`/`mis_auth` 原值保留在中心记录（可随时重新打开恢复），远端 module.env 的托管变量随之移除回落 Compose 插值默认值**（`mis_auth` 留空回填公开约定默认值 `20260606123a`，legacy 中心不支持；机构编码不配置，chute 用 Compose 插值默认值 `637101`）。未开启时落格核验使用 Compose 插值默认值（WMS `http://10.21.61.48:31995` 等）。chute 只监听回环 8091，局域网查询经网关 `:8080/fall-events/` 反代（nginx 模板内置；保存中心时 `ApplyBusinessSettings` 顺带幂等补齐存量中心的 nginx——`EnsureFallEventsProxy`，锚点带回退、CRLF 归一，并把 gateway 模块置待发布；同步对老模板中心全兼容：缺转码锚点追加 chute 块，插入块缩进按目标文件实际的服务键缩进/步长自适应（手写或编辑器重排过、非模板 2 空格的 Compose 也对齐插入，幂等判定经 YAML 查 services 段而非头行精确匹配），`TestMisSortSyncOnLegacyCompose` 与 `TestApplyChuteServiceNonStandardIndent` 锁定该行为）。**错分任务不再由 ingress 摄像机轮询创建**：`CV_CAMERA_POLL_*` 插值行已从 ingress 环境移除、`EnsureIngressPollEnv` 删除，存量 module.env 的轮询开关按托管变量清理；chute 读 `CV_WMS_URL` 基地址、代码内拼 API 路径，取值只写 business module.env。初始化时随模板渲染；已初始化中心改字段由 `provision.ApplyBusinessSettings`（保存中心时自动触发，双 tmp 成对校验 + 备份 + 原子替换）更新远端文件并置 ConfigChanged，等 business 整体发布生效。软件发布预检（`deploy.requireLocalImages`）要求未被本次发布覆盖的 `pull_policy: never` 镜像必须已在中心——chute 镜像需先在软件仓库构建发布。
- Debug 模式与存图：中心「Debug 模式」开关（`debug_log_enabled`，编辑页，位于告警控制下方）开启后 business module.env 托管 `LOG_LEVEL=debug`（`templates.ApplyLogLevelModuleEnv`），业务 Worker（center-cv-service，源码读 `LOG_LEVEL` 环境变量）日志切到 debug；关闭移除托管值回落 Compose Worker 公共环境锚点插值默认 info。「存图」开关（`save_image_enabled`，紧随其下）同一套路托管 `CV_SAVE_DEBUG_IMAGE=1`（`templates.ApplySaveImageModuleEnv`，源码读该环境变量、默认 0 不存图；置 1 时每轮推理保存调试图片——原始帧与带框结果帧），关闭回落插值默认 0。两开关的插值行均两平台模板内置、存量中心由 `EnsureWorkerLogLevelEnv`/`EnsureWorkerSaveImageEnv` 幂等补齐只增不删（共用 `ensureWorkerEnvEntry`）。与错分检测共用保存中心的同步入口 `provision.ApplyBusinessSettings`（原 `ApplyBusinessWMS`，同一中心锁内一次成对校验写回），置 ConfigChanged 等 business 整体发布生效；仅模块化中心支持，legacy 中心保存时直接报错。

### 前端（web/）

Vue 3 + TypeScript + Vite + Element Plus。UI 对标 Grafana 暗色风格：`index.html` 挂 `class="dark"`，`main.ts` 引入 `element-plus/theme-chalk/dark/css-vars.css`，Grafana 配色与 Element 变量覆盖集中在 `style.css` 顶部的 `html.dark` 块（画布 `#111217`、面板 `#181B1F`、主色橙 `#FF780A`、链接蓝 `#33A2E5`、成功绿 `#73BF69`、告警黄 `#FF9830`、错误红 `#E02F44`；主按钮橙底深色文字）。新增样式应引用这些变量或同系颜色，不要引入浅色背景。入口 `src/App.vue`，主要组件：`ModuleManager.vue`（模块/Compose 列表与整体发布状态、Compose/module.env 编辑、部署/重启/停止、迁移）、`ModelCatalog.vue`/`ModelUploadDialog.vue`（模型上传与发布）、`TerminalPage.vue`（xterm WebSocket SSH 终端）、`UserManagement.vue`（管理员用户管理）、`YamlEditor.vue`（CodeMirror 暗色 YAML）；「统一监控」页在 `App.vue` 内实现（各中心健康信号卡片 + Grafana 跳转，地址取 `window.location.hostname:3000`）。API 封装在 `src/api.ts`，类型在 `src/types.ts`。`npm run build` 会先跑 `vue-tsc --noEmit` 类型检查。

## 关键约定

- 凭据（SSH 密码/私钥/私钥口令、Git token、OSS secret、飞书 Webhook）入库前必须经 `secret.Cipher` 加密，API 返回时带 `omitempty` 脱敏；`data/master.key` 与 `manager.db` 必须一起备份且不可入库。
- 修改 API 时注意 `/api/v1` 前缀、envelope 包装、`requireAuth` 保护范围；新增长耗时操作应走 `task.Manager` + SSE，而不是同步阻塞。
- 远端命令执行统一用 `runSSH` + `shellQuote`（单引号转义，provision 包内）或 `centerpath.ShellQuote`（可信路径含 `~/` 前缀），新增 SSH 命务必沿用，避免注入。
- 任何对同一中心的状态变更（部署、容器操作、初始化、升级、模型发布、模块操作、迁移）入口须先 `centerlock.Acquire(centerID)`，避免并发改坏远端状态。
- 禁止用 `--remove-orphans` 或 `down -v` 实现迁移或清理；清理旧容器必须按 Compose project 标签精确匹配。
- 数据目录默认 `./data`，源码缓存在 `data/sources`，两者均可通过 flag 覆盖；统一监控动态配置渲染目录默认 `./data/monitor`（`--monitor` 覆盖，compose 挂专用卷）。
- 默认只监听 `127.0.0.1`；局域网监听需显式 `--listen 0.0.0.0:8099`（compose 已配置）。
- 首次登录 admin/admin（bcrypt 存储，12 小时 Cookie Session），支持跳过强制改密。仅管理员可授予权限、发布软件/模型、执行重启等变更操作；普通用户只读，新增路由时按 `requireAdmin()` 分组归类。`auth.json` 必须与 `master.key` 一起备份。

- 配置更新只走 SSH/编辑器修改后重新发布，或重置后重新初始化。已登记中心初始化不更新内置模板；配置文件指纹包括模块目录及只读配置挂载，排除运行数据与发布记录。重置只删除受管 Compose 容器，并要求重新选择软件与模型。
- 软件发布硬依赖镜像仓库，源码不再发送到中心构建（中心远端构建路径已删除，`build` 包只保留本地构建/推送与平台探测）。对中心只有**镜像版本**与**模型版本**两个概念：Git ref/commit 只出现在软件仓库页（构建入口），中心详情页、统一发布页、初始化对话框、任务目标与飞书通知一律以镜像版本主体（`build.ReleaseVersionBody`，`ref-commit12`）展示。两段式：**构建发布**在代码仓库页（`POST /sources/:id/publish`）——本地构建全部平台变体（Dockerfile.nvidia/Dockerfile.denglin 各自成变体、都成功才推送；只有通用 Dockerfile 时一次构建、打**不带平台后缀的通用标签**，全部平台含 CPU 可用）；**发布**只查仓库已有版本——中心详情页（`GET /centers/:id/software/registry-versions` 按显卡平台过滤；软件与模型版本同口径多选累计，底部发布条把两者合成**一个**统一发布任务，`POST /releases` 单中心提交；单软件即时切换端点 `POST /centers/:id/software/release` 保留作 API 用途、UI 不再走它）、统一发布页（`GET /registry/versions` 全量清单）与初始化对话框都从镜像版本选择，`prepareDeploymentItems` 校验版本已入库、`checkUnifiedRelease` 逐中心校验，取 digest 一律"平台后缀标签优先、通用标签兜底"（`manifestDigestForPlatform`），发布编排只分发不构建。中心侧本地镜像名保持 `ReleaseImageTag`（不带平台后缀）——分发时 pull-by-digest 后 retag，`deploy.Check` 的标签校验口径不变；分平台变体的仓库内引用必须用带平台后缀的 `ReleaseRegistryTag`。
