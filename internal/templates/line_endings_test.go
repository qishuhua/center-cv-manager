package templates

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderModulesNormalizesWindowsScripts(t *testing.T) {
	original := templateFiles
	dir := t.TempDir()
	err := fs.WalkDir(original, ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, e := fs.ReadFile(original, p)
		if e != nil {
			return e
		}
		if strings.HasSuffix(p, ".sh") {
			b = []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n"))
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	templateFiles = os.DirFS(dir)
	t.Cleanup(func() { templateFiles = original })
	for _, platform := range []string{"nvidia", "denglin"} {
		files, e := RenderModules(platform, RenderOptions{Workers: 1})
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for p, s := range files {
			if strings.HasSuffix(p, ".sh") {
				count++
				if strings.Contains(s, "\r") {
					t.Fatalf("CRLF in %s", p)
				}
			}
		}
		if count == 0 {
			t.Fatal("no scripts rendered")
		}
	}
}
