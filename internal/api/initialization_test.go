package api

import (
	"center-cv-manager/internal/domain"
	"testing"
	"time"
)

func TestInitializationRequiresUpfrontVersionsAndUninitializedCenter(t *testing.T) {
	for _, counts := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
		if validateInitialization(domain.Center{}, counts[0], counts[1]) == nil {
			t.Fatal("accepted missing versions", counts)
		}
	}
	for _, status := range []string{"reset", "waiting_business", "failed", "preparing"} {
		c := domain.Center{Deployment: &domain.Deployment{Status: status}}
		if err := validateInitialization(c, 1, 1); err != nil {
			t.Fatal(status, err)
		}
		now := time.Now()
		c.InitializedAt = &now
		if validateInitialization(c, 1, 1) == nil {
			t.Fatal("allowed reinitializing previously initialized center", status)
		}
	}
	if validateInitialization(domain.Center{Deployment: &domain.Deployment{Status: "ready"}}, 1, 1) == nil {
		t.Fatal("accepted ready center")
	}
}
