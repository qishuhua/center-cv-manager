package templates

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAllComposeTemplatesRuntimeContracts(t *testing.T) {
	err := fs.WalkDir(templateFiles, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), "compose") || !strings.HasSuffix(path, ".yml") {
			return nil
		}
		content, err := fs.ReadFile(templateFiles, path)
		if err != nil {
			return err
		}
		t.Run(path, func(t *testing.T) {
			if err := validateGeneratedCompose(content); err != nil {
				t.Fatal(err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"business", "tensorrt", "denglin"} {
		if err := Render(mode, t.TempDir(), RenderOptions{Workers: 2}, nil); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	for _, platform := range []string{"nvidia", "denglin"} {
		if _, err := RenderModules(platform, RenderOptions{Workers: 2}); err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
	}
}

func TestGeneratedComposeRejectsStartupTraps(t *testing.T) {
	for name, content := range map[string]string{
		"missing node name":               "services:\n  rabbitmq:\n    network_mode: host\n",
		"DNS dependent node name":         "services:\n  rabbitmq:\n    network_mode: host\n    environment: {RABBITMQ_NODENAME: rabbit@rabbitmq}\n",
		"host port mapping":               "services:\n  api:\n    network_mode: host\n    ports: ['8080:80']\n",
		"cross compose dependency":        "services:\n  api:\n    depends_on: [rabbitmq]\n",
		"missing dependency healthcheck":  "services:\n  api:\n    depends_on:\n      db: {condition: service_healthy}\n  db: {}\n",
		"disabled dependency healthcheck": "services:\n  api:\n    depends_on:\n      db: {condition: service_healthy}\n  db:\n    healthcheck: {test: [NONE]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if validateGeneratedCompose([]byte(content)) == nil {
				t.Fatal("unsafe template accepted")
			}
		})
	}
}
