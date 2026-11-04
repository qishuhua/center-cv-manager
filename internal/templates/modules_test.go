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
				if id == "monitoring" {
					cadvisor := services["cadvisor"].(map[string]any)
					env, ok := cadvisor["environment"].(map[string]any)
					if !ok || env["CADVISOR_HEALTHCHECK_URL"] != "http://127.0.0.1:8084/healthz" {
						t.Fatal("cAdvisor healthcheck must use its configured port 8084")
					}
				}
				if id == "infra" {
					// 任务状态存储 redis：host 网络仅回环、AOF 持久化、禁止逐出
					// 运行中的任务记录，数据卷随模块前缀命名。
					content := files[id+"/compose.yml"]
					for _, want := range []string{"image: redis:7.2-alpine", `"--bind", "127.0.0.1"`, `"--appendonly", "yes"`, `"--maxmemory-policy", "noeviction"`} {
						if !strings.Contains(content, want) {
							t.Fatalf("infra redis 配置缺少 %s", want)
						}
					}
					redis, _ := services["redis"].(map[string]any)
					if redis == nil {
						t.Fatal("infra 模块应包含 redis 服务")
					}
					if _, hasPorts := redis["ports"]; hasPorts {
						t.Fatal("redis host 网络不能配置 ports")
					}
					volumes, _ := doc["volumes"].(map[string]any)
					if _, ok := volumes["redis-data"]; !ok {
						t.Fatal("redis 数据卷未定义")
					}
				}
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
					// 2 workers + ingress + transcode。
					if len(services) != 4 {
						t.Fatalf("expected 2 workers + ingress + transcode, got %d", len(services))
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

func TestSilencedWebhookKeepsReceiver(t *testing.T) {
	// 监控平台告警静默中心经占位地址静默：receivers 与 url_file 保持原样
	//（重载后发送必然失败仅记日志），重新打开时只换回文件内容即可恢复。
	files, err := RenderModules("nvidia", RenderOptions{Workers: 1, ServerName: "静默中心", FeishuWebhook: SilencedFeishuWebhook})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(files["monitoring/alerting/feishu-webhook-url"]); got != SilencedFeishuWebhook {
		t.Fatalf("webhook 文件应为占位地址，实际 %q", got)
	}
	if !strings.Contains(files["monitoring/alerting/alertmanager.yml"], "url_file: /run/secrets/feishu-webhook-url") {
		t.Fatal("静默渲染不应摘除 receivers/url_file")
	}
}
