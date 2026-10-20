package api

import (
	"center-cv-manager/internal/domain"
	"testing"
	"time"
)

func TestInitializationRequiresUpfrontVersionsAndUninitializedCenter(t *testing.T) {
	// 业务镜像必选；模型可选（不选则停在等待业务发布）。
	if validateInitialization(domain.Center{}, 0) == nil {
		t.Fatal("accepted missing software versions")
	}
	if err := validateInitialization(domain.Center{}, 1); err != nil {
		t.Fatal("software-only initialization should be allowed", err)
	}
	for _, status := range []string{"reset", "waiting_business", "failed", "preparing"} {
		c := domain.Center{Deployment: &domain.Deployment{Status: status}}
		if err := validateInitialization(c, 1); err != nil {
			t.Fatal(status, err)
		}
		now := time.Now()
		c.InitializedAt = &now
		if validateInitialization(c, 1) == nil {
			t.Fatal("allowed reinitializing previously initialized center", status)
		}
	}
	if validateInitialization(domain.Center{Deployment: &domain.Deployment{Status: "ready"}}, 1) == nil {
		t.Fatal("accepted ready center")
	}
}
