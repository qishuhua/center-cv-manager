# 中心数字化管理平台

模块化部署改造设计见 [中心模块化部署方案](docs/architecture/modular-center-deployment.md)；操作说明见下文“模块化中心部署”。

运行在开发机上的中心服务器集中管理服务。它用于登记各中心的 SSH 信息、查看并选择软件 Git
版本，在需要构建镜像时自动准备精确 commit 的源码，并把 Docker 构建上下文通过 SSH 隧道直接
流式发送给中心服务器的 Docker Engine。中心服务器不需要安装 Agent，也不会保存一份额外源码。

## 当前能力

- 登录后可通过左侧账号区域的“修改密码”更新密码，需验证当前密码并确认新密码；修改后其他登录会话失效
- 中心服务器增删改查，支持 SSH 端口、密码或加密私钥、Docker Socket 配置
- 全局飞书机器人 Webhook 加密配置；渲染模板时自动带入各中心名称
- SSH 页面直接建立连接；首次连接记录主机指纹，指纹变化时拒绝连接
- 按需检测 CPU、GPU、内存与磁盘并直接展示结果，不创建任务或任务日志
- 暗色 YAML Compose 编辑器；模板重生成和 Compose 重新部署集中在中心运行详情
- 软件 Git 仓库管理，实时查看远端分支/标签；发布时锁定 commit 并按需自动缓存源码
- 项目内置模型上传与版本管理，通过不可变 Manifest 发布到中心，支持多种模型格式、GPU 型号校验、断点续传、SHA-256 校验、原子激活及中心级模型版本固定/解绑

- 统一版本发布：直接选择远端分支/标签并锁定 commit，按中心检查精确镜像，缺失时自动准备源码、构建并部署
- 下载/发布任务异步执行，SQLite 持久化任务与日志，SSE 推送实时状态
- Vue 3 + Element Plus 管理界面；前端静态文件独立放入 Docker 镜像，由 Go 服务读取

新中心默认把部署文件和持久化数据放在 SSH 用户自己的 `~/.local/share/center-manager` 工作区，不需要向宿主机根目录写入。初始化会先创建并验证数据目录，再生成 Compose，避免 Docker 自动创建 root 所有目录。自定义目录不可写时，模型发布仍会自动验证 sudo 作为兼容回退。

## 模块化中心部署

新中心默认使用五个独立项目：`infra`、`logging`、`business`、`gateway`、`monitoring`。模板来自 `center-cv-docker` 分类目录，作为独立资源打包进 Docker 镜像，运行时从 `/app/templates` 读取。

- 初始化先准备并校验配置，按需同步基础镜像，然后按 **中间件 → 日志 → 业务 → 网关 → 监控** 启动。初始化服务必须成功退出，常驻服务必须运行且健康；业务额外检查应用就绪接口，监控最后启动。
- 缺少模型或业务镜像时，显示“等待业务发布”，保留已完成的基础模块。发布所需镜像和模型后自动继续，也可在运行详情中点击“初始化 / 继续失败步骤”。已成功且未变化的模块仅核对状态。
- 运行详情的“部署模块”支持独立编辑 Compose、`module.env` 和模块内配置文件，管理依赖、模型使用方、就绪 URL 和超时；支持添加自定义模块。每次命令只有一个 `-f`，并使用独立项目名。
- 保存 Compose 时先在目标机校验，再备份原文件并原子替换；文件 hash 防止并发覆盖，远端手工修改会提示。保存不会自动部署。可查看差异、载入 Compose 历史备份后重新保存和部署。
- “部署 / 重启 / 停止”可以选择整个模块或指定服务，执行前展示服务范围并绑定配置版本。配置变化后原计划拒绝执行，需要重新生成。失败任务可在配置不变时重试。
- 软件发布只修改原始 YAML 中匹配服务的镜像及版本标签，保留其他配置、变量和继承标签；模块中心使用服务级 `up -d --no-deps`。不使用 `--remove-orphans`，删除服务不会自动清理容器或卷。
- 模型升级只重启登记的模型使用服务。联合发布先构建所需镜像，再切换模型并部署软件；本次已经重建的 Worker 不重复重启。
- 基础镜像默认仅加载；选择立即应用时，只重建实际使用本次变化镜像的运行服务。
- 模块禁用不会停止现有容器；启用关系必须满足依赖。自定义服务或关闭部分模块时，请同步编辑监控采集目标和网关配置，避免访问未运行的上游。
- 未配置飞书时，新中心监控仍可启动，但默认不发送通知；之后需在监控模块更新 Alertmanager 配置和 Webhook 文件并部署。已有文件不会被初始化覆盖。

