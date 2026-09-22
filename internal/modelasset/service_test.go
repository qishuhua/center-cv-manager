package modelasset

import (
	"strings"
	"testing"
)

func TestSafeRelativePath(t *testing.T) {
	for _, value := range []string{"detector/model.onnx", "weights.bin", "nested/v1/config.json"} {
		if !safeRelativePath(value) {
			t.Errorf("expected safe model path: %q", value)
		}
	}
	for _, value := range []string{"", "/etc/passwd", "../secret", "dir/../../secret", "dir\\model.bin"} {
		if safeRelativePath(value) {
			t.Errorf("expected unsafe model path: %q", value)
		}
	}
}

func TestSelectObjectsRejectsUnknownKeysAndDeduplicates(t *testing.T) {
	catalog := []Object{{Key: "center-models/20260810/a.bin", Name: "20260810/a.bin"}}
	selected, err := selectObjects(catalog, []string{"center-models/20260810/a.bin", "center-models/20260810/a.bin"})
	if err != nil || len(selected) != 1 {
		t.Fatalf("unexpected selection: %#v, %v", selected, err)
	}
	if _, err = selectObjects(catalog, []string{"center-models/20260810/missing.bin"}); err == nil {
		t.Fatal("unknown OSS key must be rejected")
	}
}

func TestDownloadCommandSupportsResumeAndQuotesURL(t *testing.T) {
	command := downloadCommand("/data/models/a file.part", "https://oss.example/a?signature=secret")
	for _, expected := range []string{"curl -fsSL", "-C -", "wget -c", "'/data/models/a file.part'", "'https://oss.example/a?signature=secret'"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("download command should contain %q: %s", expected, command)
		}
	}
}
