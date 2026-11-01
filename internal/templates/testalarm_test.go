package templates

import (
	"strings"
	"testing"
)

func TestTestAlarmModuleEnvAndCompose(t *testing.T) {
	webhook := "https://open.feishu.cn/open-apis/bot/v2/hook/test-token"
	player := "http://10.0.0.8:8080/player.html"
	base := "# Module-specific Compose variables\nCV_SAVE_DEBUG_IMAGE=1\n"
	on := ApplyTestAlarmModuleEnv(base, true, webhook, player)
	if !strings.Contains(on, "TEST_ALARM_ENABLED=true\nFEISHU_WEBHOOK_URL="+webhook+"\nFEISHU_PLAYER_URL="+player) {
		t.Fatal("开启后应写入转码服务的三个变量")
	}
	if again := ApplyTestAlarmModuleEnv(on, true, webhook, player); again != on {
		t.Fatal("重复同步应保持不变")
	}
	off := ApplyTestAlarmModuleEnv(on, false, webhook, player)
	if strings.Contains(off, "TEST_ALARM_ENABLED=") || strings.Contains(off, "FEISHU_WEBHOOK_URL=") || strings.Contains(off, "FEISHU_PLAYER_URL=") || !strings.Contains(off, "CV_SAVE_DEBUG_IMAGE=1") {
		t.Fatal("关闭后应移除测试变量并保留其他配置")
	}
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err := RenderModules(platform, RenderOptions{Workers: 1, TestAlarmEnabled: true, TestAlarmWebhook: webhook, TestAlarmPlayerURL: player})
		if err != nil {
			t.Fatal(err)
		}
		compose := files["business/compose.yml"]
		if !strings.Contains(compose, "TEST_ALARM_ENABLED: ${TEST_ALARM_ENABLED:-false}") || !strings.Contains(compose, "FEISHU_WEBHOOK_URL: ${FEISHU_WEBHOOK_URL:-}") || !strings.Contains(compose, "FEISHU_PLAYER_URL: ${FEISHU_PLAYER_URL:-}") {
			t.Fatalf("%s 模板缺少转码服务测试变量", platform)
		}
		if !strings.Contains(files["business/module.env"], "FEISHU_WEBHOOK_URL="+webhook) || !strings.Contains(files["business/module.env"], "FEISHU_PLAYER_URL="+player) {
			t.Fatalf("%s 未渲染机器人地址或播放器入口", platform)
		}
		// 网关模板自带播放器静态页与挂载。
		if _, ok := files["gateway/nginx/player.html"]; !ok {
			t.Fatalf("%s 网关未渲染 player.html", platform)
		}
		if !strings.Contains(files["gateway/compose.yml"], GatewayPlayerMount) {
			t.Fatalf("%s 网关 Compose 缺少 player.html 挂载", platform)
		}
		if !strings.Contains(files["gateway/nginx/nginx.conf"], "location = /player.html") {
			t.Fatalf("%s 网关 nginx.conf 缺少播放器 location", platform)
		}
	}
	legacy := "services:\n  video-transcode-service:\n    image: video-transcode-service:latest\n    environment:\n      HTTP_PORT: \"8092\"\n"
	updated := EnsureTranscodeTestAlarmEnv(legacy)
	if !strings.Contains(updated, "TEST_ALARM_ENABLED: ${TEST_ALARM_ENABLED:-false}") || !strings.Contains(updated, "FEISHU_WEBHOOK_URL: ${FEISHU_WEBHOOK_URL:-}") || !strings.Contains(updated, "FEISHU_PLAYER_URL: ${FEISHU_PLAYER_URL:-}") {
		t.Fatal("存量 Compose 应补齐环境变量")
	}
	if EnsureTranscodeTestAlarmEnv(updated) != updated {
		t.Fatal("存量 Compose 重复补齐应保持不变")
	}
}

func TestEnsureGatewayPlayerIdempotent(t *testing.T) {
	rendered, err := RenderModules("denglin", RenderOptions{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	compose, nginxConf := rendered["gateway/compose.yml"], rendered["gateway/nginx/nginx.conf"]

	// 存量中心（无挂载行、无 location）补齐后应与最新模板渲染产物逐字一致。
	oldCompose := strings.Replace(compose, "      "+GatewayPlayerMount+"\n", "", 1)
	oldNginx := strings.Replace(nginxConf, GatewayPlayerLocation+"\n", "", 1)
	if got := EnsureGatewayPlayerMount(oldCompose); got != compose {
		t.Fatalf("Compose 补挂载应与模板一致:\n% s", got)
	}
	if got := EnsureGatewayPlayerLocation(oldNginx); got != nginxConf {
		t.Fatalf("nginx.conf 补 location 应与模板一致:\n% s", got)
	}
	// 已包含时原样返回（幂等），缺少锚点时不改动。
	if EnsureGatewayPlayerMount(compose) != compose || EnsureGatewayPlayerLocation(nginxConf) != nginxConf {
		t.Fatal("已包含挂载与 location 时应保持不变")
	}
	if EnsureGatewayPlayerMount("services: {}\n") != "services: {}\n" || EnsureGatewayPlayerLocation("# no fallback\n") != "# no fallback\n" {
		t.Fatal("缺少锚点时应原样返回")
	}
}
