package modular

import (
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
)

func TestDefaultDeploymentOrderAndPaths(t *testing.T) {
	d := NewDeployment("/home/cv/.local/share/center-manager/deploy/docker-compose.yml", 2)
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	order, err := Order(d.Modules)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"infra", "logging", "business", "gateway", "monitoring"} {
		if order[i].ID != want {
			t.Fatalf("order=%v", order)
		}
	}
	if d.ModelsRoot != "/home/cv/.local/share/center-manager/data/models" {
		t.Fatal(d.ModelsRoot)
	}
	if len(order[2].ModelServices) != 2 || !strings.HasSuffix(order[2].ReadyURLs[0], "/api/readyz") {
		t.Fatal(order[2])
	}
	c := domain.Center{ComposeFile: order[2].ComposeFile, Deployment: d}
	if ModelsRoot(c) != d.ModelsRoot {
		t.Fatal("module path changed model root")
	}
}

func TestDependencyValidation(t *testing.T) {
	for _, kind := range []string{"cycle", "missing", "disabled", "monitoring", "project", "path"} {
		t.Run(kind, func(t *testing.T) {
			d := NewDeployment("/home/cv/deploy/docker-compose.yml", 1)
			switch kind {
			case "cycle":
				d.Modules[0].DependsOn = []string{"business"}
			case "missing":
				d.Modules[2].DependsOn = []string{"absent"}
			case "disabled":
				d.Modules[0].Enabled = false
			case "monitoring":
				d.Modules[2].DependsOn = []string{"monitoring"}
			case "project":
				d.Modules[1].Project = d.Modules[0].Project
			case "path":
				d.Modules[0].ComposeFile = "/etc/passwd"
			}
			if Validate(d) == nil {
				t.Fatal("accepted invalid deployment")
			}
		})
	}
}

func TestIndependentCommands(t *testing.T) {
	d := NewDeployment("/home/deploy user/deploy/docker-compose.yml", 1)
	for _, m := range d.Modules {
		cmd := Command(m)
		if strings.Count(cmd, " -f ") != 1 || !strings.Contains(cmd, " -p '"+m.Project+"'") || !strings.Contains(cmd, "--env-file '") || strings.Contains(cmd, "--remove-orphans") {
			t.Fatal(cmd)
		}
	}
}

func TestLegacyCommandPreservesExistingEnvironment(t *testing.T) {
	cmd := Command(domain.ComposeModule{ComposeFile: "/opt/center/deploy/docker-compose.yml"})
	if strings.Contains(cmd, " -u ") || strings.Contains(cmd, " -p ") || strings.Contains(cmd, "--env-file") {
		t.Fatal("legacy project resolution changed", cmd)
	}
}

func TestCustomModuleAndRegisteredServices(t *testing.T) {
	d := NewDeployment("/home/cv/deploy/docker-compose.yml", 1)
	d.Modules = append(d.Modules, domain.ComposeModule{ID: "recording", Name: "录像", Project: "center-cv-recording", ComposeFile: "/home/cv/deploy/recording/compose.yml", Enabled: true, Timeout: 90, DependsOn: []string{"infra"}})
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	m := d.Modules[2]
	if ValidateConfig(m, "name: wrong\nservices:\n  test:\n    image: test\n") == nil {
		t.Fatal("wrong project accepted")
	}
	if ValidateConfig(m, "services:\n  test:\n    image: test\n") == nil {
		t.Fatal("missing model consumer accepted")
	}
}

func TestPlanRevisionIgnoresFailedRuntimeButTracksConfiguration(t *testing.T) {
	m := NewDeployment("/home/cv/deploy/docker-compose.yml", 1).Modules[0]
	before := PlanRevision(m, "config")
	m.Status = "failed"
	m.Error = "timeout"
	if PlanRevision(m, "config") != before {
		t.Fatal("failed task cannot retry unchanged configuration")
	}
	m.FilesRevision = "new-file"
	if PlanRevision(m, "config") == before {
		t.Fatal("config file change not detected")
	}
}
func TestReadinessOnlyTargetsSelectedWorker(t *testing.T) {
	m := NewDeployment("/home/cv/deploy/docker-compose.yml", 2).Modules[2]
	urls := ReadyURLsForServices(m, []string{"center-cv-service-1"})
	if len(urls) != 1 || urls[0] != "http://127.0.0.1:8182/api/readyz" {
		t.Fatal(urls)
	}
}
