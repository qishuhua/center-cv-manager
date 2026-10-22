package provision

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"gopkg.in/yaml.v3"
)

type MigrationPlan struct {
	Revision   string             `json:"revision"`
	Deployment *domain.Deployment `json:"deployment"`
	Containers []string           `json:"containers"`
	Volumes    []string           `json:"volumes"`
	Files      map[string]string  `json:"-"`
}

func serviceCategory(name string) string {
	switch name {
	case "rabbitmq", "rabbitmq-init":
		return "infra"
	case "nginx":
		return "gateway"
	case "elasticsearch", "elasticsearch-init", "kibana", "kibana-init", "filebeat":
		return "logging"
	case "prometheus", "grafana", "alertmanager", "node-exporter", "cadvisor", "blackbox-exporter", "mtail", "dcgm-exporter", "denglin-gpu-exporter":
		return "monitoring"
	default:
		return "business"
	}
}

// SplitLegacy uses resolved config: existing absolute mounts and exact volume
// names survive project ownership changes. Unknown services are retained in
// business and shown in the migration plan; no source file is overwritten.
func SplitLegacy(c domain.Center, resolved string) (MigrationPlan, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(resolved), &doc); err != nil {
		return MigrationPlan{}, err
	}
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return MigrationPlan{}, fmt.Errorf("旧 Compose 没有服务")
	}
	project, _ := doc["name"].(string)
	if project == "" {
		return MigrationPlan{}, fmt.Errorf("无法确定旧项目名")
	}
	d := modular.NewDeployment(c.ComposeFile, 0)
	d.ModelsRoot = centerpath.ModelsRoot(c.ComposeFile)
	d.Migration = &domain.ModuleMigration{Project: project}
	d.Status = "migrating"
	groups := map[string]map[string]any{}
	owners := map[string]string{}
	for name, spec := range services {
		group := serviceCategory(name)
		if groups[group] == nil {
			groups[group] = map[string]any{}
		}
		groups[group][name] = spec
		owners[name] = group
	}
	volumes, _ := doc["volumes"].(map[string]any)
	plan := MigrationPlan{Deployment: d, Files: map[string]string{}, Volumes: []string{}}
	modules := []domain.ComposeModule{}
	dataRoots := map[string]string{}
	for _, m := range d.Modules {
		group := groups[m.ID]
		if len(group) == 0 {
			continue
		}
		m.InitServices = nil
		m.ModelServices = nil
		m.ReadyURLs = nil
		m.DependsOn = nil
		deps := map[string]bool{}
		usedVolumes := map[string]any{}
		for name, raw := range group {
			spec, ok := raw.(map[string]any)
			if !ok {
				return plan, fmt.Errorf("服务 %s 无效", name)
			}
			if depMap, ok := spec["depends_on"].(map[string]any); ok {
				for dep := range depMap {
					if owners[dep] != m.ID {
						if owners[dep] == "" {
							return plan, fmt.Errorf("未知依赖 %s", dep)
						}
						deps[owners[dep]] = true
						delete(depMap, dep)
					}
				}
			}
			if mounts, ok := spec["volumes"].([]any); ok {
				for _, rawMount := range mounts {
					mount, ok := rawMount.(map[string]any)
					if !ok {
						return plan, fmt.Errorf("迁移需要 Compose config 展开后的挂载")
					}
					if mount["type"] == "bind" {
						target, _ := mount["target"].(string)
						source, _ := mount["source"].(string)
						if target == "/data/models" || target == "/data/logs" || target == "/data/media-resources" {
							if previous := dataRoots[target]; previous != "" && previous != source {
								return plan, fmt.Errorf("多个服务对 %s 使用不同主机路径，请先统一目录", target)
							}
							dataRoots[target] = source
						}
					}
					if mount["type"] == "volume" {
						source, _ := mount["source"].(string)
						definition, _ := volumes[source].(map[string]any)
						actual, _ := definition["name"].(string)
						if actual == "" {
							return plan, fmt.Errorf("无法确定卷 %s 的实际名称", source)
						}
						usedVolumes[source] = map[string]any{"name": actual, "external": true}
						plan.Volumes = append(plan.Volumes, actual)
					}
				}
			}
			if name == "rabbitmq-init" || name == "elasticsearch-init" || name == "kibana-init" {
				m.InitServices = append(m.InitServices, name)
			}
			if name == "center-cv-service" || strings.HasPrefix(name, "center-cv-service-") {
				m.ModelServices = append(m.ModelServices, name)
			}
		}
		for dep := range deps {
			m.DependsOn = append(m.DependsOn, dep)
		}
		sort.Strings(m.DependsOn)
		// 网关独立于业务（与默认部署同口径）：反代不构成模块级依赖，服务级
		// depends_on 已在上面从目标 Compose 摘除，这里不再记入模块依赖。
		if m.ID == "gateway" {
			kept := m.DependsOn[:0]
			for _, dep := range m.DependsOn {
				if dep != "business" {
					kept = append(kept, dep)
				}
			}
			m.DependsOn = kept
		}
		sort.Strings(m.InitServices)
		sort.Strings(m.ModelServices)
		candidate := map[string]any{"name": m.Project, "services": group}
		if len(usedVolumes) > 0 {
			candidate["volumes"] = usedVolumes
		}
		// Keep all non-service resource definitions; normalized config uses absolute paths.
		for _, key := range []string{"networks", "configs", "secrets"} {
			if value, exists := doc[key]; exists {
				candidate[key] = value
			}
		}
		content, err := yaml.Marshal(candidate)
		if err != nil {
			return plan, err
		}
		plan.Files[m.ID] = string(content)
		modules = append(modules, m)
	}
	d.Modules = modules
	for _, m := range modules {
		d.Migration.Projects = append(d.Migration.Projects, m.Project)
	}
	if root := dataRoots["/data/models"]; root != "" {
		d.ModelsRoot = root
	}
	if root := dataRoots["/data/logs"]; root != "" {
		d.LogsRoot = root
	}
	if root := dataRoots["/data/media-resources"]; root != "" {
		d.MediaRoot = root
	}
	if err := modular.Validate(d); err != nil {
		return plan, err
	}
	sort.Strings(plan.Volumes)
	return plan, nil
}

