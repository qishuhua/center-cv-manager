package templates

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// validateGeneratedCompose checks runtime assumptions shared by our templates.
// YAML/Compose syntax validation alone cannot detect these startup failures.
func validateGeneratedCompose(content []byte) error {
	var doc struct {
		Services map[string]struct {
			NetworkMode string `yaml:"network_mode"`
			Environment any    `yaml:"environment"`
			Ports       []any  `yaml:"ports"`
			DependsOn   any    `yaml:"depends_on"`
			Healthcheck struct {
				Test    []string `yaml:"test"`
				Disable bool     `yaml:"disable"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return err
	}
	if len(doc.Services) == 0 {
		return fmt.Errorf("生成的 Compose 没有服务")
	}
	for name, svc := range doc.Services {
		if svc.NetworkMode == "host" {
			if len(svc.Ports) > 0 {
				return fmt.Errorf("%s: host 网络不能依赖 ports 端口映射", name)
			}
			if name == "rabbitmq" && templateEnv(svc.Environment, "RABBITMQ_NODENAME") != "rabbit@localhost" {
				return fmt.Errorf("%s: host 网络必须设置 RABBITMQ_NODENAME=rabbit@localhost，避免节点地址解析失败", name)
			}
		}
		deps := map[string]string{}
		switch v := svc.DependsOn.(type) {
		case map[string]any:
			for dep, value := range v {
				deps[dep] = ""
				if opts, ok := value.(map[string]any); ok {
					deps[dep], _ = opts["condition"].(string)
				}
			}
		case []any:
			for _, dep := range v {
				deps[fmt.Sprint(dep)] = ""
			}
		}
		for dep, condition := range deps {
			target, ok := doc.Services[dep]
			if !ok {
				return fmt.Errorf("%s: 依赖 %s 不在同一个 Compose 中", name, dep)
			}
			if condition == "service_healthy" && (target.Healthcheck.Disable || len(target.Healthcheck.Test) == 0 || strings.EqualFold(target.Healthcheck.Test[0], "NONE")) {
				return fmt.Errorf("%s: 依赖 %s 的健康检查未配置", name, dep)
			}
		}
	}
	return nil
}

func templateEnv(value any, key string) string {
	switch env := value.(type) {
	case map[string]any:
		return fmt.Sprint(env[key])
	case []any:
		for _, item := range env {
			k, v, ok := strings.Cut(fmt.Sprint(item), "=")
			if ok && k == key {
				return v
			}
		}
	}
	return ""
}
