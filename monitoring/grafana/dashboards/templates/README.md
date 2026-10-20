# 仪表盘模板

统一监控各中心的 Grafana 仪表盘模板，是本项目的正式配置：

- `center-cv-overview.nvidia.json`
- `center-cv-overview.denglin.json`

`dashboard-generator` 只读取本目录中的模板，将各中心的最终面板生成到相邻的
`generated` 目录（compose 卷 center-monitor-grafana-dashboards），签名比对包含
模板文件内容——在本目录修改模板后，运行中的生成器最迟 15 秒内自动重新生成
全部面板，无需重启容器或手工执行脚本。