func (s *Service) PlanMigration(ctx context.Context, id string) (MigrationPlan, error) {
	remote, c, err := s.connect(id)
	if err != nil {
		return MigrationPlan{}, err
	}
	defer remote.Close()
	if c.Deployment != nil {
		return MigrationPlan{}, fmt.Errorf("中心已登记模块；未完成迁移请重试原任务")
	}
	raw, err := runSSH(ctx, remote.SSH, modular.Command(modular.Modules(c)[0])+" config", "")
	if err != nil {
		return MigrationPlan{}, err
	}
	plan, err := SplitLegacy(c, raw)
	if err != nil {
		return plan, err
	}
	for _, name := range plan.Volumes {
		if _, err = remote.Client.VolumeInspect(ctx, name); err != nil {
			return plan, fmt.Errorf("不能复用数据卷 %s: %w", name, err)
		}
	}
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return plan, err
	}
	parsed, err := modular.Parse(raw)
	if err != nil {
		return plan, err
	}
	for _, v := range containers {
		for _, targetProject := range plan.Deployment.Migration.Projects {
			if v.Labels["com.docker.compose.project"] == targetProject {
				return plan, fmt.Errorf("目标项目 %s 已有容器，请先明确现有部署归属", targetProject)
			}
		}
		for _, spec := range parsed.Services {
			for _, name := range v.Names {
				if spec.ContainerName != "" && strings.TrimPrefix(name, "/") == spec.ContainerName && v.Labels["com.docker.compose.project"] != plan.Deployment.Migration.Project {
					return plan, fmt.Errorf("同名容器 %s 属于其他项目", spec.ContainerName)
				}
			}
		}
	}
	for _, v := range containers {
		if v.Labels["com.docker.compose.project"] == plan.Deployment.Migration.Project {
			if _, exists := parsed.Services[v.Labels["com.docker.compose.service"]]; !exists {
				return plan, fmt.Errorf("旧项目存在 Compose 未登记的容器 %s，请先明确其归属", v.ID)
			}
			plan.Deployment.Migration.Containers = append(plan.Deployment.Migration.Containers, v.ID)
			plan.Containers = append(plan.Containers, strings.Join(v.Names, ","))
		}
	}
	sort.Strings(plan.Deployment.Migration.Containers)
	sort.Strings(plan.Containers)
	source, err := readRemoteFile(ctx, remote.SSH, c.ComposeFile)
	if err != nil {
		return plan, err
	}
	plan.Deployment.Migration.SourceRevision = modular.Hash(source)
	plan.Revision = modular.Hash(raw + strings.Join(plan.Deployment.Migration.Containers, ",") + plan.Deployment.Migration.SourceRevision)
	return plan, nil
}

