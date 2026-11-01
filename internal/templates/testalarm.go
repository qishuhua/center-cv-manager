package templates

import "strings"

const testAlarmEnvMarker = "# 飞书告警测试：由中心开关维护，修改请回中心编辑"

// ApplyTestAlarmModuleEnv 托管转码服务的测试告警开关、机器人地址与回放播放器
// 入口。关闭时保留中心记录中的地址，但不向容器传递任何取值。
func ApplyTestAlarmModuleEnv(env string, enabled bool, webhook, playerURL string) string {
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		key := envKeyOf(line)
		if key == "TEST_ALARM_ENABLED" || key == "FEISHU_WEBHOOK_URL" || key == "FEISHU_PLAYER_URL" || strings.TrimSpace(line) == testAlarmEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if enabled {
		block := testAlarmEnvMarker + "\nTEST_ALARM_ENABLED=true\nFEISHU_WEBHOOK_URL=" + webhook + "\nFEISHU_PLAYER_URL=" + playerURL
		if result == "" {
			result = block
		} else {
			result += "\n\n" + block
		}
	}
	if result == "" {
		result = "# Module-specific Compose variables"
	}
	return result + "\n"
}

// EnsureTranscodeTestAlarmEnv 补齐存量业务 Compose 的转码服务环境变量。
func EnsureTranscodeTestAlarmEnv(compose string) string {
	return ensureServiceEnv(compose, "video-transcode-service", []string{
		"TEST_ALARM_ENABLED: ${TEST_ALARM_ENABLED:-false}",
		"FEISHU_WEBHOOK_URL: ${FEISHU_WEBHOOK_URL:-}",
		"FEISHU_PLAYER_URL: ${FEISHU_PLAYER_URL:-}",
	})
}

// GatewayPlayerMount 是网关 Compose 的 player.html 只读挂载行，
// 与模板渲染产物保持逐字一致（EnsureGatewayPlayerMount 的判重依据）。
const GatewayPlayerMount = "- ./nginx/player.html:/etc/nginx/static/player.html:ro"

// GatewayPlayerLocation 是网关 nginx.conf 的播放器静态 location 块。
const GatewayPlayerLocation = `        # 告警视频回放播放器（纯静态单文件）：飞书告警消息里的 FEISHU_PLAYER_URL
        # 指向该入口，URL 参数携带 OSS 视频/封面/SVG 地址。
        location = /player.html {
            default_type text/html;
            root /etc/nginx/static;
        }
`

// EnsureGatewayPlayerMount 只增不删地为存量网关 Compose 补 player.html 挂载行：
// 挂载在 nginx.conf 挂载行之后、grafana-logs.htpasswd 挂载行之前插入，与模板
// 顺序一致；找不到锚点时原样返回（compose 校验与整体发布指纹会兜底暴露漂移）。
func EnsureGatewayPlayerMount(compose string) string {
	if strings.Contains(compose, GatewayPlayerMount) {
		return compose
	}
	anchor := "      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro\n"
	if !strings.Contains(compose, anchor) {
		return compose
	}
	return strings.Replace(compose, anchor, anchor+"      "+GatewayPlayerMount+"\n", 1)
}

// EnsureGatewayPlayerLocation 只增不删地为存量网关 nginx.conf 补 /player.html
// 静态 location：插在兜底 `location /` 之前；找不到兜底块时原样返回。
func EnsureGatewayPlayerLocation(nginxConf string) string {
	if strings.Contains(nginxConf, "location = /player.html") {
		return nginxConf
	}
	anchor := "        location / {"
	if !strings.Contains(nginxConf, anchor) {
		return nginxConf
	}
	return strings.Replace(nginxConf, anchor, GatewayPlayerLocation+"\n"+anchor, 1)
}