中心配置中的 `compose_file` 对新中心作为部署目录锚点保留兼容；实际入口分别位于 `deploy/<模块>/compose.yml`。模型、日志和媒体目录独立保存，不再跟随业务 Compose 路径变化。

### 旧中心迁移

已有中心默认保留原 Compose；新登记时发现指定路径已有 Compose 也按兼容模式导入，不自动拆分。可在运行详情点击“迁移到模块部署”，预检会展示模块、受影响容器和复用卷；实际迁移需要维护窗口并先排空业务任务。

迁移保留旧镜像版本、绝对挂载路径、未识别的自定义服务（归入 business），将旧命名卷设为 `external: true` 并复用实际卷名。只移除计划中旧项目的容器，不删除数据卷、不清理其他项目。跨模块依赖变为管理端启动依赖。计划与现场不一致或目标项目已被占用时停止执行。

失败后可重试原迁移任务；“回退迁移”恢复原 Compose，保留数据和模型现状，不回滚数据库内容或队列消息。原 Compose 被修改、或迁移后新增其他模块时，会要求先处理这些差异。仅配置解析通过不代表生产运行已验证，首次迁移应在测试中心验证。

### 模块 API

- `GET/POST /api/v1/centers/:id/modules`：列表 / 添加模块。
- `PUT/DELETE /api/v1/centers/:id/modules/:module`：修改设置 / 移除无容器且无依赖的登记（保留文件）。
- `GET/PUT /api/v1/centers/:id/modules/:module/compose?file=compose.yml`：读取 / 校验保存文件；`file=module.env` 编辑模块变量；PUT 必须提交读取时的 `revision`。
- `GET /api/v1/centers/:id/modules/:module/revisions`：Compose 历史备份文件名。
- `POST /api/v1/centers/:id/modules/:module/plan`：按 action 和 services 生成操作范围及配置版本。
- `POST /api/v1/centers/:id/modules/:module/actions`：提交 action、services、计划 revision 和管理员密码，异步执行。
- `POST /api/v1/centers/:id/migration/plan`：只读旧部署迁移预检。
- `POST /api/v1/centers/:id/migration`：提交计划 revision、管理员密码后迁移；`rollback: true` 回退迁移。

模块状态及路径作为中心的 `deployment_json` 存入 SQLite，旧记录默认为 null，升级数据库不触碰远端容器。任务与步骤输出沿用持久化任务日志和 SSE。

## 架构

```text
浏览器 -> 开发机 中心数字化管理平台
                   |-- Git ls-remote / clone -> data/sources
                   |-- SQLite + AES-GCM 凭据 -> data/manager.db + data/master.key
                   `-- SSH(密码/私钥) -> 中心服务器 /var/run/docker.sock
                                          |-- ~/.local/share/center-manager/deploy -> Compose 与配置
                                          |-- ~/.local/share/center-manager/data -> 模型、日志与业务数据
                                          `-- Docker Build API -> 中心镜像
```

源码只落在开发机的 `data/sources`。构建时 SDK 从该目录生成 tar 流，经 SSH 的 Unix Socket
转发直接写入 Docker API。系统没有开放任意远程 Shell 接口。

## 开发运行

需要 Go 1.26、Node.js、Git。中心服务器需要运行 Docker Engine，且所配置的 SSH 用户必须有
权限访问 Docker Socket。

```powershell
cd web
npm install
npm run build
cd ..

go run ./cmd/server --listen 127.0.0.1:8099 --data ./data
```

打开 `http://127.0.0.1:8099`。前后端分离开发时，后端保持上述端口，另一个终端执行：

```powershell
cd web
npm run dev
```

## Docker 运行（局域网）

直接启动：

```powershell
docker compose up -d --build
docker compose ps
```

服务发布在宿主机 `0.0.0.0:8099`，局域网通过 `http://<开发机IP>:8099` 访问。数据与源码
保存在 `center-manager-data` 具名卷；容器配置为 `unless-stopped` 自动重启。首次登录使用
`admin/admin`，系统会建议修改初始密码，也可以像 Grafana 一样暂时跳过。

