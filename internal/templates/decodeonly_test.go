package templates

import (
	"strings"
	"testing"
)

func TestApplyDecodeOnlyModuleEnv(t *testing.T) {
	base := "# Module-specific Compose variables\nRABBITMQ_REALTIME_PRIORITY=5\n\n" + logLevelEnvMarker + "\nLOG_LEVEL=debug\n\n" + saveImageEnvMarker + "\nCV_SAVE_DEBUG_IMAGE=1\n"

	// 开启仅解码：追加托管块，既有行（含 Debug 模式、存图托管块）不动。
	on := ApplyDecodeOnlyModuleEnv(base, true)
	for _, want := range []string{
		"LOG_LEVEL=debug",
		"CV_SAVE_DEBUG_IMAGE=1",
		decodeOnlyEnvMarker + "\nCV_DECODE_ONLY_MODE=1",
	} {
		if !strings.Contains(on, want) {
			t.Fatalf("开启仅解码后 module.env 缺少 %q:\n%s", want, on)
		}
	}
	// 幂等：重复应用结果不变。
	if again := ApplyDecodeOnlyModuleEnv(on, true); again != on {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", on, again)
	}
	// 关闭仅解码：托管块移除（回落 Compose 插值默认 0），其余托管块保留。
	off := ApplyDecodeOnlyModuleEnv(on, false)
	if strings.Contains(off, "CV_DECODE_ONLY_MODE=") || strings.Contains(off, decodeOnlyEnvMarker) {
		t.Fatalf("关闭仅解码后托管块应移除:\n%s", off)
	}
	if !strings.Contains(off, "LOG_LEVEL=debug") || !strings.Contains(off, "CV_SAVE_DEBUG_IMAGE=1") {
		t.Fatal("关闭仅解码不应影响 Debug 模式与存图托管块")
	}
	// 用户手工写入的 CV_DECODE_ONLY_MODE 也按托管变量清理，避免双来源漂移。
	manual := "# Module-specific Compose variables\nCV_DECODE_ONLY_MODE=1\n"
	if out := ApplyDecodeOnlyModuleEnv(manual, false); strings.Contains(out, "CV_DECODE_ONLY_MODE=") {
		t.Fatalf("手工 CV_DECODE_ONLY_MODE 应被托管清理:\n%s", out)
	}
}

func TestEnsureWorkerDecodeOnlyEnv(t *testing.T) {
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

	out := EnsureWorkerDecodeOnlyEnv(compose)
	if !strings.Contains(out, "  CV_DECODE_ONLY_MODE: ${CV_DECODE_ONLY_MODE:-0}") {
		t.Fatalf("锚点应补 CV_DECODE_ONLY_MODE 插值行:\n%s", out)
	}
	// 插入位置在锚点块内（x-worker-common 之前）。
	anchor := strings.Index(out, "CV_DECODE_ONLY_MODE:")
	common := strings.Index(out, "x-worker-common:")
	if anchor < 0 || common < anchor {
		t.Fatalf("CV_DECODE_ONLY_MODE 行应在锚点块内:\n%s", out)
	}
	// 幂等：已存在（任意形式）不再改动。
	if again := EnsureWorkerDecodeOnlyEnv(out); again != out {
		t.Fatalf("重复应用应幂等:\n%s\n---\n%s", out, again)
	}
	hardcoded := strings.Replace(compose, "  LOG_LEVEL: ${LOG_LEVEL:-info}", "  LOG_LEVEL: ${LOG_LEVEL:-info}\n  CV_DECODE_ONLY_MODE: 1", 1)
	if out := EnsureWorkerDecodeOnlyEnv(hardcoded); out != hardcoded {
		t.Fatalf("已存在硬编码 CV_DECODE_ONLY_MODE 时不应改动:\n%s", out)
	}
	// 锚点缺失（异常布局）：原样返回。
	if out := EnsureWorkerDecodeOnlyEnv("services:\n  x:\n    image: a\n"); !strings.HasPrefix(out, "services:") {
		t.Fatalf("无锚点时应原样返回:\n%s", out)
	}
}

func TestRenderModulesDecodeOnly(t *testing.T) {
	base := RenderOptions{Workers: 1, CPUSets: []string{"0-3"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心"}

	// 默认关闭：module.env 不写 CV_DECODE_ONLY_MODE，Compose 锚点带插值默认 0。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose, env := files["business/compose.yml"], files["business/module.env"]
	if !strings.Contains(compose, "CV_DECODE_ONLY_MODE: ${CV_DECODE_ONLY_MODE:-0}") {
		t.Fatal("business Compose 锚点应含 CV_DECODE_ONLY_MODE 插值行")
	}
	if strings.Contains(env, "CV_DECODE_ONLY_MODE=") {
		t.Fatalf("默认关闭时 module.env 不应写 CV_DECODE_ONLY_MODE:\n%s", env)
	}

	// 开启仅解码：module.env 写入 1，两平台 Compose 锚点一致。
	base.DecodeOnlyEnabled = true
	for _, platform := range []string{"nvidia", "denglin"} {
		files, err = RenderModules(platform, base)
		if err != nil {
			t.Fatal(err)
		}
		if compose = files["business/compose.yml"]; !strings.Contains(compose, "CV_DECODE_ONLY_MODE: ${CV_DECODE_ONLY_MODE:-0}") {
			t.Fatalf("%s business Compose 锚点应含 CV_DECODE_ONLY_MODE 插值行", platform)
		}
		if env = files["business/module.env"]; !strings.Contains(env, "CV_DECODE_ONLY_MODE=1") {
			t.Fatalf("%s module.env 缺少 CV_DECODE_ONLY_MODE=1:\n%s", platform, env)
		}
		if strings.Contains(files["infra/module.env"], "CV_DECODE_ONLY_MODE=") {
			t.Fatalf("CV_DECODE_ONLY_MODE 只应写入 business 模块")
		}
	}
}
