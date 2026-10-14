package provision

import (
	"center-cv-manager/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigurationPathsExcludeRuntimeData(t *testing.T) {
	c := domain.Center{Deployment: &domain.Deployment{ModelsRoot: "/srv/models", LogsRoot: "/srv/logs", MediaRoot: "/srv/media"}}
	roots, excluded, err := configurationPaths(c, "/srv/deploy/monitoring/compose.yml", `services:
  prometheus:
    volumes:
      - {type: bind, source: /srv/deploy/monitoring/rules, target: /etc/prometheus/rules, read_only: true}
      - {type: bind, source: /srv/deploy/monitoring/data, target: /prometheus}
      - {type: bind, source: /srv/models, target: /data/models, read_only: true}
      - {type: bind, source: /proc, target: /host/proc, read_only: true}
      - {type: bind, source: /var/lib/docker, target: /var/lib/docker, read_only: true}
      - {type: bind, source: /srv/docker-runtime, target: /var/lib/docker, read_only: true}
      - {type: bind, source: /srv/logs, target: /service-logs, read_only: true}
configs:
  custom: {file: /etc/custom-app/config.yml}
secrets:
  password: {file: ./secrets/password}
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/srv/deploy/monitoring", "/srv/deploy/monitoring/rules", "/etc/custom-app/config.yml", "/srv/deploy/monitoring/secrets/password"} {
		if !containsString(roots, p) {
			t.Fatal("missing config", p)
		}
	}
	for _, p := range []string{"/srv/models", "/srv/logs", "/proc", "/var/lib/docker", "/srv/docker-runtime", "/srv/deploy/monitoring/data"} {
		if containsString(roots, p) || !containsString(excluded, p) {
			t.Fatal("runtime data tracked", p)
		}
	}
}

func TestConfigurationFingerprintDetectsSSHFileEdits(t *testing.T) {
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(bash); err != nil {
			t.Skip("Git Bash required for Linux configuration fingerprint command")
		}
	}
	root := t.TempDir()
	shellRoot := root
	if runtime.GOOS == "windows" {
		out, err := exec.Command(bash, "-c", "cygpath -u \"$1\"", "convert", root).CombinedOutput()
		if err != nil {
			t.Fatalf("cygpath: %s %v", out, err)
		}
		shellRoot = strings.TrimSpace(string(out))
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		cmd := configurationHashCommand([]string{shellRoot}, []string{shellRoot + "/data"})
		out, err := exec.Command(bash, "-c", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("hash command: %v\n%s", err, out)
		}
		return string(out)
	}
	write("compose.yml", "services: unchanged")
	write("rules/test alert.yml", "threshold: 1")
	write("module.env", "PORT=8080")
	initial := snapshot()
	write("rules/test alert.yml", "threshold: 2")
	changed := snapshot()
	if changed == initial {
		t.Fatal("same-size SSH rule edit was missed")
	}
	write("data/runtime.log", "variable data")
	write(".center-manager/published/state.json", "record")
	write("compose.yml.bak.123", "old version")
	if snapshot() != changed {
		t.Fatal("runtime data or publication record caused false drift")
	}
	write("rules/new.yml", "new rule")
	added := snapshot()
	if added == changed {
		t.Fatal("added rule missed")
	}
	if err := os.Remove(filepath.Join(root, "rules", "new.yml")); err != nil {
		t.Fatal(err)
	}
	if snapshot() != changed {
		t.Fatal("deleted rule missed")
	}
	write("module.env", "PORT=8090")
	if snapshot() == changed {
		t.Fatal("module.env change missed")
	}
}
