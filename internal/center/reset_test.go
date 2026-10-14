package center

import (
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"testing"
	"time"
)

type resetRepository struct{ value domain.Center }

func (r *resetRepository) ListCenters() ([]domain.Center, error) {
	return []domain.Center{r.value}, nil
}
func (r *resetRepository) GetCenter(string) (domain.Center, error) { return r.value, nil }
func (r *resetRepository) SaveCenter(c domain.Center) error        { r.value = c; return nil }
func (r *resetRepository) DeleteCenter(string) error               { return nil }

func TestResetRuntimeRequiresInitializationAndKeepsConnection(t *testing.T) {
	now := time.Now()
	repo := &resetRepository{domain.Center{ID: "c1", Host: "host", Password: "encrypted", ComposeFile: "/srv/deploy/docker-compose.yml", ComposeContent: "old", ComposeProject: "old", InitializedAt: &now}}
	next := modular.NewDeployment(repo.value.ComposeFile, 2)
	next.Status = "reset"
	next.ModelsRoot = "/srv/custom-models"
	s := &Service{repo: repo}
	if err := s.ResetRuntime("c1", next); err != nil {
		t.Fatal(err)
	}
	got := repo.value
	if !got.Deployment.ReselectionRequired {
		t.Fatal("reset must require explicit version reselection")
	}
	if got.InitializedAt != nil || got.ComposeContent != "" || got.ComposeProject != "" {
		t.Fatal("retained initialized state")
	}
	if got.Host != "host" || got.Password != "encrypted" || got.Deployment.ModelsRoot != "/srv/custom-models" || got.Deployment.Status != "reset" {
		t.Fatal("lost connection or preserved paths")
	}
	for _, m := range got.Deployment.Modules {
		if m.Status != "pending" || m.AppliedRevision != "" {
			t.Fatal("retained module publication state")
		}
	}
}
