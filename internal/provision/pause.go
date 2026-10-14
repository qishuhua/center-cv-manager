package provision

import (
	"context"
	"fmt"
	"slices"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	containertypes "github.com/docker/docker/api/types/container"
)

// PauseCenter stops every managed Compose project on the center with
// `docker compose down`: containers and networks are removed while images,
// volumes, configuration and model files stay untouched. The deployment is
// marked paused so the UI can offer ResumeCenter to bring the modules back.
func (s *Service) PauseCenter(ctx context.Context, id string, r Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		remote, c, err := s.connect(id)
		if err != nil {
			return err
		}
		defer remote.Close()
		var targets []domain.ComposeModule
		if c.Deployment != nil {
			if c.Deployment.Status == "reset" {
				return fmt.Errorf("中心已重置，没有可暂停的部署")
			}
			order, e := modular.Order(c.Deployment.Modules)
			if e != nil {
				return e
			}
			targets = order
		} else {
			if targets, err = s.ListComposeFiles(ctx, id); err != nil {
				return err
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("没有找到可暂停的 Compose 配置")
		}
		r.Message(fmt.Sprintf("正在暂停中心：按依赖反序停止 %d 个 Compose 项目，数据卷、镜像与配置保留", len(targets)))
		for i, m := range slices.Backward(targets) {
			if _, err = runSSH(ctx, remote.SSH, modular.Command(m)+" down", ""); err != nil {
				return fmt.Errorf("停止模块 %s 失败: %w", m.Name, err)
			}
			r.Logf("已停止并移除模块 %s 的容器（%s）", m.Name, m.Project)
			r.Progress(float64(len(targets)-i) * 90 / float64(len(targets)))
		}
		if c.Deployment != nil {
			projects := map[string]bool{}
			for _, m := range c.Deployment.Modules {
				projects[m.Project] = true
			}
			remaining, e := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
			if e != nil {
				return e
			}
			if len(managedResetContainers(remaining, projects)) > 0 {
				return fmt.Errorf("仍有受管容器未停止，请检查任务日志后重试暂停")
			}
			for i := range c.Deployment.Modules {
				c.Deployment.Modules[i].Status = "stopped"
				c.Deployment.Modules[i].Error = ""
			}
			c.Deployment.Status = "paused"
			if err = s.centers.UpdateDeployment(id, c.Deployment); err != nil {
				return err
			}
		}
		r.Progress(100)
		r.Message("暂停完成：全部受管服务已停止、容器已移除；数据卷、镜像与配置保留，可随时恢复")
		return nil
	})
}

// ResumeCenter brings a paused center back online. Modular centers reuse the
// ordered startup of ResumeModules (dependency order with readiness waits);
// legacy centers run `up -d` for every Compose file in the deploy directory.
func (s *Service) ResumeCenter(ctx context.Context, id string, r Reporter) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		_, _, c, err := s.centers.Connection(id)
		if err != nil {
			return err
		}
		if c.Deployment != nil {
			// ResumeModules re-acquires the same center lock (re-entrant) and
			// applies each module in dependency order, waiting for readiness.
			return s.ResumeModules(ctx, id, r)
		}
		remote, _, err := s.connect(id)
		if err != nil {
			return err
		}
		defer remote.Close()
		files, err := s.ListComposeFiles(ctx, id)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("没有找到可恢复的 Compose 配置")
		}
		r.Message(fmt.Sprintf("正在恢复中心：启动 %d 个 Compose 项目", len(files)))
		for i, m := range files {
			if _, err = runSSH(ctx, remote.SSH, modular.Command(m)+" up -d", ""); err != nil {
				return fmt.Errorf("启动 %s 失败: %w", m.ComposeFile, err)
			}
			r.Logf("已启动 %s", m.ComposeFile)
			r.Progress(float64(i+1) * 100 / float64(len(files)))
		}
		r.Message("恢复完成：全部 Compose 项目已启动")
		return nil
	})
}
