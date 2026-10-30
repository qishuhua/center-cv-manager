package templates

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCleanupStaleManagedTemplates(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "docker-compose.tensorrt.yml")
	keep := filepath.Join(root, "docker-compose.nvidia.yml")
	for _, path := range []string{stale, keep} {
		if err := os.WriteFile(path, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupStaleManagedTemplates(root, []FileSpec{{TargetPath: "docker-compose.nvidia.yml"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("legacy TensorRT template was not removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("current NVIDIA template was removed: %v", err)
	}
}

func TestRenderDynamicDenglinWorkers(t *testing.T) {
	source := []byte("services:\n  center-cv-service-0:\n    old: true\n  center-cv-service-1:\n    old: true\n  center-cv-ingress:\n    image: ingress\n")
	result, err := renderDynamicWorkers(source, "denglin", 2, []string{"0-3", "4-7"}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, want := range []string{"center-cv-service-0:", "cpuset: \"0-3\"", "DL_DEVICES: \"0,1,2,3\"", "center-cv-service-1:", "cpuset: \"4-7\"", "DL_DEVICES: \"4,5,6,7\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated compose missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old: true") {
		t.Fatalf("old worker definitions were not removed:\n%s", text)
	}
}

func TestRenderEightDenglinPhysicalGPUsCover32LogicalDevices(t *testing.T) {
	content, err := fs.ReadFile(templateFiles, "compose/docker-compose.denglin.yml")
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderDynamicWorkers(content, "denglin", 8, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, want := range []string{
		"center-cv-service-0:",
		`DL_DEVICES: "0,1,2,3"`,
		"center-cv-service-7:",
		`DL_DEVICES: "28,29,30,31"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated compose missing %q", want)
		}
	}
	if strings.Contains(text, "center-cv-service-8:") || strings.Contains(text, `DL_DEVICES: "32,33,34,35"`) {
		t.Fatal("generated compose created a ninth worker or referenced logical devices above 31")
	}
}

func TestActualComposeTemplatesRenderDetectedTopology(t *testing.T) {
	for _, mode := range []string{"denglin", "tensorrt"} {
		content, err := fs.ReadFile(templateFiles, "compose/"+composeForMode(mode))
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"- --port=8084", "CADVISOR_HEALTHCHECK_URL: http://localhost:8084/healthz"} {
			if !strings.Contains(string(content), required) {
				t.Fatalf("%s compose must keep cAdvisor and its healthcheck on the non-conflicting 8084 port", mode)
			}
		}
		if mode == "tensorrt" && !strings.Contains(string(content), "center-cv-service:nvidia") {
			t.Fatal("NVIDIA compose must use the current nvidia business image tag")
		}
		if !strings.Contains(string(content), "RABBITMQ_NODENAME: rabbit@localhost") {
			t.Fatalf("%s compose must use a loopback RabbitMQ node name with host networking", mode)
		}
		for _, required := range []string{"kibana-init:", "condition: service_healthy", "./monitoring/kibana/init.sh:/config/init.sh:ro"} {
			if !strings.Contains(string(content), required) {
				t.Fatalf("%s compose missing Kibana initialization %q", mode, required)
			}
		}
		result, err := renderDynamicWorkers(content, mode, 2, []string{"0-7", "8-15"}, false)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var document struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(result, &document); err != nil {
			t.Fatalf("%s generated invalid YAML: %v", mode, err)
		}
		workers := 0
		for name := range document.Services {
			if strings.HasPrefix(name, "center-cv-service-") {
				workers++
			}
		}
		if workers != 2 {
			t.Fatalf("%s generated %d workers, want 2", mode, workers)
		}
		for _, service := range []string{"center-cv-ingress", "video-transcode-service", "alertmanager"} {
			if _, ok := document.Services[service]; !ok {
				t.Fatalf("%s generated compose missing service %q", mode, service)
			}
		}
		if got, want := len(document.Services), 20; got != want {
			t.Fatalf("%s generated %d services for 2 workers, want %d (26 for the standard 8-worker topology)", mode, got, want)
		}
	}
}

func TestStandardEightWorkerTemplatesHave26Services(t *testing.T) {
	for _, mode := range []string{"denglin", "tensorrt"} {
		content, err := fs.ReadFile(templateFiles, "compose/"+composeForMode(mode))
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(content, &document); err != nil {
			t.Fatalf("%s template is invalid YAML: %v", mode, err)
		}
		if got := len(document.Services); got != 26 {
			t.Fatalf("%s standard template has %d services, want 26", mode, got)
		}
	}
}

func TestVideoTranscodeReadinessEndpoint(t *testing.T) {
	const expected = `http://127.0.0.1:8092/readyz`
	const obsolete = `http://127.0.0.1:8092/api/readyz`
	for _, name := range []string{"prometheus.denglin.yml", "prometheus.nvidia.yml"} {
		content, err := fs.ReadFile(templateFiles, "monitoring/prometheus/"+name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if !strings.Contains(text, expected) {
			t.Fatalf("%s missing video-transcode readiness endpoint %q", name, expected)
		}
		if strings.Contains(text, obsolete) {
			t.Fatalf("%s still contains obsolete readiness endpoint %q", name, obsolete)
		}
	}
}

func TestLogTemplatesUseMatchingDataStreamAndCreateDataView(t *testing.T) {
	filebeat, err := fs.ReadFile(templateFiles, "monitoring/filebeat/filebeat.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(filebeat), "\r\n", "\n")
	for _, required := range []string{"index: center-cv-logs\n", "setup.template.name: center-cv-logs\n", "setup.template.pattern: center-cv-logs\n"} {
		if !strings.Contains(text, required) {
			t.Fatalf("Filebeat template missing %q", required)
		}
	}
	if strings.Contains(text, "center-cv-logs-*") {
		t.Fatal("Filebeat template pattern must match the exact data stream name")
	}
	if strings.Contains(text, "log.source:") || !strings.Contains(text, "event.dataset: center-cv-service") {
		t.Fatal("Filebeat custom fields must not conflict with the ECS log.source object")
	}
	initScript, err := fs.ReadFile(templateFiles, "monitoring/kibana/init.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"/api/data_views/data_view", `"timeFieldName":"@timestamp"`, "/api/data_views/default"} {
		if !strings.Contains(string(initScript), required) {
			t.Fatalf("Kibana init script missing %q", required)
		}
	}
}

func TestBusinessTemplateIncludesCurrentAlarmReporter(t *testing.T) {
	content, err := fs.ReadFile(templateFiles, "compose/docker-compose.business.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "hk-alarm-reporter") {
		t.Fatal("business compose missing current hk-alarm-reporter service")
	}
}

func TestComposeTemplatesUseSharedMediaResourcesPath(t *testing.T) {
	for _, name := range []string{"docker-compose.business.yml", "docker-compose.denglin.yml", "docker-compose.nvidia.yml"} {
		content, err := fs.ReadFile(templateFiles, "compose/"+name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if !strings.Contains(text, "/data/media-resources:/data/media-resources") {
			t.Fatalf("%s missing shared media-resources mount", name)
		}
		if strings.Contains(text, "/data/center-cv-service") {
			t.Fatalf("%s still contains obsolete center-cv-service data mount", name)
		}
	}
}

func TestRenderCreatesWebhookPlaceholderOnce(t *testing.T) {
	root := t.TempDir()
	if err := Render("tensorrt", root, RenderOptions{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "monitoring", "alerting", "feishu-webhook-url")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("webhook placeholder not created: %v", err)
	}
	if err := os.WriteFile(path, []byte("https://example.invalid/custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Render("tensorrt", root, RenderOptions{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "https://example.invalid/custom\n" {
		t.Fatal("render overwrote the configured webhook")
	}
}

func TestRenderUsesCenterAlertConfig(t *testing.T) {
	root := t.TempDir()
	const serverName = `西宁中心 "GPU-01"`
	const webhook = "https://open.feishu.cn/open-apis/bot/v2/hook/test-token"
	if err := Render("tensorrt", root, RenderOptions{Workers: 1, ServerName: serverName, FeishuWebhook: webhook}, nil); err != nil {
		t.Fatal(err)
	}
	alertmanager, err := os.ReadFile(filepath.Join(root, "monitoring", "alerting", "alertmanager.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(alertmanager), `server_name: "西宁中心 \"GPU-01\""`) {
		t.Fatalf("rendered alertmanager missing server name: %s", alertmanager)
	}
	webhookFile, err := os.ReadFile(filepath.Join(root, "monitoring", "alerting", "feishu-webhook-url"))
	if err != nil {
		t.Fatal(err)
	}
	if string(webhookFile) != webhook+"\n" {
		t.Fatalf("rendered webhook mismatch: %q", webhookFile)
	}
}

func TestRenderHostMounts(t *testing.T) {
	source := []byte("volumes:\n  - /data/models:/data/models:ro\n  - /data/logs:/data/logs\n  - /data/logs:/data/logs:ro\n  - /data/media-resources:/data/media-resources\n")
	result := string(renderHostMounts(source, "denglin", "/home/jt/center-deployer/models", "/home/jt/center-deployer/data/logs", "/home/jt/center-deployer/data/media-resources"))
	for _, want := range []string{
		`"/home/jt/center-deployer/models:/data/models:ro"`,
		`"/home/jt/center-deployer/data/logs:/data/logs"`,
		`"/home/jt/center-deployer/data/logs:/data/logs:ro"`,
		`"/home/jt/center-deployer/data/media-resources:/data/media-resources"`,
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("rendered mounts missing %q: %s", want, result)
		}
	}
	if strings.Contains(result, `":ro`) {
		t.Fatalf("read-only suffix rendered outside quotes: %s", result)
	}
	nvidia := string(renderHostMounts(source, "tensorrt", "/srv/models", "/srv/logs", "/srv/service"))
	if !strings.Contains(nvidia, `"/srv/models:/data/models:ro"`) {
		t.Fatalf("NVIDIA model root mounted incorrectly: %s", nvidia)
	}
}

func TestRenderDynamicPrometheus(t *testing.T) {
	source := []byte("  - job_name: center-cv-service\r\n    static_configs:\r\n      - targets: [\"127.0.0.1:8181\"]\r\n        labels: {worker: old}\r\n    metric_relabel_configs:\r\n      - action: drop\r\n  - job_name: service-http-health\r\n    static_configs:\r\n      - targets: [\"http://127.0.0.1:8181/api/healthz\"]\r\n        labels: {worker: old}\r\n      - targets: [\"http://127.0.0.1:8090/readyz\"]\r\n        labels: {service: ingress}\r\n    relabel_configs:\r\n  - job_name: service-readiness\r\n    static_configs:\r\n      - targets: [\"http://127.0.0.1:8181/api/readyz\"]\r\n        labels: {name: old}\r\n      - targets: [\"http://127.0.0.1:8090/readyz\"]\r\n        labels: {name: ingress}\r\n    relabel_configs:\r\n")
	result, err := renderDynamicPrometheus(source, "tensorrt", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, want := range []string{"127.0.0.1:8181", "127.0.0.1:8182", "center-cv-service-1", "http://127.0.0.1:8182/api/healthz", "http://127.0.0.1:8182/api/readyz"} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated prometheus config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "worker: old") || strings.Contains(text, "name: old") {
		t.Fatalf("old worker targets were not removed:\n%s", text)
	}
	if strings.Contains(text, ":8181/healthz") || strings.Contains(text, ":8181/readyz") {
		t.Fatalf("generated worker endpoints are missing /api:\n%s", text)
	}
}

func TestPrometheusWorkerHealthEndpointsHaveAPIPrefix(t *testing.T) {
	for _, name := range []string{"prometheus.denglin.yml", "prometheus.nvidia.yml"} {
		content, err := fs.ReadFile(templateFiles, "monitoring/prometheus/"+name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if strings.Contains(text, ":8181/healthz") || strings.Contains(text, ":8181/readyz") {
			t.Fatalf("%s contains a worker endpoint without the required /api prefix", name)
		}
		for _, want := range []string{
			`targets: ["http://127.0.0.1:8181/api/healthz"]`,
			`targets: ["http://127.0.0.1:8181/api/readyz"]`,
			`targets: ["http://127.0.0.1:8090/readyz"]`,
			`targets: ["http://127.0.0.1:8092/readyz"]`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing health endpoint %q", name, want)
			}
		}
	}
}

func TestPrometheusTemplatesTrackVideoTranscodeEndpoints(t *testing.T) {
	for _, name := range []string{"prometheus.denglin.yml", "prometheus.nvidia.yml"} {
		content, err := fs.ReadFile(templateFiles, "monitoring/prometheus/"+name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, want := range []string{
			"job_name: video-transcode-service",
			`targets: ["127.0.0.1:8092"]`,
			`targets: ["http://127.0.0.1:8092/readyz"]`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %q", name, want)
			}
		}
		if strings.Contains(text, "127.0.0.1:8092/api/readyz") {
			t.Fatalf("%s still uses the obsolete video transcode readiness URL", name)
		}
	}
}

func TestRenderUsesOneWorkerEnvironment(t *testing.T) {
	content, err := fs.ReadFile(templateFiles, "compose/docker-compose.nvidia.yml")
	if err != nil {
		t.Fatal(err)
	}
	selected := renderWorkerEnvironment(content, map[string]string{
		"LOG_LEVEL": "debug",
		"CUSTOM":    "a:b # value",
	})
	rendered, err := renderDynamicWorkers(selected, "tensorrt", 2, []string{"0-3", "4-7"}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	for _, want := range []string{
		"image: center-cv-service:nvidia",
		"image: center-cv-ingress:latest",
		"image: video-transcode-service:latest",
		`CUSTOM: "a:b # value"`,
		`LOG_LEVEL: "debug"`,
		"center-cv-service-1:",
		"<<: *worker-environment",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered compose missing %q:\n%s", want, text)
		}
	}
	var document map[string]any
	if err := yaml.Unmarshal(rendered, &document); err != nil {
		t.Fatalf("rendered compose is invalid YAML: %v", err)
	}
}
