package provision

import (
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
	"gopkg.in/yaml.v3"
)

func TestMigrationPreservesVolumesImagesAndUnknownServices(t *testing.T) {
	plan, err := SplitLegacy(domain.Center{ComposeFile: "/opt/center/deploy/docker-compose.yml"}, `name: old-center
services:
  rabbitmq:
    image: rabbitmq:old
    network_mode: host
    volumes:
      - type: volume
        source: rabbit-data
        target: /var/lib/rabbitmq
  rabbitmq-init:
    image: rabbitmq:old
    depends_on:
      rabbitmq:
        condition: service_healthy
  center-cv-service-0:
    image: custom/worker:exact-commit
    depends_on:
      rabbitmq-init:
        condition: service_completed_successfully
    volumes:
      - type: bind
        source: /data/models
        target: /data/models
        read_only: true
  custom-processor:
    image: custom/processor:1
volumes:
  rabbit-data:
    name: actual-old-volume
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deployment.Modules) != 2 || plan.Deployment.ModelsRoot != "/data/models" {
		t.Fatal(plan.Deployment)
	}
	if !strings.Contains(plan.Files["infra"], "external: true") || !strings.Contains(plan.Files["infra"], "name: actual-old-volume") {
		t.Fatal(plan.Files["infra"])
	}
	var business struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err = yaml.Unmarshal([]byte(plan.Files["business"]), &business); err != nil {
		t.Fatal(err)
	}
	if business.Services["custom-processor"]["image"] != "custom/processor:1" || business.Services["center-cv-service-0"]["image"] != "custom/worker:exact-commit" {
		t.Fatal(business)
	}
	if strings.Contains(plan.Files["business"], "rabbitmq-init:") {
		t.Fatal("cross-file dependency retained")
	}
	if len(plan.Deployment.Modules[1].DependsOn) != 1 || plan.Deployment.Modules[1].DependsOn[0] != "infra" {
		t.Fatal("dependency barrier missing")
	}
}

func TestMigrationRejectsUnresolvedVolume(t *testing.T) {
	_, err := SplitLegacy(domain.Center{ComposeFile: "/home/cv/deploy/docker-compose.yml"}, "name: old\nservices:\n  rabbitmq:\n    image: rabbitmq\n    volumes:\n      - type: volume\n        source: data\n        target: /data\n")
	if err == nil {
		t.Fatal("unresolved volume accepted")
	}
}

func TestModuleFileTraversal(t *testing.T) {
	m := domain.ComposeModule{ComposeFile: "/deploy/business/compose.yml"}
	for _, file := range []string{"../infra/compose.yml", "/etc/passwd", "a/../../secret", "a\\secret", ".history/a", "a\nfile"} {
		if _, err := moduleFile(m, file); err == nil {
			t.Fatalf("accepted %q", file)
		}
	}
	if got, err := moduleFile(m, "config/app.yml"); err != nil || got != "/deploy/business/config/app.yml" {
		t.Fatal(got, err)
	}
}
