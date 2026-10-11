package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
)

type moduleResolver struct {
	CenterResolver
	module domain.ComposeModule
}

func (r moduleResolver) Connection(id string) (sshx.Config, string, domain.Center, error) {
	cfg, socket, c, err := r.CenterResolver.Connection(id)
	c.Deployment = nil
	c.ComposeFile = r.module.ComposeFile
	c.ComposeProject = r.module.Project
	return cfg, socket, c, err
}
func (r moduleResolver) RememberHostKey(c domain.Center, fp string) error {
	_, _, original, err := r.CenterResolver.Connection(c.ID)
	if err != nil {
		return err
	}
	return r.CenterResolver.RememberHostKey(original, fp)
}
func (r moduleResolver) UpdateRuntime(id, accelerator string, cpus, gpus int, file, content string) error {
	_, _, c, err := r.CenterResolver.Connection(id)
	if err != nil {
		return err
	}
	if updater, ok := r.CenterResolver.(interface {
		UpdateDeployment(string, *domain.Deployment) error
	}); ok && c.Deployment != nil {
		for i := range c.Deployment.Modules {
			if c.Deployment.Modules[i].ID == r.module.ID {
				c.Deployment.Modules[i].Revision = modular.Hash(content)
				c.Deployment.Modules[i].Status = "prepared"
				c.Deployment.Modules[i].UpdatedAt = time.Now()
			}
		}
		return updater.UpdateDeployment(id, c.Deployment)
	}
	return fmt.Errorf("中心不支持模块状态持久化")
}

func (s *Service) checkModules(ctx context.Context, id string, c domain.Center, items []Item) CheckResult {
	result := CheckResult{Images: map[string]string{}, ServiceMappings: map[string][]string{}, Modules: map[string]CheckResult{}, MissingImages: []string{}, MissingServices: []string{}}
	for _, m := range c.Deployment.Modules {
		if !m.Enabled || m.Status == "pending" {
			continue
		}
		check := New(moduleResolver{s.centers, m}).Check(ctx, id, items)
		if check.Error != "" {
			result.Error = fmt.Sprintf("模块 %s: %s", m.Name, check.Error)
			return result
		}
		result.Modules[m.ID] = check
		for key, image := range check.Images {
			result.Images[key] = image
		}
		for key, names := range check.ServiceMappings {
			for _, name := range names {
				result.ServiceMappings[key] = append(result.ServiceMappings[key], m.ID+"/"+name)
			}
		}
	}
	for _, item := range items {
		if len(result.ServiceMappings[item.SourceID]) == 0 {
			result.MissingServices = append(result.MissingServices, item.SourceName+" 未映射到任何已启用模块")
		}
		if result.Images[item.SourceID] == "" {
			result.MissingImages = append(result.MissingImages, item.SourceName+":"+item.Ref)
		}
	}
	result.Ready = result.Error == "" && len(result.MissingImages) == 0 && len(result.MissingServices) == 0
	return result
}

func (s *Service) runModules(ctx context.Context, id string, c domain.Center, items []Item, r Reporter) error {
	check := s.Check(ctx, id, items)
	if !check.Ready {
		return fmt.Errorf("发布预检失败: %s", checkSummary(check))
	}
	order, err := modular.Order(c.Deployment.Modules)
	if err != nil {
		return err
	}
	cfg, socket, _, err := s.centers.Connection(id)
	if err != nil {
		return err
	}
	remote, fp, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(c, fp)
	for _, m := range order {
		sub := check.Modules[m.ID]
		selected := []Item{}
		targets := []string{}
		for _, item := range items {
			if names := sub.ServiceMappings[item.SourceID]; len(names) > 0 {
				selected = append(selected, item)
				targets = append(targets, names...)
			}
		}
		if len(selected) == 0 {
			continue
		}
		for _, depID := range m.DependsOn {
			dep, e := modular.Find(c, depID)
			if e != nil {
				return e
			}
			raw, e := modular.Run(ctx, remote.SSH, modular.Command(dep)+" config", "")
			if e != nil {
				return e
			}
			parsed, e := modular.Parse(raw)
			if e != nil {
				return e
			}
			if e = modular.Inspect(ctx, remote, dep, modular.ServiceNames(parsed)); e != nil {
				return fmt.Errorf("先初始化依赖 %s: %w", depID, e)
			}
		}
		r.Logf("只更新模块 %s 的服务：%s", m.Name, strings.Join(targets, ", "))
		if err = New(moduleResolver{s.centers, m}).runLocked(ctx, id, selected, r); err != nil {
			_ = s.recordModuleResult(id, m.ID, "failed", err.Error())
			return err
		}
		if err = modular.Wait(ctx, remote, m, uniqueSorted(targets), modular.ReadyURLsForServices(m, targets)); err != nil {
			_ = s.recordModuleResult(id, m.ID, "failed", err.Error())
			return err
		}
		status := "healthy"
		if cfg, e := modular.Parse(sub.ComposeConfig); e != nil {
			return e
		} else if e = modular.Inspect(ctx, remote, m, modular.ServiceNames(cfg)); e != nil {
			status = "partial"
		}
		if err = s.recordModuleResult(id, m.ID, status, ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordModuleResult(id, moduleID, status, message string) error {
	_, _, c, err := s.centers.Connection(id)
	if err != nil {
		return err
	}
	updater, ok := s.centers.(interface {
		UpdateDeployment(string, *domain.Deployment) error
	})
	if !ok || c.Deployment == nil {
		return fmt.Errorf("无法保存模块状态")
	}
	for i := range c.Deployment.Modules {
		if c.Deployment.Modules[i].ID == moduleID {
			c.Deployment.Modules[i].Status = status
			c.Deployment.Modules[i].Error = message
			c.Deployment.Modules[i].UpdatedAt = time.Now()
		}
	}
	if status == "failed" && c.Deployment.Status == "ready" {
		c.Deployment.Status = "degraded"
	}
	return updater.UpdateDeployment(id, c.Deployment)
}
