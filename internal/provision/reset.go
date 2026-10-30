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
	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
)

// ResetCenter removes deployment configuration and managed Compose containers on this
// center's Docker endpoint. Images, volumes and model files are never deleted.
// Caller confirmation is enforced by the API; the operation has no rollback.
func (s *Service) ResetCenter(ctx context.Context, id string, r Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		remote, c, err := s.connect(id)
		if err != nil {
			return err
		}
		defer remote.Close()
		compose := c.ComposeFile
		if compose == "" {
			compose = centerpath.DefaultComposeFile
		}
		root := path.Dir(compose)
		if c.Deployment != nil {
			root = c.Deployment.Root
		}
		resolved, err := runSSH(ctx, remote.SSH, "realpath -m -- "+shellQuote(root), "")
		if err != nil {
			return err
		}
		root = strings.TrimSpace(resolved)
		if err = validateResetRoot(root); err != nil {
			return err
		}
		next := modular.NewDeployment(path.Join(root, path.Base(compose)), deploymentWorkers(c, max(1, c.GPUCount)))
		next.Status = "reset"
		next.ReselectionRequired = true
		if c.Deployment != nil {
			next.ModelsRoot = c.Deployment.ModelsRoot
			next.LogsRoot = c.Deployment.LogsRoot
			next.MediaRoot = c.Deployment.MediaRoot
		}
		protected := []string{next.ModelsRoot, next.LogsRoot, next.MediaRoot}
		containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
		if err != nil {
			return err
		}
		projects := map[string]bool{}
		if c.Deployment != nil {
			for _, m := range c.Deployment.Modules {
				projects[m.Project] = true
			}
		} else {
			// Compose files (and their env files) may already have been deleted.
			// Docker keeps the original configuration paths on existing containers.
			projects, err = legacyResetProjects(root, path.Base(compose), containers)
			if err != nil {
				return err
			}
			r.Logf("根据容器 Compose 路径标签识别到 %d 个受管项目", len(projects))
		}
		managed := managedResetContainers(containers, projects)
		// Protect actual mounted volume storage and any model mount, including
		// custom locations inside the deployment directory.
		for _, v := range containers {
			info, e := remote.Client.ContainerInspect(ctx, v.ID)
			if errdefs.IsNotFound(e) {
				continue
			}
			if e != nil {
				return e
			}
			for _, m := range info.Mounts {
				if !projects[v.Labels["com.docker.compose.project"]] || m.Type == "volume" || m.Destination == "/data/models" || strings.HasPrefix(m.Destination, "/data/models/") {
					if m.Source != "" {
						protected = append(protected, m.Source)
					}
				}
			}
		}
		for i, p := range protected {
			out, e := runSSH(ctx, remote.SSH, "realpath -m -- "+shellQuote(p), "")
			if e != nil {
				return e
			}
			protected[i] = strings.TrimSpace(out)
		}
		listing, err := runSSH(ctx, remote.SSH, "if [ -d "+shellQuote(root)+" ]; then find "+shellQuote(root)+" -mindepth 1 -maxdepth 1 -printf '%f\\n'; fi", "")
		if err != nil {
			return err
		}
		files := resetConfigPaths(root, path.Base(compose), strings.Split(strings.TrimSpace(listing), "\n"), c.Deployment)
		for _, file := range files {
			out, e := runSSH(ctx, remote.SSH, "realpath -m -- "+shellQuote(file), "")
			if e != nil {
				return e
			}
			if err = validateResetTarget(root, file, strings.TrimSpace(out), protected); err != nil {
				return err
			}
		}
		r.Message("正在重置中心：删除受管 Compose 容器，保留镜像、数据卷及模型文件")
		for i, v := range managed {
			if err = remote.Client.ContainerRemove(ctx, v.ID, containertypes.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("删除容器 %s 失败: %w", v.ID, err)
			}
			r.Logf("已删除容器 %s", v.ID)
			r.Progress(float64(i+1) * 65 / float64(len(managed)))
		}
		for _, file := range files {
			// Recheck resolution immediately before deletion; never follow an escaped
			// path or a symlink into a preserved data directory.
			out, e := runSSH(ctx, remote.SSH, "realpath -m -- "+shellQuote(file), "")
			if e != nil {
				return e
			}
			if err = validateResetTarget(root, file, strings.TrimSpace(out), protected); err != nil {
				return err
			}
			if _, err = runSSH(ctx, remote.SSH, "rm -rf -- "+shellQuote(file), ""); err != nil {
				return fmt.Errorf("删除部署配置 %s 失败: %w", file, err)
			}
			r.Logf("已删除部署配置 %s", file)
		}
		remaining, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
		if err != nil {
			return err
		}
		if len(managedResetContainers(remaining, projects)) > 0 {
			return fmt.Errorf("仍有 %d 个容器，请停止外部自动创建容器的任务后重试重置", len(managedResetContainers(remaining, projects)))
		}
		if err = s.centers.ResetRuntime(id, next); err != nil {
			return err
		}
		r.Progress(90)
		r.Message("容器和部署配置已清理，等待模型版本组解绑")
		return nil
	})
}

