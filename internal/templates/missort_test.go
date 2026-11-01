package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderModulesMisSortArchitecture(t *testing.T) {
	base := RenderOptions{Workers: 2, CPUSets: []string{"2,4", "6,8"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心"}

	// 未开启错分检测：ingress 带默认关闭的轮询与计划发布插值，
	// Worker 锚点带订阅插值，module.env 为注释桩。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env := files["business/compose.yml"], files["business/module.env"]
	for _, want := range []string{
		"CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}",
		"CV_PLANNED_ZMQ_BIND: ${CV_PLANNED_ZMQ_BIND:-tcp://127.0.0.1:8091}",
		"CV_PLANNED_ZMQ_ENDPOINTS: ${CV_PLANNED_ZMQ_ENDPOINTS:-tcp://127.0.0.1:8091}",
		"CV_EVENT_TIME_UTC_OFFSET_MINUTES: ${CV_EVENT_TIME_UTC_OFFSET_MINUTES:-480}",
		"CV_PRO_POST_API: ${CV_PRO_POST_API:-https://jmsgw.jtexpress.com.cn/businessplan/aiVideoAnalysis/v2/algorithmCallBack}",
		"CV_UAT_POST_API: ${CV_UAT_POST_API:-https://uat-jmsgw.jtexpress.com.cn/businessplan/aiVideoAnalysis/v2/algorithmCallBack}",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("渲染结果缺少错分/告警插值行 %s", want)
		}
	}
	if strings.Contains(env, "CV_WMS_URL=") || !strings.HasPrefix(env, "# Module-specific Compose variables") {
		t.Fatal("未开启错分检测时 module.env 应为注释桩")
	}

	// 开启错分检测：compose 形态不变（ingress 控制开关），module.env 写入
	// WMS、共享鉴权、轮询开关与轮询周期（未配置时回填默认 1 小时；机构
	// 编码不配置）。
	base.WMSURL, base.MisAuth, base.MisSortEnabled = "http://10.21.61.48:31995", "20260606123a", true
	files, err = RenderModules("denglin", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env = files["business/compose.yml"], files["business/module.env"]
	for _, line := range []string{
		"CV_WMS_URL=http://10.21.61.48:31995",
		"CV_MIS_AUTH=20260606123a",
		"CV_CAMERA_POLL_ENABLED=1",
		"CV_CAMERA_POLL_INTERVAL_SECONDS=3600",
	} {
		if !strings.Contains(env, line) {
			t.Fatalf("module.env 缺少 %s", line)
		}
	}
	if strings.Contains(env, "CV_ORGAN_CODE=") {
		t.Fatal("机构编码不再配置，module.env 不应写入")
	}

	// 自定义轮询周期：module.env 写入显式取值。
	base.MisSortIntervalSeconds = 120
	files, err = RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	if env = files["business/module.env"]; !strings.Contains(env, "CV_CAMERA_POLL_INTERVAL_SECONDS=120") {
		t.Fatal("module.env 应写入自定义轮询周期 120")
	}
	base.MisSortIntervalSeconds = 0

	// 配置保留但开关关闭：module.env 同样回落注释桩（wms_url 留存于中心
	// 记录，重新打开开关即可恢复）。
	base.MisSortEnabled = false
	files, err = RenderModules("denglin", base)
	if err != nil {
		t.Fatal(err)
	}
	if env = files["business/module.env"]; strings.Contains(env, "CV_WMS_URL=") {
		t.Fatal("开关关闭时 module.env 不应写入错分托管变量")
	}
}

func TestApplyMisSortModuleEnv(t *testing.T) {
	opts := MisSortOptions{Enabled: true, WMSURL: "http://h:1", MisAuth: "a"}
	fromStub := ApplyMisSortModuleEnv("# Module-specific Compose variables\n", opts)
	if !strings.Contains(fromStub, "CV_CAMERA_POLL_INTERVAL_SECONDS=3600") {
		t.Fatal("轮询周期未配置时应回填默认 3600")
	}
	if fromStub != ApplyMisSortModuleEnv(fromStub, opts) {
		t.Fatal("重复应用应幂等")
	}
	// 自定义周期替换旧值（含模板时代写入的 300），始终只保留一行。
	retuned := ApplyMisSortModuleEnv(fromStub, MisSortOptions{Enabled: true, WMSURL: "http://h:1", MisAuth: "a", IntervalSeconds: 120})
	if strings.Count(retuned, "CV_CAMERA_POLL_INTERVAL_SECONDS=") != 1 || !strings.Contains(retuned, "CV_CAMERA_POLL_INTERVAL_SECONDS=120") {
		t.Fatal("自定义轮询周期应替换旧值")
	}
	// 旧版本写入的 CV_ORGAN_CODE 存量行应被清理，让服务回落插值默认值。
	foreign := "RABBITMQ_REALTIME_PRIORITY=7\nCV_ORGAN_CODE=637101\n" + fromStub
	merged := ApplyMisSortModuleEnv(foreign, opts)
	if !strings.Contains(merged, "RABBITMQ_REALTIME_PRIORITY=7") || merged != ApplyMisSortModuleEnv(merged, opts) {
		t.Fatal("外部变量必须保留且结果幂等")
	}
	if strings.Contains(merged, "CV_ORGAN_CODE=") {
		t.Fatal("存量 CV_ORGAN_CODE 行应被移除")
	}
	disabled := ApplyMisSortModuleEnv(merged, MisSortOptions{})
	if strings.Contains(disabled, "CV_WMS_URL=") || strings.Contains(disabled, "CV_MIS_AUTH=") || strings.Contains(disabled, "CV_CAMERA_POLL_INTERVAL_SECONDS=") || strings.Contains(disabled, "CV_ORGAN_CODE=") || !strings.Contains(disabled, "RABBITMQ_REALTIME_PRIORITY=7") {
		t.Fatal("关闭后应移除托管变量并保留外部变量")
	}
}


func assertValidYAML(t *testing.T, compose string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		t.Fatalf("结果应是合法 YAML: %v\n%s", err, compose)
	}
	services, _ := document["services"].(map[string]any)
	return services
}

// TestMisSortSyncEnsures 验证错分同步的插值行补齐：渲染模板已自洽（各
// ensure 为空操作）、远端被手工删改或缺少插值行的 Compose 补齐后幂等、
// 已有自定义取值只增不删、缺失服务原样跳过。
func TestMisSortSyncEnsures(t *testing.T) {
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err := RenderModules(platform, RenderOptions{Workers: 1, CPUSets: []string{"2,4"}})
		if err != nil {
			t.Fatal(err)
		}
		compose := files["business/compose.yml"]
		services := assertValidYAML(t, compose)
		ingress := services["center-cv-ingress"].(map[string]any)["environment"].(map[string]any)
		if ingress["CV_WMS_URL"] == nil || ingress["CV_MIS_AUTH"] == nil {
			t.Fatal("center-cv-ingress missing WMS settings")
		}
		if EnsureIngressPollEnv(compose) != compose || EnsureWorkerZMQEnv(compose) != compose || EnsureTranscodeAlarmEnv(compose) != compose {
			t.Fatal("templates must already contain all entries")
		}
	}
	// 远端手工精简过的 Compose：同步把缺失的插值行补齐，已有自定义值保留。
	stale := "services:\n    center-cv-ingress:\n        image: custom\n        environment:\n            CV_WMS_URL: http://custom\n    video-transcode-service:\n        image: custom\n        environment:\n            HTTP_PORT: \"8092\"\n"
	sync := func(s string) string {
		s = EnsureIngressPollEnv(s)
		s = EnsureWorkerZMQEnv(s)
		return EnsureTranscodeAlarmEnv(s)
	}
	patched := sync(stale)
	if sync(patched) != patched {
		t.Fatal("sync must be idempotent")
	}
	services := assertValidYAML(t, patched)
	ingress := services["center-cv-ingress"].(map[string]any)["environment"].(map[string]any)
	if ingress["CV_WMS_URL"] != "http://custom" {
		t.Fatal("custom WMS must be preserved")
	}
	if ingress["CV_PLANNED_ZMQ_BIND"] == nil || ingress["CV_CAMERA_POLL_ENABLED"] == nil {
		t.Fatal("planned publisher entries must be added")
	}
	transcode := services["video-transcode-service"].(map[string]any)["environment"].(map[string]any)
	if transcode["CV_PRO_POST_API"] == nil || transcode["CV_UAT_POST_API"] == nil ||
		transcode["ALARM_PRO_OSS_UPLOAD_URL_API"] == nil || transcode["ALARM_UAT_OSS_UPLOAD_URL_API"] == nil {
		t.Fatal("transcode alarm endpoints must be added")
	}
	if transcode["HTTP_PORT"] != "8092" {
		t.Fatal("transcode custom entries must be preserved")
	}
	missing := "services:\n  other:\n    image: x\n"
	if sync(missing) != missing {
		t.Fatal("missing services must be skipped")
	}
	enabled := ApplyMisSortModuleEnv("CUSTOM=keep\nCV_CAMERA_POLL_ENABLED=0\nCV_CAMERA_POLL_INTERVAL_SECONDS=300\n", MisSortOptions{Enabled: true, WMSURL: "http://wms", MisAuth: "auth"})
	if strings.Count(enabled, "CV_CAMERA_POLL_ENABLED=") != 1 || !strings.Contains(enabled, "CV_CAMERA_POLL_ENABLED=1") {
		t.Fatal("enable must replace stale switch")
	}
	if strings.Count(enabled, "CV_CAMERA_POLL_INTERVAL_SECONDS=") != 1 || !strings.Contains(enabled, "CV_CAMERA_POLL_INTERVAL_SECONDS=3600") {
		t.Fatal("enable must replace stale interval with managed default")
	}
	disabled := ApplyMisSortModuleEnv(enabled, MisSortOptions{})
	if strings.Contains(disabled, "CV_CAMERA_POLL_ENABLED=") || strings.Contains(disabled, "CV_CAMERA_POLL_INTERVAL_SECONDS=") || !strings.Contains(disabled, "CUSTOM=keep") {
		t.Fatal("disable must remove managed switch only")
	}
}

