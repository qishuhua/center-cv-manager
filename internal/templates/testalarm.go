package templates

import "strings"

const testAlarmEnvMarker = "# 飞书告警测试：由中心开关维护，修改请回中心编辑"

// ApplyTestAlarmModuleEnv 托管转码服务的测试告警开关与机器人地址。
// 关闭时保留中心记录中的地址，但不向容器传递地址。
func ApplyTestAlarmModuleEnv(env string, enabled bool, webhook string) string {
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		key := envKeyOf(line)
		if key == "TEST_ALARM_ENABLED" || key == "FEISHU_WEBHOOK_URL" || strings.TrimSpace(line) == testAlarmEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if enabled {
		block := testAlarmEnvMarker + "\nTEST_ALARM_ENABLED=true\nFEISHU_WEBHOOK_URL=" + webhook
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
	})
}
