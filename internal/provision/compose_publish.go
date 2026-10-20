package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/errdefs"
	containertypes "github.com/docker/docker/api/types/container"
	"gopkg.in/yaml.v3"
)

type ComposePublishPlan struct {
	File             string   `json:"file"`
	Project          string   `json:"project"`
	Services         []string `json:"services"`
	Changed          bool     `json:"changed"`
	Revision         string   `json:"revision"`
	Action           string   `json:"action"`
	base             string
	contentRevision  string
	resolvedRevision string
	filesRevision    string
	module           domain.ComposeModule
}
type composePublishedState struct {
	Content  string `json:"content_revision"`
	Resolved string `json:"resolved_revision"`
	Files    string `json:"files_revision"`
	Failed   bool   `json:"failed"`
}

func publishStatePath(file string) string {
	return path.Join(path.Dir(file), ".center-manager", "published", modular.Hash(file)+".json")
}
func readPublishedState(ctx context.Context, r *sshx.DockerClient, file string) (composePublishedState, error) {
	p := publishStatePath(file)
	text, err := runSSH(ctx, r.SSH, "if [ -f "+shellQuote(p)+" ]; then cat "+shellQuote(p)+"; else printf '{}'; fi", "")
	var state composePublishedState
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(text), &state)
	return state, err
}
func writePublishedState(ctx context.Context, r *sshx.DockerClient, file string, state composePublishedState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	p := publishStatePath(file)
	tmp := p + fmt.Sprintf(".tmp.%d", time.Now().UnixNano())
	if err = uploadRemoteFile(ctx, r.SSH, tmp, string(data), "0600"); err != nil {
		return err
	}
	_, err = runSSH(ctx, r.SSH, "mv "+shellQuote(tmp)+" "+shellQuote(p), "")
	return err
}
func publishAction(changed bool) string {
	if changed {
		return "deploy"
	}
	return "restart"
}

