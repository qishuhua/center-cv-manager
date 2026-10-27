// Package modular defines the shared boundaries for independent Compose projects.
package modular

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"gopkg.in/yaml.v3"
)

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func NewDeployment(composeFile string, workers int) *domain.Deployment {
	d := &domain.Deployment{Root: path.Dir(composeFile), ModelsRoot: centerpath.ModelsRoot(composeFile), LogsRoot: centerpath.LogsRoot(composeFile), MediaRoot: centerpath.MediaResourcesRoot(composeFile), Status: "preparing"}
	for _, entry := range []struct{ id, name string }{{"infra", "中间件"}, {"logging", "日志"}, {"business", "业务"}, {"gateway", "网关"}, {"monitoring", "监控"}} {
		m := domain.ComposeModule{ID: entry.id, Name: entry.name, Project: "center-cv-" + entry.id, ComposeFile: path.Join(d.Root, entry.id, "compose.yml"), Enabled: true, Timeout: 600, Status: "pending"}
		switch m.ID {
		case "infra":
			m.InitServices = []string{"rabbitmq-init"}
		case "logging":
			m.InitServices = []string{"elasticsearch-init", "kibana-init"}
		case "business":
			m.DependsOn = []string{"infra"}
			for i := 0; i < workers; i++ {
				m.ModelServices = append(m.ModelServices, fmt.Sprintf("center-cv-service-%d", i))
				m.ReadyURLs = append(m.ReadyURLs, fmt.Sprintf("http://127.0.0.1:%d/api/readyz", 8181+i))
			}
			// ingress（8090）/transcode（8092）就绪探活。错分检测由 ingress
			// 计划落格 ZeroMQ + Worker 本地核验承接，告警上报由 transcode
			// 直接完成。
			m.ReadyURLs = append(m.ReadyURLs, "http://127.0.0.1:8090/readyz", "http://127.0.0.1:8092/readyz")
		case "gateway":
			// 网关独立于业务：仅反代、不构成启动/就绪依赖（业务停止时网关
			// 502 属预期调试场景）。存量中心的旧依赖由 store 一次性迁移摘除。
		case "monitoring":
			m.ReadyURLs = []string{"http://127.0.0.1:9090/prometheus/-/ready", "http://127.0.0.1:3000/grafana/api/health"}
		}
		d.Modules = append(d.Modules, m)
	}
	return d
}

func Modules(c domain.Center) []domain.ComposeModule {
	if c.Deployment != nil {
		return c.Deployment.Modules
	}
	return []domain.ComposeModule{{ID: "legacy", Name: "原有部署", ComposeFile: c.ComposeFile, Enabled: true, Timeout: 600}}
}

func Find(c domain.Center, id string) (domain.ComposeModule, error) {
	for _, m := range Modules(c) {
		if m.ID == id {
			return m, nil
		}
	}
	return domain.ComposeModule{}, fmt.Errorf("模块不存在: %s", id)
}

func ModelsRoot(c domain.Center) string {
	if c.Deployment != nil {
		return c.Deployment.ModelsRoot
	}
	return centerpath.ModelsRoot(c.ComposeFile)
}

func Hash(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

func PlanRevision(m domain.ComposeModule, config string) string {
	// Runtime changes must not invalidate a failed task's configuration plan.
	m.Status = ""
	m.Error = ""
	m.Revision = ""
	m.AppliedRevision = ""
	m.UpdatedAt = time.Time{}
	m.ConfigChanged = false
	metadata, _ := json.Marshal(m)
	return Hash(config + string(metadata))
}

// Every invocation has exactly one Compose file and an explicit environment.
// The module's own .env is passed even when empty; ambient Compose variables
// cannot override project ownership or turn orphan removal on.
func Command(m domain.ComposeModule) string {
	if m.Project == "" {
		// Legacy centers retain their existing project/environment resolution.
		// Changing it before migration could silently select new data volumes.
		return "env COMPOSE_REMOVE_ORPHANS=false docker compose -f " + centerpath.ShellQuote(m.ComposeFile)
	}
	return CommandWithEnvFile(m, path.Join(path.Dir(m.ComposeFile), "module.env"))
}

// CommandWithEnvFile builds the modular Compose command with an explicit
// environment file, used when validating candidate compose/env pairs before
// either file replaces the live one.
func CommandWithEnvFile(m domain.ComposeModule, envFile string) string {
	base := "env -u COMPOSE_PROJECT_NAME -u COMPOSE_FILE -u COMPOSE_ENV_FILES -u COMPOSE_PROFILES -u CENTER_CV_DATA_PREFIX COMPOSE_REMOVE_ORPHANS=false docker compose"
	base += " -p " + centerpath.ShellQuote(m.Project) + " --env-file " + centerpath.ShellQuote(envFile)
	return base + " -f " + centerpath.ShellQuote(m.ComposeFile)
}

func CenterCommand(c domain.Center) string {
	return Command(domain.ComposeModule{Project: c.ComposeProject, ComposeFile: c.ComposeFile})
}

func Validate(d *domain.Deployment) error {
	if d == nil {
		return fmt.Errorf("缺少模块部署配置")
	}
	ids, projects, files := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range d.Modules {
		if !identifier.MatchString(m.ID) || !identifier.MatchString(m.Project) || strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("模块标识、项目名或名称无效")
		}
		if ids[m.ID] || projects[m.Project] || files[m.ComposeFile] {
			return fmt.Errorf("模块标识、项目名、Compose 路径必须唯一")
		}
		ids[m.ID], projects[m.Project], files[m.ComposeFile] = true, true, true
		if m.ComposeFile != path.Join(d.Root, m.ID, "compose.yml") {
			return fmt.Errorf("模块 Compose 必须位于 %s", path.Join(d.Root, m.ID, "compose.yml"))
		}
		if m.Timeout < 10 || m.Timeout > 3600 {
			return fmt.Errorf("模块超时必须介于 10 和 3600 秒")
		}
		for _, service := range append(append([]string{}, m.InitServices...), m.ModelServices...) {
			if !identifier.MatchString(service) {
				return fmt.Errorf("无效的服务名: %s", service)
			}
		}
		for _, raw := range m.ReadyURLs {
			u, e := url.Parse(raw)
			if e != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.User != nil {
				return fmt.Errorf("就绪探测仅支持本机 HTTP URL: %s", raw)
			}
		}
	}
	_, err := Order(d.Modules)
	return err
}

