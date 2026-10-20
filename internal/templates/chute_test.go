package templates

import (
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	base.WMSURL, base.MisAuth, base.MisSortEnabled = "http://10.21.61.48:31995", "20260606123a", true
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
	// 老版本模板初始化的中心没有转码服务锚点：回退追加到 services 段末尾
	//（下一个顶级键之前），插入后幂等。
	legacy := "services:\n  center-cv-ingress:\n    image: x\n\nnetworks:\n  default:\n"
	appended, err := ApplyChuteService(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	chuteIdx := strings.Index(appended, "  chute-verification-service:")
	networksIdx := strings.Index(appended, "\nnetworks:")
	if chuteIdx < 0 || networksIdx < chuteIdx {
		t.Fatalf("chute 块应追加在 services 段末尾（networks 之前）:\n%s", appended)
	}
	if kept, err := ApplyChuteService(appended, ""); err != nil || kept != appended {
		t.Fatal("追加后的 Compose 应保持幂等")
	}
	// 没有 services 段的非法输入仍应报错。
	if _, err = ApplyChuteService("not: compose\n", ""); err == nil {
		t.Fatal("缺少 services 段应报错")
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

// TestMisSortSyncOnLegacyCompose 模拟模板演进前初始化的极老中心 business
// Compose（无转码服务、无 ingress），错分同步的三步模板操作必须全部成功
// 且幂等——WMS 配置同步不应被任何模板版本差异阻断。
func TestMisSortSyncOnLegacyCompose(t *testing.T) {
	legacy := strings.Join([]string{
		"services:",
		"  center-cv-service-0:",
		"    image: center-cv-service:latest",
		"    network_mode: host",
		"",
		"networks:",
		"  default:",
		"",
	}, "\n")
	compose, err := ApplyChuteService(legacy, "/srv/logs")
	if err != nil {
		t.Fatalf("老 Compose 添加 chute 服务失败: %v", err)
	}
	compose, err = EnsureIngressPollEnv(compose)
	if err != nil {
		t.Fatalf("老 Compose 补轮询插值失败: %v", err)
	}
	chuteIdx := strings.Index(compose, "  chute-verification-service:")
	networksIdx := strings.Index(compose, "\nnetworks:")
	if chuteIdx < 0 || networksIdx < chuteIdx {
		t.Fatalf("chute 块应在 services 段内:\n%s", compose)
	}
	env := ApplyMisSortModuleEnv("# Module-specific Compose variables\n", MisSortOptions{Enabled: true, WMSURL: "http://h:1", MisAuth: "a"})
	if !strings.Contains(env, "CV_WMS_URL=http://h:1") {
		t.Fatal("module.env 应写入 WMS 取值")
	}
	// 全链路幂等。
	if again, err := ApplyChuteService(compose, "/srv/logs"); err != nil || again != compose {
		t.Fatal("chute 服务应用应幂等")
	}
	if again, err := EnsureIngressPollEnv(compose); err != nil || again != compose {
		t.Fatal("轮询插值应用应幂等")
	}
	if again := ApplyMisSortModuleEnv(env, MisSortOptions{Enabled: true, WMSURL: "http://h:1", MisAuth: "a"}); again != env {
		t.Fatal("module.env 应用应幂等")
	}
}

// TestEnsureFallEventsProxy 验证网关 /fall-events/ 反代的幂等补齐：新模板
// 已内置（跳过）、老配置补齐、部分存在只补缺失、补齐后幂等。
func TestEnsureFallEventsProxy(t *testing.T) {
	current, err := fs.ReadFile(templateFiles, "modules/gateway/nginx/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	// 模板已内置：除 LF 行尾归一化外不应有任何改动。
	normalized := strings.ReplaceAll(string(current), "\r\n", "\n")
	if patched := EnsureFallEventsProxy(string(current)); patched != normalized {
		t.Fatal("模板已内置反代时不应改动（仅行尾归一化）")
	}
	// 老配置：无 upstream、无 location，全部补齐且幂等。
	stale := strings.ReplaceAll(string(current), "upstream chute_backend", "upstream removed_chute")
	stale = strings.ReplaceAll(stale, "location /fall-events/", "location /removed-fall-events/")
	patched := EnsureFallEventsProxy(stale)
	if !strings.Contains(patched, "upstream chute_backend") || !strings.Contains(patched, "location /fall-events/") {
		t.Fatal("老配置应补齐 upstream 与 location")
	}
	if again := EnsureFallEventsProxy(patched); again != patched {
		t.Fatal("补齐后应幂等")
	}
	// 插入位置：location 在任务创建 location 之前、catch-all 之前。
	chuteIdx := strings.Index(patched, "location /fall-events/")
	taskIdx := strings.Index(patched, "location ~ ^/api/v3/")
	if chuteIdx < 0 || taskIdx < chuteIdx {
		t.Fatal("fall-events location 应位于任务创建 location 之前")
	}
	// 极简兜底：无 cv_ingress 锚点时插在 server 块 / catch-all 之前。
	minimal := "http {\n    server {\n        listen 8080;\n        location / {\n            return 404;\n        }\n    }\n}\n"
	simple := EnsureFallEventsProxy(minimal)
	if !strings.Contains(simple, "upstream chute_backend") || !strings.Contains(simple, "location /fall-events/") {
		t.Fatalf("兜底锚点也应完成插入:\n%s", simple)
	}
}

// TestApplyChuteServiceNonStandardIndent 复现手写或编辑器重排过的 business
// Compose（服务键不是模板的 2 空格缩进）：此前插入块硬编码 2 空格、锚点与
// 服务头行按 2 空格精确匹配全部落空，兜底插入后与服务缩进错位，YAML 终检
// 报 did not find expected key。修复后按文件实际缩进平移插入，幂等判定也
// 不再依赖缩进。
func TestApplyChuteServiceNonStandardIndent(t *testing.T) {
	wide := strings.Join([]string{
		"services:",
		"    center-cv-service-0:",
		"        image: center-cv-service:latest",
		"        healthcheck:",
		`            test: ["CMD", "true"]`,
		"            interval: 10s",
		"            timeout: 3s",
		"            retries: 12",
		"            start_period: 10s",
		"",
		"    video-transcode-service:",
		"        image: video-transcode-service:latest",
		"",
		"networks:",
		"    default:",
		"",
	}, "\n")
	// 锚点路径：插在转码服务之前，缩进平移到 4 空格。
	inserted, err := ApplyChuteService(wide, "/srv/logs")
	if err != nil {
		t.Fatalf("4 空格缩进 Compose 添加 chute 服务失败: %v", err)
	}
	if !strings.Contains(inserted, "\n    chute-verification-service:") ||
		!strings.Contains(inserted, "        image: chute-verification-service:latest") {
		t.Fatalf("chute 块应按 4 空格缩进插在转码服务之前:\n%s", inserted)
	}
	assertChuteInServices(t, inserted)
	if again, err := ApplyChuteService(inserted, "/srv/logs"); err != nil || again != inserted {
		t.Fatal("插入后应保持幂等")
	}

	// 无转码锚点：回退到 services 段末尾追加，同样按实际缩进平移（线上
	// 同步失败的实际路径：错位插入 → 终检拒绝）。
	noAnchor := strings.Join([]string{
		"services:",
		"    center-cv-service-0:",
		"        image: center-cv-service:latest",
		"        healthcheck:",
		`            test: ["CMD", "true"]`,
		"            timeout: 3s",
		"            retries: 12",
		"            start_period: 10s",
		"",
		"networks:",
		"    default:",
		"",
	}, "\n")
	appended, err := ApplyChuteService(noAnchor, "")
	if err != nil {
		t.Fatalf("无锚点 4 空格 Compose 追加 chute 服务失败: %v", err)
	}
	chuteIdx := strings.Index(appended, "    chute-verification-service:")
	networksIdx := strings.Index(appended, "\nnetworks:")
	if chuteIdx < 0 || networksIdx < chuteIdx {
		t.Fatalf("chute 块应按 4 空格缩进追加在 services 段末尾:\n%s", appended)
	}
	assertChuteInServices(t, appended)
	if again, err := ApplyChuteService(appended, ""); err != nil || again != appended {
		t.Fatal("追加后应保持幂等")
	}

	// 已有 4 空格 chute 块且镜像被发布流程改写：原样保留，不重复插入。
	published := strings.Replace(appended,
		"        image: chute-verification-service:latest",
		"        image: chute-verification-service:main-a1b2c3d4e5f6", 1)
	if kept, err := ApplyChuteService(published, ""); err != nil || kept != published {
		t.Fatal("已存在的 4 空格服务块必须原样保留")
	}
}

// TestEnsureIngressPollEnvNonStandardIndent 验证轮询插值补齐对非 2 空格
// 缩进的 ingress 服务同样生效（此前按 2/4/6 空格精确匹配会静默跳过）。
func TestEnsureIngressPollEnvNonStandardIndent(t *testing.T) {
	wide := strings.Join([]string{
		"services:",
		"    center-cv-ingress:",
		"        image: center-cv-ingress:latest",
		"        environment:",
		"            CV_ORGAN_CODE: 637101",
		"",
		"networks:",
		"    default:",
		"",
	}, "\n")
	patched, err := EnsureIngressPollEnv(wide)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"            CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}",
		"            CV_WMS_URL: ${CV_WMS_URL:-}",
	} {
		if !strings.Contains(patched, want) {
			t.Fatalf("插值行应按 8 空格缩进补齐（缺 %s）:\n%s", want, patched)
		}
	}
	if strings.Count(patched, "CV_ORGAN_CODE:") != 1 {
		t.Fatalf("已存在的变量不应被改动:\n%s", patched)
	}
	if again, err := EnsureIngressPollEnv(patched); err != nil || again != patched {
		t.Fatal("补齐后应幂等")
	}
	assertValidYAML(t, patched)

	// 无 environment 段：按实际缩进自动补段（字段在 8 空格，条目在 10）。
	bare := "services:\n    center-cv-ingress:\n        image: x\n"
	patched, err = EnsureIngressPollEnv(bare)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, "        environment:") ||
		!strings.Contains(patched, "          CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}") {
		t.Fatalf("无 environment 段时应按块内字段缩进补段:\n%s", patched)
	}
	if again, err := EnsureIngressPollEnv(patched); err != nil || again != patched {
		t.Fatal("补段后应幂等")
	}
	assertValidYAML(t, patched)
}

func assertChuteInServices(t *testing.T, compose string) {
	t.Helper()
	services := assertValidYAML(t, compose)
	if _, ok := services["chute-verification-service"]; !ok {
		t.Fatalf("chute 服务应位于 services 段:\n%s", compose)
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
	// 老版本模板没有 ingress 服务：静默跳过（无服务即无轮询可配），
	// 原样返回不报错、不阻断 WMS 同步。
	if skipped, err := EnsureIngressPollEnv("services:\n  other:\n    image: x\n"); err != nil || skipped != "services:\n  other:\n    image: x\n" {
		t.Fatal("缺少 ingress 服务应原样跳过")
	}
	// 有 ingress 服务但没有 environment 段：自动补段并插入全部插值行。
	bare := "services:\n  center-cv-ingress:\n    image: center-cv-ingress:latest\n"
	patched, err = EnsureIngressPollEnv(bare)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, "    environment:") || !strings.Contains(patched, "CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}") {
		t.Fatalf("无 environment 段时应自动补段:\n%s", patched)
	}
	if again, err := EnsureIngressPollEnv(patched); err != nil || again != patched {
		t.Fatal("补段后应幂等")
	}
}
