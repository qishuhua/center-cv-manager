package provision

import (
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
	"context"
	"os"
	"testing"
	"time"
)

type liveComposeRepo struct{ c domain.Center }

func (r *liveComposeRepo) ListCenters() ([]domain.Center, error)   { return []domain.Center{r.c}, nil }
func (r *liveComposeRepo) GetCenter(string) (domain.Center, error) { return r.c, nil }
func (r *liveComposeRepo) SaveCenter(c domain.Center) error        { r.c = c; return nil }
func (r *liveComposeRepo) DeleteCenter(string) error               { return nil }
func TestLiveComposeListRead(t *testing.T) {
	host := os.Getenv("COMPOSE_TEST_HOST")
	if host == "" {
		t.Skip("optional read-only SSH integration")
	}
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	password, err := cipher.Encrypt(os.Getenv("COMPOSE_TEST_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &liveComposeRepo{domain.Center{ID: "test", Host: host, Port: 22, Username: os.Getenv("COMPOSE_TEST_USER"), Password: password, DockerSocket: "/var/run/docker.sock", ComposeFile: os.Getenv("COMPOSE_TEST_FILE")}}
	service := New(center.New(repo, cipher), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	files, err := service.ListComposePublications(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("expected multiple Compose files, got %d", len(files))
	}
	for _, file := range files {
		doc, err := service.ReadCompose(ctx, "test", file.ID)
		if err != nil {
			t.Fatal(file.ID, err)
		}
		if doc.Path != file.ComposeFile || doc.Content == "" {
			t.Fatal("incorrect document", file.ID)
		}
		t.Log(file.Name, doc.Path, file.Status, file.Error)
		if file.Status == "unknown" {
			t.Fatal(file.Error)
		}
		plan, err := service.PlanComposePublish(ctx, "test", file.ID)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Project != "deploy" {
			t.Fatal("wrong project", plan.Project)
		}
		t.Log("publish plan", plan.Action, len(plan.Services))
		if file.ID == "docker-compose.yml" && len(plan.Services) != 3 {
			t.Fatal("business compose includes other modules")
		}
		if err := service.PublishCompose(ctx, "test", file.ID, "stale-plan", true, nil); err == nil {
			t.Fatal("accepted stale plan")
		}
		if !plan.Changed {
			if err := service.PublishCompose(ctx, "test", file.ID, plan.Revision, false, nil); err == nil {
				t.Fatal("accepted unconfirmed restart")
			}
		}
	}
}