// Dependencies are checked even for disabled modules, so cycles cannot be
// hidden by disabling a node. Monitoring is a last-start barrier, not a runtime dependency.
func Order(modules []domain.ComposeModule) ([]domain.ComposeModule, error) {
	byID := map[string]domain.ComposeModule{}
	for _, m := range modules {
		byID[m.ID] = m
	}
	state := map[string]int{}
	out := []domain.ComposeModule{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("模块依赖存在循环: %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		m, ok := byID[id]
		if !ok {
			return fmt.Errorf("依赖模块不存在: %s", id)
		}
		state[id] = 1
		for _, dep := range m.DependsOn {
			other, ok := byID[dep]
			if !ok {
				return fmt.Errorf("依赖模块不存在: %s", dep)
			}
			if m.Enabled && !other.Enabled {
				return fmt.Errorf("模块 %s 依赖已禁用模块 %s", id, dep)
			}
			if dep == "monitoring" && id != "monitoring" {
				return fmt.Errorf("监控在初始化最后启动，不能作为 %s 的启动前置", id)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		if m.Enabled {
			out = append(out, m)
		}
		return nil
	}
	for _, m := range modules {
		if m.ID != "monitoring" {
			if err := visit(m.ID); err != nil {
				return nil, err
			}
		}
	}
	if _, ok := byID["monitoring"]; ok {
		if err := visit("monitoring"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type Config struct {
	Name     string `yaml:"name"`
	Services map[string]struct {
		Image         string `yaml:"image"`
		ContainerName string `yaml:"container_name"`
		Restart       string `yaml:"restart"`
	} `yaml:"services"`
}

func Parse(content string) (Config, error) {
	var c Config
	err := yaml.Unmarshal([]byte(content), &c)
	if err == nil && len(c.Services) == 0 {
		err = fmt.Errorf("Compose 没有服务")
	}
	return c, err
}

func ServiceNames(c Config) []string {
	names := []string{}
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func ValidateConfig(m domain.ComposeModule, content string) error {
	c, err := Parse(content)
	if err != nil {
		return err
	}
	if m.Project != "" && c.Name != "" && c.Name != m.Project {
		return fmt.Errorf("Compose name 必须与登记项目名 %s 一致", m.Project)
	}
	for _, n := range append(append([]string{}, m.InitServices...), m.ModelServices...) {
		if _, ok := c.Services[n]; !ok {
			return fmt.Errorf("登记的服务 %s 在 Compose 中不存在", n)
		}
	}
	return nil
}

// ReadyURLsForServices narrows built-in probes to the selected business
// services. Custom modules keep their explicitly configured readiness contract.
func ReadyURLsForServices(m domain.ComposeModule, services []string) []string {
	if m.ID != "business" {
		return m.ReadyURLs
	}
	ports := map[string]bool{}
	for _, name := range services {
		switch name {
		case "center-cv-ingress":
			ports["8090"] = true
		case "video-transcode-service":
			ports["8092"] = true
		default:
			if strings.HasPrefix(name, "center-cv-service-") {
				if index, err := strconv.Atoi(strings.TrimPrefix(name, "center-cv-service-")); err == nil {
					ports[strconv.Itoa(8181+index)] = true
				}
			}
		}
	}
	urls := []string{}
	for _, raw := range m.ReadyURLs {
		u, err := url.Parse(raw)
		if err == nil && ports[u.Port()] {
			urls = append(urls, raw)
		}
	}
	return urls
}
