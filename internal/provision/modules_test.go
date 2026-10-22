package provision

import (
	"testing"

	"center-cv-manager/internal/domain"
)

func TestAllEnabledModules(t *testing.T) {
	base := &domain.Deployment{Status: "ready", Modules: []domain.ComposeModule{
		{ID: "infra", Enabled: true, Status: "stopped"},
		{ID: "business", Enabled: true, Status: "stopped"},
		{ID: "monitoring", Enabled: false, Status: "healthy"},
	}}
	if !allEnabledModules(base, "stopped") {
		t.Fatal("禁用模块不参与判断：全部启用模块 stopped 应为 true")
	}
	if allEnabledModules(base, "healthy") {
		t.Fatal("存在 stopped 启用模块时 healthy 应为 false")
	}
	base.Modules[1].Status = "healthy"
	if allEnabledModules(base, "stopped") || allEnabledModules(base, "healthy") {
		t.Fatal("模块状态混杂时两个聚合判断都应为 false")
	}
	// 从 paused 中心逐模块恢复：最后一个模块 healthy 后聚合为 ready 的判定输入。
	resumed := &domain.Deployment{Status: "paused", Modules: []domain.ComposeModule{
		{ID: "infra", Enabled: true, Status: "healthy"},
		{ID: "business", Enabled: true, Status: "healthy"},
	}}
	if !allEnabledModules(resumed, "healthy") {
		t.Fatal("全部启用模块 healthy 应为 true")
	}
}
