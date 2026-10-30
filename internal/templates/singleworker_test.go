package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderModulesSingleWorkerNvidia(t *testing.T) {
	base := RenderOptions{Workers: 4, CPUSets: []string{"0-7", "8-15", "16-23", "24-31"}, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心", SingleWorker: true}

	// 开启单实例：只部署一个 Worker，可见全部 GPU（NVIDIA_VISIBLE_DEVICES=all）
	// 且不绑定 cpuset（由调度器使用全部 CPU）。
	files, err := RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	compose := files["business/compose.yml"]
	if strings.Contains(compose, "center-cv-service-1:") {
		t.Fatalf("单实例模式只应部署一个 Worker:\n%s", compose)
	}
	if !strings.Contains(compose, `NVIDIA_VISIBLE_DEVICES: "all"`) {
		t.Fatalf("单实例 Worker 应可见全部 GPU:\n%s", compose)
	}
	if strings.Contains(compose, "cpuset:") {
		t.Fatalf("单实例 Worker 不应绑定 cpuset:\n%s", compose)
	}
	var document struct {
		Services map[string]any `yaml:"services"`
	}
	if err = yaml.Unmarshal([]byte(compose), &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document.Services["center-cv-service-0"]; !ok {
		t.Fatal("单实例模式缺少 center-cv-service-0")
	}
	if len(document.Services) != 3 {
		t.Fatalf("单实例 business 应只有 1 Worker + ingress + transcode 共 3 个服务，实际 %d", len(document.Services))
	}
	// 监控只生成一组 Worker 目标，标签标注全部物理卡范围。
	prom := files["monitoring/prometheus/prometheus.nvidia.yml"]
	if strings.Contains(prom, "8182") {
		t.Fatalf("单实例监控不应有第二个 Worker 目标:\n%s", prom)
	}
	if !strings.Contains(prom, `physical_gpu: "0-3"`) || !strings.Contains(prom, `logical_gpus: "0-3"`) {
		t.Fatalf("单实例监控标签应标注全部物理卡:\n%s", prom)
	}

	// 关闭单实例：恢复按物理显卡数一对一生成并绑定 cpuset。
	base.SingleWorker = false
	files, err = RenderModules("nvidia", base)
	if err != nil {
		t.Fatal(err)
	}
	if compose = files["business/compose.yml"]; !strings.Contains(compose, "center-cv-service-3:") || !strings.Contains(compose, "cpuset: \"0-7\"") {
		t.Fatalf("默认模式应按显卡数生成 Worker 并绑定 cpuset:\n%s", compose)
	}
}

func TestRenderModulesSingleWorkerDenglin(t *testing.T) {
	base := RenderOptions{Workers: 2, ModelsRoot: "/data/models", LogsRoot: "/data/logs", MediaResourcesRoot: "/data/media", ServerName: "测试中心", SingleWorker: true}

	files, err := RenderModules("denglin", base)
	if err != nil {
		t.Fatal(err)
	}
	compose := files["business/compose.yml"]
	// 2 张物理卡 → 8 个逻辑卡全部交给唯一实例。
	if !strings.Contains(compose, `DL_DEVICES: "0,1,2,3,4,5,6,7"`) {
		t.Fatalf("单实例登临 Worker 应占用全部逻辑卡:\n%s", compose)
	}
	if strings.Contains(compose, "center-cv-service-1:") || strings.Contains(compose, "cpuset:") {
		t.Fatalf("单实例模式只应部署一个不绑核的 Worker:\n%s", compose)
	}
	prom := files["monitoring/prometheus/prometheus.denglin.yml"]
	if !strings.Contains(prom, `logical_gpus: "0-7"`) || !strings.Contains(prom, `physical_gpu: "0-1"`) {
		t.Fatalf("单实例登临监控标签应标注全部逻辑卡:\n%s", prom)
	}
}