// Require every container in a selected project to have matching provenance;
// a project name alone is not evidence that it belongs to this deployment.
func legacyResetProjects(root, primary string, containers []types.Container) (map[string]bool, error) {
	projects := map[string]bool{}
	owned := map[string]bool{}
	for _, c := range containers {
		project := c.Labels["com.docker.compose.project"]
		if project == "" {
			continue
		}
		files := strings.Split(c.Labels["com.docker.compose.project.config_files"], ",")
		matches := true
		for _, file := range files {
			file = strings.TrimSpace(file)
			if !path.IsAbs(file) || path.Dir(file) != root || (path.Base(file) != primary && !composeFilename.MatchString(path.Base(file))) {
				matches = false
			}
		}
		if !matches && c.Labels["com.docker.compose.project.working_dir"] == root {
			return nil, fmt.Errorf("容器 %s 位于部署目录但 Compose 配置路径标签缺失或不匹配，无法安全重置", c.ID)
		}
		owned[c.ID] = matches
		if matches {
			projects[project] = true
		}
	}
	for _, c := range containers {
		if projects[c.Labels["com.docker.compose.project"]] && !owned[c.ID] {
			return nil, fmt.Errorf("Compose 项目 %s 包含部署目录外或来源不明的容器 %s，拒绝重置", c.Labels["com.docker.compose.project"], c.ID)
		}
	}
	return projects, nil
}

func validateResetRoot(root string) error {
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") {
		return fmt.Errorf("无效的部署目录")
	}
	switch root {
	case "/", "/home", "/root", "/opt", "/usr", "/etc", "/var", "/data", "/tmp", "/var/lib", "/var/lib/docker":
		return fmt.Errorf("拒绝重置系统或共享目录 %s", root)
	}
	return nil
}

func validateResetTarget(root, target, resolved string, protected []string) error {
	if path.Dir(target) != root || !strings.HasPrefix(resolved, root+"/") {
		return fmt.Errorf("重置配置路径超出部署目录: %s", target)
	}
	for _, p := range protected {
		if p == resolved || strings.HasPrefix(p, resolved+"/") || strings.HasPrefix(resolved, p+"/") {
			return fmt.Errorf("部署配置 %s 与保留的数据目录重叠，拒绝删除", target)
		}
	}
	return nil
}

func resetConfigPaths(root, primary string, names []string, d *domain.Deployment) []string {
	dirs := map[string]bool{"infra": true, "logging": true, "business": true, "gateway": true, "monitoring": true, "nginx": true, "secrets": true, ".center-manager": true}
	if d != nil {
		for _, m := range d.Modules {
			if path.Dir(path.Dir(m.ComposeFile)) == root {
				dirs[path.Base(path.Dir(m.ComposeFile))] = true
			}
		}
	}
	out := []string{}
	for _, name := range names {
		if name == "" || name == "." || name == ".." || path.Base(name) != name || strings.ContainsAny(name, "\x00\r\n") {
			continue
		}
		base := strings.SplitN(name, ".bak.", 2)[0]
		if dirs[name] || name == primary || name == ".env" || name == "module.env" || composeFilename.MatchString(base) {
			out = append(out, path.Join(root, name))
		}
	}
	sort.Strings(out)
	return out
}

func managedResetContainers(containers []types.Container, projects map[string]bool) []types.Container {
	out := []types.Container{}
	for _, c := range containers {
		project := c.Labels["com.docker.compose.project"]
		if project != "" && projects[project] {
			out = append(out, c)
		}
	}
	return out
}
