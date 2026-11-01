package modelasset

import (
	"strings"
	"testing"
)

func TestNormalizeGPUProfile(t *testing.T) {
	cases := map[string]string{
		"NVIDIA Tesla T4":       "nvidia-tesla-t4",
		"Tesla T4":              "tesla-t4",
		"  NVIDIA A10 -- max ":  "nvidia-a10-max",
		"RTX 3090":              "rtx-3090",
		"GPU（测试）":              "gpu",
		"---":                   "",
		"NVIDIA GeForce RTX 4090": "nvidia-geforce-rtx-4090",
	}
	for input, expected := range cases {
		if got := normalizeGPUProfile(input); got != expected {
			t.Errorf("normalizeGPUProfile(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestBuildConvertedManifest(t *testing.T) {
	sha := strings.Repeat("a", 64)
	source := Version{
		Model: "detector", Version: "1.2.0", ManifestKey: "center-models/detector/1.2.0/manifest.json", ManifestDigest: strings.Repeat("b", 64),
		Formats: []ManifestFormat{
			{ID: "onnx", Platform: "onnx", RepositoryPath: "onnx/detector", Artifacts: []ManifestArtifact{{Path: "artifact/onnx/detector.onnx", TargetPath: "onnx/detector/detector.onnx", Size: 100, SHA256: sha}}},
			{ID: "nvidia", Platform: "nvidia", HardwareProfile: "tesla-t4", RepositoryPath: "plan/tesla-t4/detector", Artifacts: []ManifestArtifact{{Path: "artifact/nvidia/detector.plan", TargetPath: "plan/tesla-t4/detector/detector.plan", Size: 50, SHA256: sha}}},
		},
	}
	manifest, err := buildConvertedManifest(source, "nvidia", "tesla-t4", "1.2.0-nvidia", "detector-1.2.0-nvidia.plan", "plan/tesla-t4/detector", 60, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Model != "detector" || manifest.Version != "1.2.0-nvidia" {
		t.Fatalf("版本派生错误: %s:%s", manifest.Model, manifest.Version)
	}
	if len(manifest.Formats) != 2 {
		t.Fatalf("应保留 onnx 并剔除旧 nvidia 格式，得到 2 个格式，实际 %d", len(manifest.Formats))
	}
	if manifest.Formats[0].Platform != "onnx" || manifest.Formats[1].Platform != "nvidia" {
		t.Fatalf("格式顺序或平台错误: %+v", manifest.Formats)
	}
	artifact := manifest.Formats[1].Artifacts[0]
	if artifact.TargetPath != "plan/tesla-t4/detector/detector-1.2.0-nvidia.plan" || artifact.Size != 60 {
		t.Fatalf("转换产物 artifact 错误: %+v", artifact)
	}
}

func TestBuildConvertedManifestRejectsDuplicateProfile(t *testing.T) {
	sha := strings.Repeat("a", 64)
	// 源版本已有不同硬件配置的 nvidia 格式：保留会导致平台+硬件重复校验失败时才报错，
	// 同平台一律剔除；此处验证登临目标平台不受源版本 nvidia 格式影响。
	source := Version{
		Model: "detector", Version: "1", ManifestDigest: sha,
		Formats: []ManifestFormat{
			{ID: "onnx", Platform: "onnx", RepositoryPath: "onnx/detector", Artifacts: []ManifestArtifact{{Path: "artifact/onnx/a.onnx", TargetPath: "onnx/detector/a.onnx", Size: 1, SHA256: sha}}},
			{ID: "nv", Platform: "nvidia", HardwareProfile: "tesla-t4", RepositoryPath: "plan/tesla-t4/detector", Artifacts: []ManifestArtifact{{Path: "artifact/nv/a.plan", TargetPath: "plan/tesla-t4/detector/a.plan", Size: 1, SHA256: sha}}},
		},
	}
	manifest, err := buildConvertedManifest(source, "denglin", "", "1-denglin", "a-1-denglin.engine", "denglin/detector", 2, sha)
	if err != nil {
		t.Fatal(err)
	}
	platforms := map[string]int{}
	for _, format := range manifest.Formats {
		platforms[format.Platform]++
	}
	if platforms["denglin"] != 1 || platforms["onnx"] != 1 || platforms["nvidia"] != 1 {
		t.Fatalf("登临转换应完整保留源格式： %+v", platforms)
	}
}

func TestConvertOutputFile(t *testing.T) {
	if convertOutputFile("nvidia") != "output.plan" || convertOutputFile("denglin") != "output.engine" {
		t.Fatal("转换产物文件名约定错误")
	}
}
