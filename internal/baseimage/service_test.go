package baseimage

import (
	"strings"
	"testing"
)

func TestArchivePlatform(t *testing.T) {
	cases := map[string]string{
		"rabbitmq.tar":            "common",
		"shared/prometheus.tgz":   "common",
		"nvidia/runtime.tar.gz":   "nvidia",
		"denglin/runtime-v2.tar":  "denglin",
		"Denglin/Exporter.TAR.GZ": "denglin",
	}
	for input, want := range cases {
		if got := archivePlatform(input); got != want {
			t.Errorf("archivePlatform(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsArchive(t *testing.T) {
	for _, name := range []string{"base.tar", "base.tar.gz", "base.tgz"} {
		if !isArchive(name) {
			t.Errorf("%s should be an archive", name)
		}
	}
	for _, name := range []string{"base.zip", "base.sha256", "readme.txt"} {
		if isArchive(name) {
			t.Errorf("%s should not be an archive", name)
		}
	}
}

func TestDownloadCommandSupportsResumeFallback(t *testing.T) {
	command := downloadCommand("/tmp/base.part", "https://oss.example/object?secret=value")
	for _, expected := range []string{"curl -fsSL", "-C -", "断点续传失败", "wget -c", "rm -f"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("download command should contain %q", expected)
		}
	}
}

func TestSanitizeOutputRemovesSignedURLAndNUL(t *testing.T) {
	secretURL := "https://oss.example/object?signature=secret"
	got := sanitizeOutput("\x00curl: "+secretURL+"\x00 failed\r\n", secretURL)
	if strings.Contains(got, secretURL) || strings.ContainsRune(got, 0) {
		t.Fatalf("sensitive or invalid output was not sanitized: %q", got)
	}
	if !strings.Contains(got, "[OSS_SIGNED_URL]") {
		t.Fatalf("redaction marker missing: %q", got)
	}
	if got = sanitizeOutput("permission denied", ""); got != "permission denied" {
		t.Fatalf("empty secret must not alter output: %q", got)
	}
}

func TestWritableDirectoryCommandQuotesPath(t *testing.T) {
	command := writableDirectoryCommand("/home/deploy user/.cache/center-manager/images")
	for _, expected := range []string{"mkdir -p '/home/deploy user/.cache/center-manager/images'", "test -d", "test -w"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("writable directory command should contain %q: %s", expected, command)
		}
	}
}

func TestArchiveMatchesExactLocalImageVersion(t *testing.T) {
	tags := map[string]string{
		"prom/blackbox-exporter:v0.28.0":                              "prom/blackbox-exporter:v0.28.0",
		"grafana/grafana:13.1.0":                                      "grafana/grafana:13.1.0",
		"registry:5000/team/dl-sdk-docker-image:mr-4.1.2-ubuntu22.04": "registry:5000/team/dl-sdk-docker-image:MR-4.1.2-ubuntu22.04",
	}
	if ref, found := archiveMatchesLocalImage("blackbox-exporter-v0.28.0.tar", tags); !found || ref != "prom/blackbox-exporter:v0.28.0" {
		t.Fatalf("expected exact blackbox match, got %q, %v", ref, found)
	}
	if _, found := archiveMatchesLocalImage("grafana-13.1.1.tar", tags); found {
		t.Fatal("an older image version must not match a newer archive")
	}
	if ref, found := archiveMatchesLocalImage("denglin/dl-sdk-docker-image-MR-4.1.2-ubuntu22.04.tar.gz", tags); !found || !strings.Contains(ref, "MR-4.1.2") {
		t.Fatalf("expected registry image match, got %q, %v", ref, found)
	}
	tags["denglin-gpu-exporter:v0.6.10-202604011052"] = "denglin-gpu-exporter:v0.6.10-202604011052"
	if ref, found := archiveMatchesLocalImage("denglin/denglin-gpu-exporter_v0.6.10-202604011052.tar", tags); !found || ref != "denglin-gpu-exporter:v0.6.10-202604011052" {
		t.Fatalf("expected underscore archive match, got %q, %v", ref, found)
	}
	if _, found := archiveMatchesLocalImage("denglin/denglin-gpu-exporter_v0.6.10.tar", tags); found {
		t.Fatal("partial image version must not match a longer tag")
	}
}

func TestParseLoadedImageRefs(t *testing.T) {
	refs := parseLoadedImageRefs("Loaded image: nginx:1.31.3-alpine\nLoaded image: prom/prometheus:v3.13.1\n")
	if len(refs) != 2 || refs[0] != "nginx:1.31.3-alpine" || refs[1] != "prom/prometheus:v3.13.1" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
	local := map[string]string{"nginx:1.31.3-alpine": "nginx:1.31.3-alpine", "prom/prometheus:v3.13.1": "prom/prometheus:v3.13.1"}
	if !allImageRefsExist(refs, local) {
		t.Fatal("all loaded refs should exist")
	}
	delete(local, "nginx:1.31.3-alpine")
	if allImageRefsExist(refs, local) {
		t.Fatal("missing refs must force a download")
	}
}

func TestLoadedImageIDsDetectSameTagContentChange(t *testing.T) {
	refs := []string{"denglin-gpu-exporter:v1"}
	recorded := map[string]string{"denglin-gpu-exporter:v1": "sha256:old"}
	if !allImageIDsMatch(refs, recorded, map[string]string{"denglin-gpu-exporter:v1": "sha256:old"}) {
		t.Fatal("same image ID should match")
	}
	if allImageIDsMatch(refs, recorded, map[string]string{"denglin-gpu-exporter:v1": "sha256:new"}) {
		t.Fatal("same tag with changed image ID must not match")
	}
}

func TestParseLoadedImageIDs(t *testing.T) {
	got := parseLoadedImageIDs("nginx:1.31\tsha256:abc\n")
	if got["nginx:1.31"] != "sha256:abc" {
		t.Fatalf("unexpected image id state: %#v", got)
	}
}
