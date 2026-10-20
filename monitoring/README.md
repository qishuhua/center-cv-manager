# 开发机统一监控

本目录是开发机统一监控栈的**静态配置**，由 compose 只读挂载给四个监控容器
（prometheus / alertmanager / grafana / dashboard-generator），前身是独立仓库
`center-cv-monitor`。指标和日志继续保存在各中心本地，开发机只做健康信号拉取、
失联告警与查询代理，不复制数据。本目录是正式配置：仪表盘模板、规则、飞书
通知模板都直接在这里修改（生成器签名比对模板内容，改动最迟 15 秒自动生效；
其余文件改动后 `docker compose up -d` 重建对应容器生效）。

## 首次部署：创建卷

本项目所有数据卷都是 external（不随 compose 自动创建、`down -v` 不可能误删）。
**创建命令与卷清单维护在根 README「Docker 运行」一节**，首次部署先执行一次
（幂等，已存在的卷不受影响）。新增卷时把 `docker volume create` 加进那份清单，
再启动 compose。

- 渲染卷（targets / secrets / grafana-dashboards）：内容可随时由 API 与生成器
  重建，删除后重启管理端即可恢复。
- 历史卷（prometheus / grafana / alertmanager）：指标曲线与 Grafana 配置，
  删除后仪表盘与数据源自动重建、仅损失历史曲线。

## 动静分离

- **本目录（静态）**：Prometheus 主配置与规则、Alertmanager 配置与飞书模板、
  Grafana provisioning、仪表盘模板、生成脚本。修改后 `docker compose up -d`
  重建对应容器生效。
- **API 渲染（动态）**：管理端 Go 后端（`internal/monitor`）把下列文件渲染到
  `center-monitor-targets` / `center-monitor-secrets` 两个卷，监控容器只读挂载：
  - `targets/centers.json` — 从中心表渲染；中心记录任何变更（登记、编辑、
    删除、硬件检测、初始化、重置、测试模式开关）后由 center.Service 的
    变更钩子自动更新。`site` 用中心 ID 前 8 位短标识（Grafana uid 40 字符
    上限），`gpu_vendor` 取检测平台（仅 nvidia/denglin 纳入监控），
    **测试模式中心完全不写入清单**（不探测、无仪表盘，关闭后自动恢复）。
  - `secrets/feishu-webhook-url` — 取自「飞书告警」全局凭据（解密落盘）；变更后
    API 自动向 alertmanager 容器发 SIGHUP 重载。
  - `secrets/center-logs-password` — 与中心网关 `grafana-logs.htpasswd` 对应的
    固定约定值（Grafana 查询中心 Elasticsearch 日志的 Basic Auth）。

  Prometheus file_sd 每分钟自动重读 targets；dashboard-generator 每 15 秒轮询，
  自动为新增中心创建数据源与仪表盘——新加中心不再需要任何手工配置。

## 中心侧要求

拉取目标直接由中心登记的 **SSH 主机 + 访问端口** 推导——与运行详情里的中心
Grafana 入口同一个地址，无需为监控单独登记任何配置。中心网关 nginx 在该入口
暴露只读 `/prometheus/` 与 Basic Auth 的 `/elasticsearch/`，这两条由本项目
初始化中心时自动部署（`internal/templates` 的 gateway 模板）。完成硬件检测的
nvidia/denglin 中心自动纳入；`auto` 未检测的中心在硬件检测后自动进入监控。

## 访问

- Grafana：`http://<开发机>:3000`（账号默认 admin，见 compose 的
  `GRAFANA_ADMIN_PASSWORD`；「各中心监控」目录含每中心仪表盘与总览）。
- Prometheus / Alertmanager 仅绑定开发机回环（9090 / 9093），供 API 状态查询。

历史指标与 Grafana 数据在 `center-monitor-prometheus` / `center-monitor-grafana`
卷中（external，见上方「首次部署」）。
