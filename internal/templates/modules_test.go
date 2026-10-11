package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIndependentModuleTemplates(t *testing.T) {
	for _, platform := range []string{"nvidia", "denglin"} {
		t.Run(platform, func(t *testing.T) {
			files, err := RenderModules(platform, RenderOptions{Workers: 2, CPUSets: []string{"2,4", "6,8"}, ModelsRoot: "/home/test/data/models", LogsRoot: "/home/test/data/logs", MediaResourcesRoot: "/home/test/data/media", ServerName: "测试中心"})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"infra", "logging", "business", "gateway", "monitoring"} {
				var doc map[string]any
				if err = yaml.Unmarshal([]byte(files[id+"/compose.yml"]), &doc); err != nil {
					t.Fatal(err)
				}
				if doc["name"] != "center-cv-"+id {
					t.Fatal(doc["name"])
				}
				services := doc["services"].(map[string]any)
				for name, value := range services {
					svc := value.(map[string]any)
					switch deps := svc["depends_on"].(type) {
					case map[string]any:
						for dep := range deps {
							if _, ok := services[dep]; !ok {
								t.Fatalf("%s/%s cross-file dependency %s", id, name, dep)
							}
						}
					case []any:
						for _, dep := range deps {
							if _, ok := services[dep.(string)]; !ok {
								t.Fatalf("cross-file dependency %s", dep)
							}
						}
					}
				}
				if id == "business" {
					if len(services) != 5 {
						t.Fatalf("expected 2 workers + 3 services, got %d", len(services))
					}
					if !strings.Contains(files[id+"/compose.yml"], "/home/test/data/models") {
						t.Fatal("model mount not rendered")
					}
				}
			}
			prom := files["monitoring/prometheus/prometheus."+platform+".yml"]
			if strings.Contains(prom, "8183") || strings.Contains(prom, "/api/api/") || !strings.Contains(prom, "8181/api/readyz") {
				t.Fatal("incorrect readiness targets", prom)
			}
			if strings.TrimSpace(files["monitoring/alerting/feishu-webhook-url"]) != "" {
				t.Fatal("unexpected secret")
			}
			if strings.Contains(files["monitoring/alerting/alertmanager.yml"], "webhook_configs") {
				t.Fatal("unconfigured notifications enabled")
			}
		})
	}
}
