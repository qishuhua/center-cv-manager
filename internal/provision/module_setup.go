package provision

import (
	"center-cv-manager/internal/modular"
	"context"
	"fmt"
	containertypes "github.com/docker/docker/api/types/container"
	"path"
	"strings"
)

type ModuleSetup struct {
	Root       string `json:"root"`
	ModelsRoot string `json:"models_root"`
	LogsRoot   string `json:"logs_root"`
	MediaRoot  string `json:"media_root"`
}

func (in ModuleSetup) Validate() error {
	for _, p := range []string{in.Root, in.ModelsRoot, in.LogsRoot, in.MediaRoot} {
		if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\r\n") {
			return fmt.Errorf("部署和数据目录必须为规范的绝对路径")
		}
	}
	return nil
}

// Caller holds the center lock. Prepare configuration without starting containers.
func (s *Service) PrepareModuleFiles(ctx context.Context, id string, in ModuleSetup, r Reporter) error {
	if err := in.Validate(); err != nil {
		return err
	}
	remote, c, err := s.connect(id)
	if err != nil {
		return err
	}
	defer remote.Close()
	if c.Deployment != nil {
		return fmt.Errorf("中心已有模块配置，请在列表中编辑或继续初始化")
	}
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}
	if len(containers) > 0 {
		return fmt.Errorf("中心仍有容器，请使用迁移流程以保留项目和数据卷归属")
	}
	if c.ComposeFile != "" {
		if _, err = runSSH(ctx, remote.SSH, "test ! -e "+shellQuote(c.ComposeFile), ""); err != nil {
			return fmt.Errorf("原 Compose 仍存在，请使用迁移流程")
		}
	}
	out, err := runSSH(ctx, remote.SSH, "if [ -e "+shellQuote(in.Root)+" ]; then find "+shellQuote(in.Root)+" -mindepth 1 -maxdepth 1 -print -quit; fi", "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("部署目录非空，请迁移原配置或选择空目录")
	}
	d := modular.NewDeployment(path.Join(in.Root, "docker-compose.yml"), max(1, c.GPUCount))
	d.Status = "configured"
	d.ModelsRoot, d.LogsRoot, d.MediaRoot = in.ModelsRoot, in.LogsRoot, in.MediaRoot
	if err = modular.Validate(d); err != nil {
		return err
	}
	if err = s.centers.UpdateRuntime(id, c.DetectedAccelerator, c.CPUCount, c.GPUCount, path.Join(in.Root, "docker-compose.yml"), ""); err != nil {
		return err
	}
	if err = s.centers.UpdateDeployment(id, d); err != nil {
		return err
	}
	r.Log("已登记五个独立 Compose；仅生成配置，不启动服务")
	return s.Initialize(ctx, id, r)
}
