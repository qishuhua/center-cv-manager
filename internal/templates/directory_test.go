package templates

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryResources(t *testing.T) {
	previous := templateFiles
	t.Cleanup(func() { templateFiles = previous })
	root := t.TempDir()
	if err := SetDirectory(root); err == nil {
		t.Fatal("expected incomplete template directory to be rejected")
	}
	for _, name := range []string{"compose", "monitoring", "secrets", "modules"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "compose", "example.yml")
	for _, content := range []string{"services: {}", "services: {updated: {}}"} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(templateFiles, "compose/example.yml")
		if err != nil || string(got) != content {
			t.Fatalf("disk resource = %q, %v; want %q", got, err, content)
		}
	}
}