## 生产构建

项目以 Docker Compose 作为默认构建和部署方式，不生成 EXE。后端编译为容器内的 Linux 程序，前端静态文件与部署模板分别位于 `/app/web`、`/app/templates`，不嵌入二进制。

```bash
docker compose build
docker compose up -d
docker compose logs -f --tail=100
```

修改代码、前端或模板后，执行 `docker compose up -d --build` 更新。也可使用 `make build` 构建镜像、`make up` 构建并启动、`make down` 停止服务；停止服务保留数据卷。

本地开发从项目根目录启动，默认读取 `web/dist` 和 `internal/templates`，可通过 `--web`、`--templates` 指定资源目录。部署只需 Docker Engine 与 Compose，无需宿主机安装 Go 或 Node.js。

登录密码使用 bcrypt 保存，浏览器使用 12 小时 Cookie Session。SSH 密码、私钥和私钥口令
使用 `data/master.key` 做 AES-256-GCM 加密后才写入 SQLite。`master.key` 与数据库必须一起
备份，且不可提交到版本库。

## API 概览

- `GET/POST/PUT/DELETE /api/v1/centers`：中心配置
- `POST /api/v1/centers/:id/detect`：同步读取中心硬件详情（不创建任务）
- `GET/POST/PUT/DELETE /api/v1/sources`：软件仓库
- `GET /api/v1/sources/:id/versions`：分支与标签
- `POST /api/v1/sources/:id/download`：兼容旧客户端的手动源码缓存接口；新版发布流程会按需自动准备
- `GET /api/v1/models`：读取 OSS 中已经通过 Manifest 固化的模型版本
- `POST /api/v1/model-uploads`：创建逻辑模型版本上传，返回各文件的临时签名 URL
- `PUT /api/v1/model-uploads/:id/file`：签名直传不可用时由管理端流式代理上传单个文件
- `POST /api/v1/model-uploads/:id/finalize`：校验文件大小和 SHA-256，固化文件并最后发布 Manifest
- `DELETE /api/v1/model-uploads/:id`：清理未完成上传的临时对象
- `POST /api/v1/model-releases/plan`：单中心详情使用的模型版本预检接口
- `POST /api/v1/model-releases`：单中心详情使用的模型切换接口
- `GET /api/v1/centers/:id/models`：读取中心实际生效的模型版本、完整性和固定状态
- `PUT /api/v1/centers/:id/models/pin`：固定或解绑中心当前整组模型版本快照
- `POST /api/v1/releases/plan`：按软件 commit、模型 Manifest 与中心生成联合只读发布计划
- `POST /api/v1/releases`：创建按中心隔离的统一版本发布任务；可以仅发布模型，也可以联合发布软件和模型
- `GET /api/v1/tasks/:id/events`：SSE 实时日志

## 模型版本 Manifest

当前 Manifest 是第一版，固定使用 `schema_version: 1`。顶层 `model` 和 `version` 标识模型版本，`formats` 可以同时容纳 ONNX、登临 engine 和一个或多个 NVIDIA plan。每种格式包含自己的 `platform`、可选 `hardware_profile`、`repository_path` 与 `artifacts`。

- 上传页面保留所选模型文件的原始文件名，不要求 Triton 的数字版本目录或 `model.onnx` 命名；只校验 ONNX、登临和 NVIDIA 文件分别使用 `.onnx`、`.engine` 和 `.plan` 扩展名。
- 浏览器优先通过两小时签名 URL 直传；对象存储未配置 CORS 时自动回退到管理端流式代理。
- 管理端逐文件流式计算 SHA-256，文件全部固化后最后写入 `manifest.json`；已经发布的模型名和版本不可覆盖。
- 模型仓库只负责上传与版本管理。发布时在“版本发布”中选择模型版本；不选择软件、只选择模型即为纯模型更新。每个中心优先使用匹配自身平台和 NVIDIA GPU 型号的原生格式，没有匹配格式时可回退 ONNX。
- 只发布软件时会沿用中心当前受管模型，但发布预检要求中心至少存在一组完整、健康的模型；新中心没有模型时必须同时选择模型版本。
- 默认宿主机模型目录为 `~/.local/share/center-manager/data/models`；容器内仍挂载为 `/data/models`，无需修改现有镜像。
- 对外模型目录只保留推理服务使用的扁平文件入口：`onnx/<model>.onnx`、`denglin/<model>.engine` 或 `plan/<hardware_profile>/<model>.plan`。版本文件存放在隐藏的 `.center-manager/repositories/` 内，扁平入口以相对符号链接指向当前版本，因此上传文件名仍不受限制。
- 模型发布和模型使用模块维护时会根据中心保存的模型发布状态校验并修复扁平文件入口；旧版遗留的 `denglin/<model>/<file>.engine` 等公开目录会迁移到隐藏版本库并从公开模型目录移除，不需要逐个重新上传模型。

