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
	return ModulePlan{ModuleID: moduleID, Action: action, Services: services, Revision: modular.PlanRevision(m, raw)}, nil
}