// TestEnsureWorkerZMQEnv 验证 Worker 公共环境锚点的计划落格订阅插值行幂等
// 补齐（只增不删）：渲染模板已含（跳过）、无锚点的老 Compose 原样跳过。
func TestEnsureWorkerZMQEnv(t *testing.T) {
	files, err := RenderModules("nvidia", RenderOptions{Workers: 1, CPUSets: []string{"2,4"}})
	if err != nil {
		t.Fatal(err)
	}
	compose := files["business/compose.yml"]
	if patched := EnsureWorkerZMQEnv(compose); patched != compose {
		t.Fatal("模板渲染结果应已包含订阅插值行")
	}
	stale := strings.Join([]string{
		"x-worker-environment: &worker-environment",
		"  TZ: Asia/Shanghai",
		"",
		"services:",
		"  center-cv-service-0:",
		"    image: center-cv-service:latest",
		"",
	}, "\n")
	patched := EnsureWorkerZMQEnv(stale)
	for _, want := range []string{
		"CV_PLANNED_ZMQ_ENDPOINTS: ${CV_PLANNED_ZMQ_ENDPOINTS:-tcp://127.0.0.1:8091}",
		"CV_EVENT_TIME_UTC_OFFSET_MINUTES: ${CV_EVENT_TIME_UTC_OFFSET_MINUTES:-480}",
	} {
		if !strings.Contains(patched, want) {
			t.Fatalf("锚点缺少插值行 %s:\n%s", want, patched)
		}
	}
	if again := EnsureWorkerZMQEnv(patched); again != patched {
		t.Fatal("补齐后应幂等")
	}
	if out := EnsureWorkerZMQEnv("services:\n  other:\n    image: x\n"); out != "services:\n  other:\n    image: x\n" {
		t.Fatal("缺少锚点的 Compose 应原样跳过")
	}
}
