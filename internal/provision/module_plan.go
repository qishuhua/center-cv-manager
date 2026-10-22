package provision

import (
	"context"
	"fmt"

	"center-cv-manager/internal/modular"
)

type ModulePlan struct {
	ModuleID string   `json:"module_id"`
	Action   string   `json:"action"`
	Services []string `json:"services"`
	Revision string   `json:"revision"`
	// Dependents 是 stop 时仍在运行（healthy）且依赖此模块的模块名，供前端
	// 确认框提示"停止后这些模块会异常"；不再据此硬性拦截。
	Dependents []string `json:"dependents,omitempty"`
}

func (s *Service) PlanModule(ctx context.Context, id, moduleID, action string, services []string) (ModulePlan, error) {
	if action != "deploy" && action != "restart" && action != "stop" {
		return ModulePlan{}, fmt.Errorf("不支持的模块操作")
	}
	remote, c, err := s.connect(id)
	if err != nil {
		return ModulePlan{}, err
	}
	defer remote.Close()
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return ModulePlan{}, err
	}
	raw, err := runSSH(ctx, remote.SSH, modular.Command(m)+" config", "")
	if err != nil {
		return ModulePlan{}, err
	}
	cfg, err := modular.Parse(raw)
	if err != nil {
		return ModulePlan{}, err
	}
	if err = modular.ValidateConfig(m, raw); err != nil {
		return ModulePlan{}, err
	}
	if len(services) == 0 {
		services = modular.ServiceNames(cfg)
	}
	for _, n := range services {
		if _, ok := cfg.Services[n]; !ok {
			return ModulePlan{}, fmt.Errorf("服务 %s 不属于模块", n)
		}
	}
	if err = modular.CheckOwnership(ctx, remote, m, cfg); err != nil {
		return ModulePlan{}, err
	}
	dependents := []string{}
	if action == "stop" && c.Deployment != nil {
		for _, other := range c.Deployment.Modules {
			if other.Enabled && other.ID != moduleID && other.Status == "healthy" {
				for _, dep := range other.DependsOn {
					if dep == moduleID {
						dependents = append(dependents, other.Name)
					}
				}
			}
		}
	}
	return ModulePlan{ModuleID: moduleID, Action: action, Services: services, Revision: modular.PlanRevision(m, raw), Dependents: dependents}, nil
}