// requireLocalImages verifies every pull_policy:never image referenced by the
// Compose exists on the center, so an up never strips running services just to
// fail on a missing image.
func requireLocalImages(ctx context.Context, r *sshx.DockerClient, base string) error {
	cfgText, err := runSSH(ctx, r.SSH, base+" config", "")
	if err != nil {
		return fmt.Errorf("Compose 校验失败: %w", err)
	}
	var doc struct {
		Services map[string]struct {
			Image      string `yaml:"image"`
			PullPolicy string `yaml:"pull_policy"`
		} `yaml:"services"`
	}
	if err = yaml.Unmarshal([]byte(cfgText), &doc); err != nil {
		return err
	}
	names := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := doc.Services[name]
		if !strings.EqualFold(strings.TrimSpace(svc.PullPolicy), "never") || strings.TrimSpace(svc.Image) == "" {
			continue
		}
		if _, _, err = r.Client.ImageInspectWithRaw(ctx, svc.Image); err != nil {
			return fmt.Errorf("服务 %s 需要本地镜像 %s，但中心上不存在；请先发布业务版本锁定镜像，或预置该镜像后再发布", name, svc.Image)
		}
	}
	return nil
}
func inferComposeProject(cfg modular.Config, containers []types.Container) (string, error) {
	projects := map[string]bool{}
	for service, spec := range cfg.Services {
		for _, c := range containers {
			for _, name := range c.Names {
				if spec.ContainerName != "" && strings.TrimPrefix(name, "/") == spec.ContainerName {
					p := c.Labels["com.docker.compose.project"]
					if p == "" || c.Labels["com.docker.compose.service"] != service {
						return "", fmt.Errorf("容器 %s 不属于可识别的 Compose 服务", spec.ContainerName)
					}
					projects[p] = true
				}
			}
		}
	}
	if len(projects) > 1 {
		return "", fmt.Errorf("该 Compose 的容器属于多个项目，不能整体发布")
	}
	for p := range projects {
		return p, nil
	}
	if cfg.Name == "" {
		return "", fmt.Errorf("无法确定 Compose 项目名")
	}
	return cfg.Name, nil
}
func hashesMatch(output, project string, containers []types.Container) bool {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false
		}
		count++
		found := false
		for _, c := range containers {
			if c.Labels["com.docker.compose.project"] == project && c.Labels["com.docker.compose.service"] == fields[0] && c.Labels["com.docker.compose.config-hash"] == fields[1] && !strings.EqualFold(c.Labels["com.docker.compose.oneoff"], "true") {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return count > 0
}
func (s *Service) composePublishPlan(ctx context.Context, r *sshx.DockerClient, c domain.Center, id string) (ComposePublishPlan, error) {
	p := ComposePublishPlan{}
	var err error
	if c.Deployment != nil {
		p.module, err = modular.Find(c, id)
		if err != nil {
			return p, err
		}
		if !p.module.Enabled {
			return p, fmt.Errorf("模块已禁用")
		}
		p.File = p.module.ComposeFile
		p.base = modular.Command(p.module)
	} else {
		if id == "compose.center-manager.override.yml" {
			return p, fmt.Errorf("不能直接发布内部覆盖文件")
		}
		p.File, err = legacyComposeTarget(ctx, r, c.ComposeFile, []string{id})
		if err != nil {
			return p, err
		}
		p.base = "env COMPOSE_REMOVE_ORPHANS=false docker compose -f " + shellQuote(p.File)
	}
	content, err := readRemoteFile(ctx, r.SSH, p.File)
	if err != nil {
		return p, err
	}
	p.contentRevision = modular.Hash(content)
	resolved, err := runSSH(ctx, r.SSH, p.base+" config", "")
	if err != nil {
		return p, fmt.Errorf("Compose 校验失败: %w", err)
	}
	cfg, err := modular.Parse(resolved)
	if err != nil {
		return p, err
	}
	containers, err := r.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return p, err
	}
	if c.Deployment == nil {
		p.Project, err = inferComposeProject(cfg, containers)
		if err != nil {
			return p, err
		}
		p.base = "env COMPOSE_REMOVE_ORPHANS=false docker compose -p " + shellQuote(p.Project) + " -f " + shellQuote(p.File)
		resolved, err = runSSH(ctx, r.SSH, p.base+" config", "")
		if err != nil {
			return p, err
		}
		cfg, err = modular.Parse(resolved)
		if err != nil {
			return p, err
		}
	} else {
		p.Project = p.module.Project
	}
	if err = modular.CheckOwnership(ctx, r, domain.ComposeModule{Project: p.Project}, cfg); err != nil {
		return p, err
	}
	p.Services = modular.ServiceNames(cfg)
	p.resolvedRevision = modular.Hash(resolved)
	p.filesRevision, err = configurationRevision(ctx, r, c, p.File, resolved)
	if err != nil {
		return p, err
	}
	state, err := readPublishedState(ctx, r, p.File)
	if err != nil {
		return p, err
	}
	if state.Content != "" {
		p.Changed = publicationChanged(state, p.contentRevision, p.resolvedRevision)
	} else if c.Deployment != nil && p.module.AppliedRevision != "" {
		p.Changed = p.module.AppliedRevision != p.contentRevision || p.module.ConfigChanged
	} else {
		hashes, e := runSSH(ctx, r.SSH, p.base+" config --hash '*'", "")
		p.Changed = e != nil || !hashesMatch(hashes, p.Project, containers)
	}
	if state.Files == "" || state.Files != p.filesRevision || (c.Deployment != nil && p.module.ConfigChanged) {
		p.Changed = true
	}
	p.Action = publishAction(p.Changed)
	p.Revision = modular.Hash(p.File + p.Project + p.contentRevision + p.resolvedRevision + p.filesRevision + p.Action + p.module.FilesRevision + modular.PlanRevision(p.module, resolved))
	return p, nil
}
func (s *Service) PlanComposePublish(ctx context.Context, id, file string) (ComposePublishPlan, error) {
	r, c, err := s.connect(id)
	if err != nil {
		return ComposePublishPlan{}, err
	}
	defer r.Close()
	return s.composePublishPlan(ctx, r, c, file)
}