func (s *Service) Migrate(ctx context.Context, id, revision string, r Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		remote, c, err := s.connect(id)
		if err != nil {
			return err
		}
		defer remote.Close()
		if c.Deployment == nil {
			plan, err := s.PlanMigration(ctx, id)
			if err != nil {
				return err
			}
			if plan.Revision != revision {
				return fmt.Errorf("旧部署状态已变化，请重新生成迁移计划")
			}
			for _, m := range plan.Deployment.Modules {
				if existing, e := readRemoteFile(ctx, remote.SSH, m.ComposeFile); e == nil && existing != plan.Files[m.ID] {
					return fmt.Errorf("迁移目标 %s 已存在且内容不同，停止覆盖", m.ComposeFile)
				} else if e != nil {
					if _, e = runSSH(ctx, remote.SSH, "test ! -e "+shellQuote(m.ComposeFile), ""); e != nil {
						return fmt.Errorf("不能读取迁移目标文件")
					}
				}
			}
			// Validate every staged file before stopping any old container.
			for _, m := range plan.Deployment.Modules {
				if err = uploadRemoteFile(ctx, remote.SSH, path.Join(path.Dir(m.ComposeFile), "module.env"), "# Imported legacy environment is resolved in compose.yml\n", "0600"); err != nil {
					return err
				}
				if err = uploadRemoteFile(ctx, remote.SSH, m.ComposeFile, plan.Files[m.ID], "0600"); err != nil {
					return err
				}
				if _, err = runSSH(ctx, remote.SSH, modular.Command(m)+" config --quiet", ""); err != nil {
					return err
				}
			}
			c.Deployment = plan.Deployment
			if err = s.centers.UpdateDeployment(id, c.Deployment); err != nil {
				return err
			}
		}
		migration := c.Deployment.Migration
		if migration == nil || migration.Completed {
			return fmt.Errorf("没有待执行的旧项目迁移")
		}
		for _, containerID := range migration.Containers {
			info, err := remote.Client.ContainerInspect(ctx, containerID)
			if errdefs.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Config.Labels["com.docker.compose.project"] != migration.Project {
				return fmt.Errorf("容器归属变化，停止迁移")
			}
			r.Logf("迁移旧容器 %s；保留全部数据卷", info.Name)
			timeout := 60
			if err = remote.Client.ContainerStop(ctx, containerID, containertypes.StopOptions{Timeout: &timeout}); err != nil {
				return err
			}
			if err = remote.Client.ContainerRemove(ctx, containerID, containertypes.RemoveOptions{RemoveVolumes: false}); err != nil {
				return err
			}
		}
		if err = s.ResumeModules(ctx, id, r); err != nil {
			return err
		}
		_, _, current, err := s.centers.Connection(id)
		if err != nil {
			return err
		}
		if current.Deployment.Status != "ready" {
			return fmt.Errorf("迁移尚未完成：%s，请补齐业务条件后重试", current.Deployment.Status)
		}
		current.Deployment.Migration.Completed = true
		return s.centers.UpdateDeployment(id, current.Deployment)
	})
}

// RollbackMigration reconstructs the untouched legacy Compose using the same
// volumes. Only containers belonging to registered target projects are removed.
func (s *Service) RollbackMigration(ctx context.Context, id string, r Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		remote, c, err := s.connect(id)
		if err != nil {
			return err
		}
		defer remote.Close()
		if c.Deployment == nil || c.Deployment.Migration == nil {
			return fmt.Errorf("没有可回退的迁移记录")
		}
		original, err := readRemoteFile(ctx, remote.SSH, c.ComposeFile)
		if err != nil {
			return err
		}
		if modular.Hash(original) != c.Deployment.Migration.SourceRevision {
			return fmt.Errorf("原 Compose 已被修改，无法直接回退；请先恢复迁移时的原文件")
		}
		legacy := domain.ComposeModule{ComposeFile: c.ComposeFile}
		legacyBase := modular.Command(legacy) + " -p " + shellQuote(c.Deployment.Migration.Project)
		if _, err = runSSH(ctx, remote.SSH, legacyBase+" config --quiet", ""); err != nil {
			return err
		}
		containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
		if err != nil {
			return err
		}
		projects := map[string]bool{}
		for _, project := range c.Deployment.Migration.Projects {
			projects[project] = true
		}
		for _, m := range c.Deployment.Modules {
			if !projects[m.Project] {
				return fmt.Errorf("迁移后新增了模块 %s，请先单独处理后再回退", m.Name)
			}
		}
		for _, v := range containers {
			if projects[v.Labels["com.docker.compose.project"]] {
				timeout := 60
				if err = remote.Client.ContainerStop(ctx, v.ID, containertypes.StopOptions{Timeout: &timeout}); err != nil {
					return err
				}
				if err = remote.Client.ContainerRemove(ctx, v.ID, containertypes.RemoveOptions{RemoveVolumes: false}); err != nil {
					return err
				}
			}
		}
		r.Log("恢复原 Compose；数据卷保持不变")
		if _, err = runSSH(ctx, remote.SSH, legacyBase+" up -d", ""); err != nil {
			return err
		}
		raw, err := runSSH(ctx, remote.SSH, legacyBase+" config", "")
		if err != nil {
			return err
		}
		parsed, err := modular.Parse(raw)
		if err != nil {
			return err
		}
		legacy.Project = c.Deployment.Migration.Project
		legacy.Timeout = 600
		for _, m := range c.Deployment.Modules {
			legacy.InitServices = append(legacy.InitServices, m.InitServices...)
		}
		if err = modular.Wait(ctx, remote, legacy, modular.ServiceNames(parsed), nil); err != nil {
			return err
		}
		return s.centers.UpdateDeployment(id, nil)
	})
}
