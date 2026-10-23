package templates

import (
	"strings"
	"testing"
)

func TestApplySaveImageModuleEnv(t *testing.T) {
	base := "# Module-specific Compose variables\nRABBITMQ_REALTIME_PRIORITY=5\n\n" + logLevelEnvMarker + "\nLOG_LEVEL=debug\n"

	// 开启存图：追加托管块，既有行（含 Debug 模式托管块）不动。
	on := ApplySaveImageModuleEnv(base, true)
	for _, want := range []string{
		"LOG_LEVEL=debug",
		saveImageEnvMarker + "\nCV_SAVE_DEBUG_IMAGE=1",
	} {
		if !strings.Contains(on, want) {
			t.Fatalf("开启存图后 module.env 缺少 %q:\n%s", want, on)
		}
	}
	// 幂等：重复应用结果不变。
	if again := ApplySaveImageModuleEnv(on, true); again != on {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", on, again)
	}
	// 关闭存图：托管块移除（回落 Compose 插值默认 0），Debug 块保留。
	off := ApplySaveImageModuleEnv(on, false)
	if strings.Contains(off, "CV_SAVE_DEBUG_IMAGE=") || strings.Contains(off, saveImageEnvMarker) {
		t.Fatalf("关闭存图后托管块应移除:\n%s", off)
	}
	if !strings.Contains(off, "LOG_LEVEL=debug") {
		t.Fatal("关闭存图不应影响 Debug 模式托管块")
	}
	// 用户手工写入的 CV_SAVE_DEBUG_IMAGE 也按托管变量清理，避免双来源漂移。
	manual := "# Module-specific Compose variables\nCV_SAVE_DEBUG_IMAGE=1\n"
	if out := ApplySaveImageModuleEnv(manual, false); strings.Contains(out, "CV_SAVE_DEBUG_IMAGE=") {
		t.Fatalf("手工 CV_SAVE_DEBUG_IMAGE 应被托管清理:\n%s", out)
	}
}

func TestEnsureWorkerSaveImageEnv(t *testing.T) {
	compose := strings.Join([]string{
		"name: center-cv-business",
		"",
		"x-worker-environment: &worker-environment",
		"  TZ: Asia/Shanghai",
		"  LOG_LEVEL: ${LOG_LEVEL:-info}",
		"",
		"x-worker-common: &worker-common",
		"  image: center-cv-service:nvidia",
		"",
	}, "\n")

	out := EnsureWorkerSaveImageEnv(compose)
	if !strings.Contains(out, "  CV_SAVE_DEBUG_IMAGE: ${CV_SAVE_DEBUG_IMAGE:-0}") {
		t.Fatalf("锚点应补 CV_SAVE_DEBUG_IMAGE 插值行:\n%s", out)
	}
	// 插入位置在锚点块内（x-worker-common 之前）。
	anchor := strings.Index(out, "CV_SAVE_DEBUG_IMAGE:")
	common := strings.Index(out, "x-worker-common:")
	if anchor < 0 || common < anchor {
		t.Fatalf("CV_SAVE_DEBUG_IMAGE 行应在锚点块内:\n%s", out)
	}
	// 幂等：已存在（任意形式）不再改动。
	if again := EnsureWorkerSaveImageEnv(out); again != out {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", out, again)
	}
	hardcoded := strings.Replace(compose, "  LOG_LEVEL: ${LOG_LEVEL:-info}", "  LOG_LEVEL: ${LOG_LEVEL:-info}\n  CV_SAVE_DEBUG_IMAGE: 1", 1)
	if out := EnsureWorkerSaveImageEnv(hardcoded); out != hardcoded {
		t.Fatalf("已存在硬编码 CV_SAVE_DEBUG_IMAGE 时不应改动:\n%s", out)
	}
	// 锚点缺失（异常布局）：原样返回。
	if out := EnsureWorkerSaveImageEnv("services:\n  x:\n    image: a\n"); !strings.HasPrefix(out, "services:") {
		t.Fatalf("无锚点时应原样返回:\n%s", out)
	}
}

func TestRenderModulesSaveImage(t *testing.T) {
	base := RenderOptions{Workers: 1, CPUSets: []string{"0-3"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心"}

	// 默认关闭：module.env 不写 CV_SAVE_DEBUG_IMAGE，Compose 锚点带插值默认 0。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env := files["business/compose.yml"], files["business/module.env"]
	if !strings.Contains(compose, "CV_SAVE_DEBUG_IMAGE: ${CV_SAVE_DEBUG_IMAGE:-0}") {
		t.Fatal("business Compose 锚点应含 CV_SAVE_DEBUG_IMAGE 插值行")
	}
	if strings.Contains(env, "CV_SAVE_DEBUG_IMAGE=") {
		t.Fatalf("默认关闭时 module.env 不应写 CV_SAVE_DEBUG_IMAGE:\n%s", env)
	}

	// 开启存图：module.env 写入 1，两平台 Compose 锚点一致。
	base.SaveImageEnabled = true
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err = RenderModules(platform, base)
		if err != nil {
			t.Fatal(err)
		}
		if compose = files["business/compose.yml"]; !strings.Contains(compose, "CV_SAVE_DEBUG_IMAGE: ${CV_SAVE_DEBUG_IMAGE:-0}") {
			t.Fatalf("%s business Compose 锚点应含 CV_SAVE_DEBUG_IMAGE 插值行", platform)
		}
		if env = files["business/module.env"]; !strings.Contains(env, "CV_SAVE_DEBUG_IMAGE=1") {
			t.Fatalf("%s module.env 缺少 CV_SAVE_DEBUG_IMAGE=1:\n%s", platform, env)
		}
		if strings.Contains(files["infra/module.env"], "CV_SAVE_DEBUG_IMAGE=") {
			t.Fatalf("CV_SAVE_DEBUG_IMAGE 只应写入 business 模块")
		}
	}
}