// Capture a proven deployed baseline before replacing a file. Saving never publishes.
func (s *Service) captureComposeBaseline(ctx context.Context, r *sshx.DockerClient, c domain.Center, id string) error {
	p, err := s.composePublishPlan(ctx, r, c, id)
	if err != nil {
		// An invalid old file has no provable baseline, but must remain editable.
		return nil
	}
	state, err := readPublishedState(ctx, r, p.File)
	if err != nil {
		return err
	}
	if state.Content == "" && !p.Changed {
		return writePublishedState(ctx, r, p.File, composePublishedState{Content: p.contentRevision, Resolved: p.resolvedRevision, Files: p.filesRevision})
	}
	return nil
}
func (s *Service) PublishCompose(ctx context.Context, id, file, revision string, confirmUnchanged bool, reporter Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		r, c, err := s.connect(id)
		if err != nil {
			return err
		}
		defer r.Close()
		p, err := s.composePublishPlan(ctx, r, c, file)
		if err != nil {
			return err
		}
		if revision == "" || revision != p.Revision {
			return fmt.Errorf("配置或发布状态已变化，请重新点击发布")
		}
		if !p.Changed && !confirmUnchanged {
			return fmt.Errorf("未修改内容，请确认是否仍然发布（重启整个 Compose）")
		}
		// up --force-recreate removes running containers before creating the new
		// ones; a missing pull_policy:never image would delete the old service
		// and fail to start its replacement. Refuse before touching anything.
		if p.Action == "deploy" {
			if err = requireLocalImages(ctx, r, p.base); err != nil {
				return err
			}
		}
		state, err := readPublishedState(ctx, r, p.File)
		if err != nil {
			return err
		}
		// Keep failures visibly pending, including a partially completed restart.
		state.Failed = true
		if state.Content == "" {
			state.Content = p.contentRevision
			state.Resolved = p.resolvedRevision
		}
		if err = writePublishedState(ctx, r, p.File, state); err != nil {
			return err
		}
		if c.Deployment != nil {
			if p.Action == "deploy" {
				p.module.ConfigChanged = true
			}
			if e := s.applyModule(ctx, r, &c, p.module, p.Action, nil, reporter); e != nil {
				return e
			}
		} else {
			cfgText, e := runSSH(ctx, r.SSH, p.base+" config", "")
			if e != nil {
				return e
			}
			cfg, e := modular.Parse(cfgText)
			if e != nil {
				return e
			}
			m := domain.ComposeModule{ID: file, Name: file, Project: p.Project, Timeout: 600}
			for name, spec := range cfg.Services {
				if strings.HasSuffix(name, "-init") || spec.Restart == "no" {
					m.InitServices = append(m.InitServices, name)
				}
			}
			command := p.base + " up -d --force-recreate"
			if !p.Changed {
				command = p.base + " restart --no-deps"
			}
			selected := []string{}
			for _, name := range p.Services {
				if !p.Changed && containsString(m.InitServices, name) {
					continue
				}
				selected = append(selected, name)
			}
			if len(selected) == 0 {
				return fmt.Errorf("没有可发布的常驻服务")
			}
			for _, name := range selected {
				command += " " + shellQuote(name)
			}
			reporter.Message("正在整体发布 Compose：" + file)
			reporter.Log("$ " + command)
			output, e := runSSH(ctx, r.SSH, command, "")
			if strings.TrimSpace(output) != "" {
				reporter.Log(strings.TrimSpace(output))
			}
			if e != nil {
				return e
			}
			if e = modular.Wait(ctx, r, m, p.Services, nil); e != nil {
				return e
			}
		}
		// 配置变化时按项目标签清理已不在配置中的服务容器（保留数据卷）。
		if p.Action == "deploy" {
			removed, e := removeVanishedServices(ctx, r, p.Project, p.Services)
			if e != nil {
				return e
			}
			if len(removed) > 0 {
				reporter.Message("已移除发布后不再定义的服务：" + strings.Join(removed, ", "))
			}
		}
		// Re-read all configuration inputs after applying. SSH changes made during
		// deployment must not be recorded as successfully published.
		current, err := s.composePublishPlan(ctx, r, c, file)
		if err != nil {
			return err
		}
		if current.contentRevision != p.contentRevision || current.resolvedRevision != p.resolvedRevision || current.filesRevision != p.filesRevision {
			return fmt.Errorf("发布期间配置文件发生变化，保留未发布状态，请刷新后重新发布")
		}
		if err = writePublishedState(ctx, r, p.File, composePublishedState{Content: p.contentRevision, Resolved: p.resolvedRevision, Files: p.filesRevision}); err != nil {
			return err
		}
		reporter.Progress(100)
		reporter.Message("Compose 发布成功")
		return nil
	})
}
// removeVanishedServices 停止并移除仍带着 Compose 项目标签、但服务已不在
// 当前配置中的容器（如关闭错分检测后移除错分审核服务）。设计上不使用
// --remove-orphans，清理必须按项目标签 + 服务名精确匹配；数据卷一律保留。
func removeVanishedServices(ctx context.Context, r *sshx.DockerClient, project string, keep []string) ([]string, error) {
	if project == "" || len(keep) == 0 {
		return nil, nil
	}
	kept := make(map[string]bool, len(keep))
	for _, name := range keep {
		kept[name] = true
	}
	containers, err := r.Client.ContainerList(ctx, containertypes.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+project)),
	})
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, c := range containers {
		service := c.Labels["com.docker.compose.service"]
		if service == "" || kept[service] {
			continue
		}
		timeout := 60
		if err = r.Client.ContainerStop(ctx, c.ID, containertypes.StopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
			return removed, err
		}
		if err = r.Client.ContainerRemove(ctx, c.ID, containertypes.RemoveOptions{RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
			return removed, err
		}
		removed = append(removed, service)
	}
	return removed, nil
}
func containsString(values []string, value string) bool {
	for _, s := range values {
		if s == value {
			return true
		}
	}
	return false
}

