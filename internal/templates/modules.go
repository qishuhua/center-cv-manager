package templates

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// SilencedFeishuWebhook 是「监控平台告警」关闭（test_mode）的中心写入
// monitoring/alerting/feishu-webhook-url 的占位地址：alertmanager 配置不
// 变（url_file 仍被引用），通知发送必然失败仅记日志，中心本地告警评估与
// UI 不受影响。指向中心本机 discard 端口，连接立即被拒，不产生外发流量。
// 与开发机 monitor 包的 unconfiguredWebhook 同套路。
const SilencedFeishuWebhook = "http://127.0.0.1:9/platform-alerts-silenced"

// RenderModules returns independent files based on the categorized Docker
// repository templates. Legacy rendering remains separate for existing centers.
func RenderModules(platform string, opts RenderOptions) (map[string]string, error) {
	if platform != "nvidia" && platform != "denglin" {
		return nil, fmt.Errorf("模块模板需要 NVIDIA 或登临 GPU")
	}
	mode := platform
	if mode == "nvidia" {
		mode = "tensorrt"
	}
	files := map[string]string{}
	err := fs.WalkDir(templateFiles, "modules", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, "modules/")
		other := "nvidia"
		if platform == "nvidia" {
			other = "denglin"
		}
		if strings.Contains(rel, "."+other+".") || (platform == "nvidia" && (strings.Contains(rel, "dashboards-denglin/") || strings.Contains(rel, "denglin-gpu-exporter/"))) {
			return nil
		}
		content, err := fs.ReadFile(templateFiles, p)
		if err != nil {
			return err
		}
		content = []byte(strings.ReplaceAll(string(content), "\r\n", "\n"))
		if strings.HasPrefix(rel, "business/compose.") {
			content, err = renderDynamicWorkers(content, mode, opts.Workers, opts.CPUSets, opts.SingleWorker, opts.DenglinLogicalDevices)
			if err != nil {
				return err
			}
			// Repository aliases used by the source catalog are stable across tags.
			content = []byte(strings.Replace(string(content), "  image: center-cv-service:", "  labels:\n    com.center-manager.image.repository: center-cv-service\n  image: center-cv-service:", 1))
			content = []byte(strings.Replace(string(content), "    image: video-transcode-service:", "    labels:\n      com.center-manager.image.repository: video-transcode\n    image: video-transcode-service:", 1))
		}
		if strings.Contains(rel, "/compose.") {
			content = renderHostMounts(content, mode, opts.ModelsRoot, opts.LogsRoot, opts.MediaResourcesRoot)
			if err := validateGeneratedCompose(content); err != nil {
				return fmt.Errorf("模板 %s: %w", rel, err)
			}
			rel = path.Join(path.Dir(rel), "compose.yml")
		}
		if strings.HasPrefix(rel, "monitoring/prometheus/prometheus.") {
			content, err = renderDynamicPrometheus(content, mode, opts.Workers, opts.SingleWorker, opts.DenglinLogicalDevices)
			if err != nil {
				return err
			}

		}
		if rel == "monitoring/alerting/alertmanager.yml" {
			content, err = renderAlertServerName(content, opts.ServerName)
			if err != nil {
				return err
			}
			if strings.TrimSpace(opts.FeishuWebhook) == "" {
				var doc map[string]any
				if err = yaml.Unmarshal(content, &doc); err != nil {
					return err
				}
				// Keep alert evaluation and UI, explicitly disable outbound notifications.
				doc["receivers"] = []any{map[string]any{"name": "feishu"}}
				content, err = yaml.Marshal(doc)
				if err != nil {
					return err
				}
			}
		}
		files[rel] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Do not copy any machine-local credentials from the source repository.
	files["monitoring/alerting/feishu-webhook-url"] = strings.TrimSpace(opts.FeishuWebhook) + "\n"
	// Existing manager credential template is retained for backwards compatibility.
	auth, err := fs.ReadFile(templateFiles, "secrets/grafana-logs.htpasswd")
	if err != nil {
		return nil, err
	}
	files["gateway/secrets/grafana-logs.htpasswd"] = string(auth)
	for _, id := range []string{"infra", "logging", "business", "gateway", "monitoring"} {
		stub := "# Module-specific Compose variables\n"
		if id == "business" {
			stub = ApplyMisSortModuleEnv(stub, MisSortOptions{Enabled: opts.WMSURL != "" && opts.MisSortEnabled, WMSURL: opts.WMSURL, MisAuth: opts.MisAuth, IntervalSeconds: opts.MisSortIntervalSeconds})
			stub = ApplyLogLevelModuleEnv(stub, opts.DebugLogEnabled)
			stub = ApplySaveImageModuleEnv(stub, opts.SaveImageEnabled)
			stub = ApplyDecodeOnlyModuleEnv(stub, opts.DecodeOnlyEnabled)
			stub = ApplyTestAlarmModuleEnv(stub, opts.TestAlarmEnabled, opts.TestAlarmWebhook, opts.TestAlarmPlayerURL)
		}
		files[id+"/module.env"] = stub
	}
	return files, nil
}