## 中心目录布局

中心连接配置默认使用 `~/.local/share/center-manager/deploy/docker-compose.yml` 作为部署目录锚点；模块化中心在其同级模块目录中生成实际 Compose。首次初始化会通过 SSH 将 `~` 解析为该账号真实的家目录，并保存绝对路径；因此也兼容家目录不在 `/home/<用户名>` 下的账号。

登临环境按“每张物理 GPU 对应 4 个 DL 逻辑设备”换算硬件数量。例如 `dlsmi` 返回逻辑设备 `0`–`31` 时，管理端会生成 8 个 Worker，分别绑定 `0,1,2,3` 到 `28,29,30,31`，不会把 32 个逻辑设备误建成 32 个 Worker。

CPU 数量使用在线逻辑 CPU 数展示，但 Worker 的 `cpuset` 不再按 `0..数量-1` 直接切段。初始化会读取 `lscpu` 返回的真实 CPU ID、Socket、NUMA 和物理 Core，把同一物理 Core 的超线程兄弟放在同一个 Worker，并按 NUMA 节点分组；104 个逻辑 CPU、双路/双 NUMA、8 个 Worker 的典型结果为 `0-6,52-58` 至 `46-51,98-103`。若无法读取可靠拓扑，则不写 `cpuset`，交给 Linux 调度，避免绑定不存在或离线的 CPU ID。

```text
~/.local/share/center-manager/
├── deploy/                    # Compose、Nginx 与监控配置
└── data/
    ├── models/                # 模型仓库和发布状态
    ├── logs/                  # 服务日志
    └── media-resources/       # CV 与转码服务共享的媒体资源
```

可以在中心配置中改为其他绝对路径。已有 `/opt/center/deploy/docker-compose.yml` 配置保持旧的 `/data` 布局，不会自动搬迁或覆盖；需要迁移时，先备份旧数据，再修改 Compose 路径并执行强制重新生成。

兼容模式的初始化或“同步内置配置”发现已有 Compose 时，会保留 Compose 和中心现有的飞书密钥，同时更新受管的 Nginx、Prometheus、Grafana、告警及日志模板；“按硬件重新生成模板”才会备份并覆盖 Compose。配置文件同步后需重启 Compose 中的相关服务才能加载新配置。

登临和 NVIDIA 完整模板的 RabbitMQ 健康检查使用 15 秒超时、60 秒启动宽限期，检查间隔为 10 秒、失败重试次数为 12，避免 `rabbitmq-diagnostics -q ping` 启动较慢时误判不健康并阻塞业务启动。已有中心仅“同步内置配置”不会更新 Compose：请在“编辑 Compose”中修改 `rabbitmq.healthcheck.timeout` 为 `15s`、`start_period` 为 `60s`，保存后在维护窗口执行“重新部署 Compose”，使 RabbitMQ 按新参数重建并启动依赖服务。重建会短暂中断消息连接，持久化数据卷保留；仅重启容器不会加载新的健康检查参数。完成后确认 RabbitMQ 健康、初始化成功、业务就绪及告警恢复。

旧中心在迁移前继续管理原单 Compose；模块化中心分别管理各模块入口。发布前校验临时文件，启动失败时保留现场和错误；不再通过 `--remove-orphans` 自动清理服务，移除配置后需单独处理旧容器。

## 下一阶段建议

当前版本已闭环“源码版本 -> 下载 -> 精确镜像检查 -> 按需构建 -> 中心部署”。后续适合依次增加：
发布回滚、中心环境/容器状态巡检、批量灰度发布、中心标签和权限分组、操作审计。