func (s *Service) ListComposePublications(ctx context.Context, id string) ([]domain.ComposeModule, error) {
	files, err := s.ListComposeFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	r, c, err := s.connect(id)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for i := range files {
		p, e := s.composePublishPlan(ctx, r, c, files[i].ID)
		if e != nil {
			files[i].Status = "unknown"
			files[i].Error = e.Error()
			continue
		}
		files[i].Revision = p.contentRevision
		files[i].Project = p.Project
		files[i].Status = "published"
		if p.Changed {
			files[i].Status = "unpublished"
		} else {
			files[i].AppliedRevision = p.contentRevision
		}
	}
	return files, nil
}

// Version releases update the same complete business Compose and publication record.
// RecordBusinessPublication 核验业务 Compose 已整体应用并写入发布状态。
// services 非空时只核验列出的服务（软件发布只重建了这些服务，其余服务
// 不因发布而重启或校验）；为空时核验全部服务（整栈重建后的调用方）。
func (s *Service) RecordBusinessPublication(ctx context.Context, id string, services ...string) error {
	r, c, err := s.connect(id)
	if err != nil {
		return err
	}
	defer r.Close()
	file := path.Base(c.ComposeFile)
	if c.Deployment != nil {
		file = "business"
	}
	p, err := s.composePublishPlan(ctx, r, c, file)
	if err != nil {
		return err
	}
	scope := p.Services
	if len(services) > 0 {
		scope = services
	}
	if len(scope) == 0 {
		return fmt.Errorf("没有需要核验的服务")
	}
	m := domain.ComposeModule{ID: file, Project: p.Project, Timeout: 600}
	for _, name := range p.Services {
		if strings.HasSuffix(name, "-init") {
			m.InitServices = append(m.InitServices, name)
		}
	}
	if err = modular.Wait(ctx, r, m, scope, nil); err != nil {
		return err
	}
	hashes, err := runSSH(ctx, r.SSH, p.base+" config --hash '*'", "")
	if err != nil {
		return err
	}
	containers, err := r.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}
	if !hashesMatch(filterHashLines(hashes, scope), p.Project, containers) {
		return fmt.Errorf("业务 Compose 尚未整体应用，保留未发布状态")
	}
	return writePublishedState(ctx, r, p.File, composePublishedState{Content: p.contentRevision, Resolved: p.resolvedRevision, Files: p.filesRevision})
}

// filterHashLines 只保留 scope 内服务的 `docker compose config --hash` 输出行。
func filterHashLines(output string, scope []string) string {
	allowed := make(map[string]bool, len(scope))
	for _, name := range scope {
		allowed[name] = true
	}
	kept := []string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && allowed[fields[0]] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func publicationChanged(state composePublishedState, content, resolved string) bool {
	return state.Failed || state.Content != content || state.Resolved != resolved
}
