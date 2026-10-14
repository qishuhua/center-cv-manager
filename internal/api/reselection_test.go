package api

import (
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/domain"
	"testing"
)

func TestResetRequiresInitializationAndBothVersionSelections(t *testing.T) {
	c := domain.Center{Deployment: &domain.Deployment{Status: "reset", ReselectionRequired: true}}
	if validateReselection(c, 1, 1) == nil {
		t.Fatal("allowed release before initialization")
	}
	c.Deployment.Status = "waiting_business"
	for _, counts := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
		if validateReselection(c, counts[0], counts[1]) == nil {
			t.Fatalf("accepted implicit reuse %v", counts)
		}
	}
	if err := validateReselection(c, 1, 1); err != nil {
		t.Fatal(err)
	}
	c.Deployment.ReselectionRequired = false
	if err := validateReselection(c, 0, 1); err != nil {
		t.Fatal("blocked daily model-only release", err)
	}
}

func TestRebuildDoesNotSilentlyReuseUnselectedBusinessImages(t *testing.T) {
	c := domain.Center{Deployment: &domain.Deployment{ReselectionRequired: true}}
	check := deploy.CheckResult{Modules: map[string]deploy.CheckResult{"business": {BaseServices: []string{"worker", "gateway"}, ServiceMappings: map[string][]string{"source-worker": {"worker"}}}}}
	if validateRebuildSoftware(c, check) == nil {
		t.Fatal("accepted omitted gateway image")
	}
	check.Modules["business"].ServiceMappings["source-gateway"] = []string{"gateway"}
	if err := validateRebuildSoftware(c, check); err != nil {
		t.Fatal(err)
	}
	c.Deployment.ReselectionRequired = false
	if err := validateRebuildSoftware(c, deploy.CheckResult{}); err != nil {
		t.Fatal("daily partial release blocked")
	}
}
