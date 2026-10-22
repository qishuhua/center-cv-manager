package templates

import (
	"strings"
	"testing"
)

func TestApplyLogLevelModuleEnv(t *testing.T) {
	base := "# Module-specific Compose variables\nRABBITMQ_REALTIME_PRIORITY=5\n\n" + misSortEnvMarker + "\nCV_CAMERA_POLL_ENABLED=1\nCV_WMS_URL=http://wms\nCV_MIS_AUTH=auth\n"

	// 开启调试：追加托管块，既有行（含错分检测托管块）不动。
	on := ApplyLogLevelModuleEnv(base, true)
	for _, want := range []string{
		"CV_WMS_URL=http://wms",
		logLevelEnvMarker + "\nLOG_LEVEL=debug",
	} {
		if !strings.Contains(on, want) {
			t.Fatalf("开启 Debug 模式后 module.env 缺少 %q:\n%s", want, on)
		}
	}
	// 幂等：重复应用结果不变。
	if again := ApplyLogLevelModuleEnv(on, true); again != on {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", on, again)
	}
	// 关闭调试：托管块移除（回落 Compose 插值默认 info），错分块保留。
	off := ApplyLogLevelModuleEnv(on, false)
	if strings.Contains(off, "LOG_LEVEL=") || strings.Contains(off, logLevelEnvMarker) {
		t.Fatalf("关闭 Debug 模式后托管块应移除:\n%s", off)
	}
	if !strings.Contains(off, "CV_WMS_URL=http://wms") {
		t.Fatal("关闭 Debug 模式不应影响错分检测托管块")
	}
	// 用户手工写入的 LOG_LEVEL 也按托管变量清理，避免双来源漂移。
	manual := "# Module-specific Compose variables\nLOG_LEVEL=warn\n"
	if out := ApplyLogLevelModuleEnv(manual, false); strings.Contains(out, "LOG_LEVEL=") {
		t.Fatalf("手工 LOG_LEVEL 应被托管清理:\n%s", out)
	}
}

func TestEnsureWorkerLogLevelEnv(t *testing.T) {
	compose := strings.Join([]string{
		"name: center-cv-business",
		"",
		"x-worker-environment: &worker-environment",
		"  TZ: Asia/Shanghai",
		"  RABBITMQ_REALTIME_PRIORITY: ${RABBITMQ_REALTIME_PRIORITY:-5}",
		"",
		"x-worker-common: &worker-common",
		"  image: center-cv-service:nvidia",
		"",
	}, "\n")

	out := EnsureWorkerLogLevelEnv(compose)
	if !strings.Contains(out, "  LOG_LEVEL: ${LOG_LEVEL:-info}") {
		t.Fatalf("锚点应补 LOG_LEVEL 插值行:\n%s", out)
	}
	// 插入位置在锚点块内（x-worker-common 之前）。
	anchor := strings.Index(out, "LOG_LEVEL:")
	common := strings.Index(out, "x-worker-common:")
	if anchor < 0 || common < anchor {
		t.Fatalf("LOG_LEVEL 行应在锚点块内:\n%s", out)
	}
	// 幂等：已存在（任意形式）不再改动。
	if again := EnsureWorkerLogLevelEnv(out); again != out {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", out, again)
	}
	hardcoded := strings.Replace(compose, "  TZ: Asia/Shanghai", "  TZ: Asia/Shanghai\n  LOG_LEVEL: debug", 1)
	if out := EnsureWorkerLogLevelEnv(hardcoded); out != hardcoded {
		t.Fatalf("已存在硬编码 LOG_LEVEL 时不应改动:\n%s", out)
	}
	// 锚点缺失（异常布局）：原样返回。
	if out := EnsureWorkerLogLevelEnv("services:\n  x:\n    image: a\n"); !strings.HasPrefix(out, "services:") {
		t.Fatalf("无锚点时应原样返回:\n%s", out)
	}
}

func TestRenderModulesDebugLog(t *testing.T) {
	base := RenderOptions{Workers: 1, CPUSets: []string{"0-3"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心"}

	// 默认关闭：module.env 不写 LOG_LEVEL，Compose 锚点带插值默认 info。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env := files["business/compose.yml"], files["business/module.env"]
	if !strings.Contains(compose, "LOG_LEVEL: ${LOG_LEVEL:-info}") {
		t.Fatal("business Compose 锚点应含 LOG_LEVEL 插值行")
	}
	if strings.Contains(env, "LOG_LEVEL=") {
		t.Fatalf("默认关闭时 module.env 不应写 LOG_LEVEL:\n%s", env)
	}

	// 开启调试：module.env 写入 debug，两平台 Compose 锚点一致。
	base.DebugLogEnabled = true
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err = RenderModules(platform, base)
		if err != nil {
			t.Fatal(err)
		}
		if compose = files["business/compose.yml"]; !strings.Contains(compose, "LOG_LEVEL: ${LOG_LEVEL:-info}") {
			t.Fatalf("%s business Compose 锚点应含 LOG_LEVEL 插值行", platform)
		}
		if env = files["business/module.env"]; !strings.Contains(env, "LOG_LEVEL=debug") {
			t.Fatalf("%s module.env 缺少 LOG_LEVEL=debug:\n%s", platform, env)
		}
		if strings.Contains(files["infra/module.env"], "LOG_LEVEL=") {
			t.Fatalf("LOG_LEVEL 只应写入 business 模块")
		}
	}
}
