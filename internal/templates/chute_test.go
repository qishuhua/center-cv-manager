package templates

import (
	"strings"
	"testing"
)

func TestRenderModulesChuteAlwaysPresent(t *testing.T) {
	base := RenderOptions{Workers: 2, CPUSets: []string{"2,4", "6,8"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心"}

	// 未开启错分检测：chute 仍部署（吃插值默认值），module.env 为注释桩。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env := files["business/compose.yml"], files["business/module.env"]
	if !strings.Contains(compose, "chute-verification-service:") || !strings.Contains(compose, "${CV_WMS_URL:-http://10.21.61.48:31995}") {
		t.Fatal("错分审核服务应无条件渲染并带插值默认值")
	}
	if !strings.Contains(compose, "CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}") {
		t.Fatal("ingress 轮询插值行缺失")
	}
	if strings.Contains(env, "CV_WMS_URL=") || !strings.HasPrefix(env, "# Module-specific Compose variables") {
		t.Fatal("未开启错分检测时 module.env 应为注释桩")
	}

	// 开启错分检测：块不变，module.env 写入轮询开关与两项取值（机构编码不配置）。
	base.WMSURL, base.MisAuth = "http://10.21.61.48:31995", "20260606123a"
	files, err = RenderModules("denglin", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env = files["business/compose.yml"], files["business/module.env"]
	if !strings.Contains(compose, "chute-verification-service:") {
		t.Fatal("配置 WMS 后错分审核服务应在")
	}
	for _, line := range []string{
		"CV_CAMERA_POLL_ENABLED=1",
		"CV_WMS_URL=http://10.21.61.48:31995",
		"CV_MIS_AUTH=20260606123a",
	} {
		if !strings.Contains(env, line) {
			t.Fatalf("module.env 缺少 %s", line)
		}
	}
	if strings.Contains(env, "CV_ORGAN_CODE=") {
		t.Fatal("机构编码不再配置，module.env 不应写 CV_ORGAN_CODE")
	}
}

func TestApplyChuteServiceIdempotent(t *testing.T) {
	files, err := RenderModules("nvidia", RenderOptions{Workers: 2, CPUSets: []string{"2,4", "6,8"}, LogsRoot: "/srv/logs"})
	if err != nil {
		t.Fatal(err)
	}
	current := files["business/compose.yml"]

	// 已存在（模板渲染结果）：原样返回，重复应用幂等。
	kept, err := ApplyChuteService(current, "/srv/logs")
	if err != nil {
		t.Fatal(err)
	}
	if kept != current || kept != mustApplyChute(t, current, "/srv/logs") {
		t.Fatal("已存在的服务块必须原样保留且幂等")
	}
	// 模拟发布流程改写 image/labels 后（如 retag 成具体版本），再次应用不能动该块。
	published := strings.Replace(current, "image: chute-verification-service:latest", "image: chute-verification-service:main-a1b2c3", 1)
	if kept, err = ApplyChuteService(published, "/srv/logs"); err != nil || kept != published {
		t.Fatal("已发布镜像引用的服务块必须原样保留")
	}
	// 存量旧 Compose（缺块）：以锚点插入，插入后与当前模板形态一致。
	stale := current
	if start := strings.Index(stale, "\n  chute-verification-service:"); start >= 0 {
		stale = stale[:start] + stale[strings.Index(stale[start+1:], "\n  video-transcode-service:")+start+1:]
	}
	inserted, err := ApplyChuteService(stale, "/srv/logs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inserted, "chute-verification-service:") || inserted != mustApplyChute(t, inserted, "/srv/logs") {
		t.Fatal("缺失的块应被插入且结果幂等")
	}
	if _, err = ApplyChuteService("services:\n  center-cv-ingress:\n    image: x\n", ""); err == nil {
		t.Fatal("缺少插入锚点应报错")
	}
}

func mustApplyChute(t *testing.T, compose, logsRoot string) string {
	t.Helper()
	out, err := ApplyChuteService(compose, logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyMisSortModuleEnv(t *testing.T) {
	opts := MisSortOptions{Enabled: true, WMSURL: "http://h:1", MisAuth: "a"}
	fromStub := ApplyMisSortModuleEnv("# Module-specific Compose variables\n", opts)
	if fromStub != ApplyMisSortModuleEnv(fromStub, opts) {
		t.Fatal("重复应用应幂等")
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
	if strings.Contains(disabled, "CV_WMS_URL=") || strings.Contains(disabled, "CV_MIS_AUTH=") || strings.Contains(disabled, "CV_ORGAN_CODE=") || !strings.Contains(disabled, "RABBITMQ_REALTIME_PRIORITY=7") {
		t.Fatal("关闭后应移除托管变量并保留外部变量")
	}
}

func TestEnsureIngressPollEnv(t *testing.T) {
	files, err := RenderModules("nvidia", RenderOptions{Workers: 2, CPUSets: []string{"2,4"}})
	if err != nil {
		t.Fatal(err)
	}
	compose := files["business/compose.yml"]
	if patched, err := EnsureIngressPollEnv(compose); err != nil || patched != compose {
		t.Fatal("模板渲染结果应已包含全部插值行")
	}
	stripped := strings.Split(compose, "\n")
	kept := []string{}
	for _, line := range stripped {
		if strings.HasPrefix(line, "      CV_CAMERA_POLL_ENABLED:") {
			continue
		}
		kept = append(kept, line)
	}
	patched, err := EnsureIngressPollEnv(strings.Join(kept, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, "CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}") {
		t.Fatal("缺失的插值行应被补齐")
	}
	if again, err := EnsureIngressPollEnv(patched); err != nil || again != patched {
		t.Fatal("补齐后应幂等")
	}
	if _, err = EnsureIngressPollEnv("services:\n  other:\n    image: x\n"); err == nil {
		t.Fatal("缺少 ingress 服务应报错")
	}
}
