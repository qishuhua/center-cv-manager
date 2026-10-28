package templates

import (
	"strings"
	"testing"
)

func TestTestAlarmModuleEnvAndCompose(t *testing.T) {
	webhook := "https://open.feishu.cn/open-apis/bot/v2/hook/test-token"
	base := "# Module-specific Compose variables\nCV_SAVE_DEBUG_IMAGE=1\n"
	on := ApplyTestAlarmModuleEnv(base, true, webhook)
	if !strings.Contains(on, "TEST_ALARM_ENABLED=true\nFEISHU_WEBHOOK_URL="+webhook) {
		t.Fatal("开启后应写入转码服务的两个变量")
	}
	if again := ApplyTestAlarmModuleEnv(on, true, webhook); again != on {
		t.Fatal("重复同步应保持不变")
	}
	off := ApplyTestAlarmModuleEnv(on, false, webhook)
	if strings.Contains(off, "TEST_ALARM_ENABLED=") || strings.Contains(off, "FEISHU_WEBHOOK_URL=") || !strings.Contains(off, "CV_SAVE_DEBUG_IMAGE=1") {
		t.Fatal("关闭后应移除测试变量并保留其他配置")
	}
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err := RenderModules(platform, RenderOptions{Workers: 1, TestAlarmEnabled: true, TestAlarmWebhook: webhook})
		if err != nil {
			t.Fatal(err)
		}
		compose := files["business/compose.yml"]
		if !strings.Contains(compose, "TEST_ALARM_ENABLED: ${TEST_ALARM_ENABLED:-false}") || !strings.Contains(compose, "FEISHU_WEBHOOK_URL: ${FEISHU_WEBHOOK_URL:-}") {
			t.Fatalf("%s 模板缺少转码服务测试变量", platform)
		}
		if !strings.Contains(files["business/module.env"], "FEISHU_WEBHOOK_URL="+webhook) {
			t.Fatalf("%s 未渲染机器人地址", platform)
		}
	}
	legacy := "services:\n  video-transcode-service:\n    image: video-transcode-service:latest\n    environment:\n      HTTP_PORT: \"8092\"\n"
	updated := EnsureTranscodeTestAlarmEnv(legacy)
	if !strings.Contains(updated, "TEST_ALARM_ENABLED: ${TEST_ALARM_ENABLED:-false}") || !strings.Contains(updated, "FEISHU_WEBHOOK_URL: ${FEISHU_WEBHOOK_URL:-}") {
		t.Fatal("存量 Compose 应补齐环境变量")
	}
	if EnsureTranscodeTestAlarmEnv(updated) != updated {
		t.Fatal("存量 Compose 重复补齐应保持不变")
	}
}
