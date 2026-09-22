package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
)

func TestCheckReleaseCentersConcurrently(t *testing.T) {
	centers := []domain.Center{{ID: "center-1"}, {ID: "center-2"}, {ID: "center-3"}}
	started := make(chan string, len(centers))
	release := make(chan struct{})
	done := make(chan []releaseCenterPlan, 1)

	go func() {
		done <- checkReleaseCentersConcurrently(context.Background(), centers, func(_ context.Context, centerID string) releaseCenterPlan {
			started <- centerID
			<-release
			return releaseCenterPlan{Error: centerID}
		})
	}()

	seen := make(map[string]bool, len(centers))
	for range centers {
		select {
		case centerID := <-started:
			seen[centerID] = true
		case <-time.After(time.Second):
			t.Fatal("中心复检未并发启动")
		}
	}
	close(release)

	var plans []releaseCenterPlan
	select {
	case plans = <-done:
	case <-time.After(time.Second):
		t.Fatal("并发复检未完成")
	}
	if len(seen) != len(centers) {
		t.Fatalf("启动中心数 = %d, want %d", len(seen), len(centers))
	}
	for index, center := range centers {
		if plans[index].Error != center.ID {
			t.Fatalf("plans[%d].Error = %q, want %q", index, plans[index].Error, center.ID)
		}
	}
}

func TestReadyReleaseCentersSkipsBlocked(t *testing.T) {
	centers := []domain.Center{{ID: "center-1", Name: "中心一"}, {ID: "center-2", Name: "中心二"}, {ID: "center-3", Name: "中心三"}}
	model := modelasset.ReleaseItem{Model: "detector", Version: "v1"}
	plans := []releaseCenterPlan{
		{Ready: true, ModelPlan: modelasset.ReleasePlan{Targets: []modelasset.ReleaseItem{model}}},
		{Ready: false, Error: "SSH 连接失败"},
		{Ready: true},
	}

	ready, resolved, blocked := readyReleaseCenters(centers, plans)
	if len(ready) != 2 || ready[0].ID != "center-1" || ready[1].ID != "center-3" {
		t.Fatalf("ready = %#v", ready)
	}
	if len(resolved["center-1"]) != 1 || resolved["center-1"][0].Model != model.Model {
		t.Fatalf("resolved center-1 = %#v", resolved["center-1"])
	}
	if len(blocked) != 1 || blocked[0] != "中心二：SSH 连接失败" {
		t.Fatalf("blocked = %#v", blocked)
	}
}

func TestCurrentModelsReady(t *testing.T) {
	tests := []struct {
		name    string
		current []modelasset.CurrentVersion
		wantErr string
	}{
		{name: "missing", wantErr: "尚未部署模型"},
		{name: "unhealthy", current: []modelasset.CurrentVersion{{Model: "detector", Healthy: false, HealthError: "文件缺失"}}, wantErr: "detector（文件缺失）"},
		{name: "healthy", current: []modelasset.CurrentVersion{{Model: "detector", Healthy: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := currentModelsReady(tt.current)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("currentModelsReady() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("currentModelsReady() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
