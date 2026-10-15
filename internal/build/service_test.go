package build

import (
	"os"
	"path/filepath"
	"testing"

	"center-cv-manager/internal/domain"
)

func TestDefaultImageTagUsesSoftwareNameAndVersion(t *testing.T) {
	source := domain.Source{Name: "Center CV Service", Repository: "https://git.example/group/service.git"}
	if got := DefaultImageTag(source, "release/1.2.0"); got != "center-cv-service:release-1.2.0" {
		t.Fatalf("unexpected image tag: %q", got)
	}
}

func TestReleaseImageTagLocksCommit(t *testing.T) {
	source := domain.Source{Name: "Center CV Service"}
	got := ReleaseImageTag(source, "release/1.2.0", "a1b2c3d4e5f678901234")
	if got != "center-cv-service:release-1.2.0-a1b2c3d4e5f6" {
		t.Fatalf("unexpected release image tag: %q", got)
	}
}

func TestReleaseRegistryTagIsolatesPlatform(t *testing.T) {
	commit := "a1b2c3d4e5f678901234"
	// 同一 commit 不同平台必须得到不同标签，防止共享仓库内互相覆盖；
	// 标签只是版本部分，不含仓库名（拼完整引用由 registry.Reference 负责）。
	nvidia := ReleaseRegistryTag("release/1.2.0", commit, "nvidia")
	denglin := ReleaseRegistryTag("release/1.2.0", commit, "denglin")
	if nvidia != "release-1.2.0-a1b2c3d4e5f6-nvidia" {
		t.Fatalf("unexpected nvidia registry tag: %q", nvidia)
	}
	if denglin != "release-1.2.0-a1b2c3d4e5f6-denglin" {
		t.Fatalf("unexpected denglin registry tag: %q", denglin)
	}
	if nvidia == denglin {
		t.Fatal("不同平台的仓库标签必须不同")
	}
	// 平台未解析时不追加后缀（等价于版本主体）。
	if got := ReleaseRegistryTag("release/1.2.0", commit, ""); got != ReleaseVersionBody("release/1.2.0", commit) {
		t.Fatalf("无平台后缀的标签应等于版本主体: %q", got)
	}
}

func TestResolveDockerfileByAccelerator(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile.nvidia", "Dockerfile.denglin"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for accelerator, expected := range map[string]string{"nvidia": "Dockerfile.nvidia", "denglin": "Dockerfile.denglin"} {
		got, err := resolveDockerfile(dir, "auto", accelerator)
		if err != nil || got != expected {
			t.Fatalf("resolveDockerfile(%s) = %q, %v; want %q", accelerator, got, err, expected)
		}
	}
}

func TestResolveDockerfileFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDockerfile(dir, "auto", "nvidia")
	if err != nil || got != "Dockerfile" {
		t.Fatalf("unexpected fallback: %q, %v", got, err)
	}
}

func TestDefaultImageTagFallsBackToRepositoryName(t *testing.T) {
	source := domain.Source{Name: "推理服务", Repository: "ssh://git@git.example:2222/group/center-cv-service.git"}
	if got := DefaultImageTag(source, "1.0.0"); got != "center-cv-service:1.0.0" {
		t.Fatalf("unexpected image tag: %q", got)
	}
}
