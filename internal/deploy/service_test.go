package deploy

import (
	"sort"
	"strings"
	"testing"

	"center-cv-manager/internal/build"
	"github.com/docker/docker/api/types/image"
)

func TestFindImagePrefersBuildLabels(t *testing.T) {
	images := []image.Summary{
		{RepoTags: []string{"custom/image:gpu"}, Labels: map[string]string{build.LabelSourceID: "source-1", build.LabelSourceRef: "v1.2.3"}},
	}
	got := findImage(images, Item{SourceID: "source-1", Ref: "v1.2.3", ExpectedImage: "software:v1.2.3"})
	if got != "custom/image:gpu" {
		t.Fatalf("findImage() = %q", got)
	}
}

func TestFindImageRequiresReleaseCommit(t *testing.T) {
	images := []image.Summary{
		{RepoTags: []string{"software:main-old"}, Labels: map[string]string{build.LabelSourceID: "source-1", build.LabelSourceRef: "main", build.LabelCommit: "old"}},
		{RepoTags: []string{"software:main-new"}, Labels: map[string]string{build.LabelSourceID: "source-1", build.LabelSourceRef: "main", build.LabelCommit: "new"}},
	}
	got := findImage(images, Item{SourceID: "source-1", Ref: "main", Commit: "new", ExpectedImage: "software:main-new"})
	if got != "software:main-new" {
		t.Fatalf("findImage() = %q", got)
	}
}

func TestFindImageRejectsStaleTagForRelease(t *testing.T) {
	images := []image.Summary{
		{RepoTags: []string{"software:main-new"}, Labels: map[string]string{build.LabelSourceID: "source-1", build.LabelSourceRef: "main", build.LabelCommit: "old"}},
	}
	got := findImage(images, Item{SourceID: "source-1", Ref: "main", Commit: "new", ExpectedImage: "software:main-new"})
	if got != "" {
		t.Fatalf("findImage() = %q, want no stale image", got)
	}
}

func TestExplicitDeploymentMapping(t *testing.T) {
	services, err := parseDeploymentServices(`services:
  worker-0:
    image: custom/worker:denglin
    labels:
      com.center-manager.image.repository: center-cv-service
  worker-1:
    image: custom/worker:denglin
    labels:
      com.center-manager.image.repository: center-cv-service
  transcode:
    image: video-transcode-service:latest
    labels:
      com.center-manager.image.repository: video-transcode
  ingress:
    image: center-cv-ingress:latest
  unrelated:
    image: registry.local/center-cv-service:master
`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"worker-0": "center-cv-service", "worker-1": "center-cv-service", "transcode": "video-transcode", "ingress": "center-cv-ingress", "unrelated": "registry.local/center-cv-service"} {
		if services[name] != want {
			t.Fatalf("%s mapping = %q, want %q", name, services[name], want)
		}
	}
}

func TestRenderComposeReleaseUpdatesSingleComposeAndPreservesRunningVersions(t *testing.T) {
	base := `name: deploy
services:
  ingress:
    image: center-cv-ingress:latest
    labels:
      com.center-manager.image.repository: center-cv-ingress
  worker:
    image: center-cv-service:denglin
    labels:
      com.center-manager.image.repository: center-cv-service
`
	current := map[string]deployedService{
		"ingress": {Image: "center-cv-ingress:uat-old", Labels: map[string]string{"com.center-manager.source.id": "source-ingress", "com.center-manager.source.ref": "uat"}},
		"removed": {Image: "removed:old", Labels: map[string]string{"com.center-manager.source.id": "source-removed", "com.center-manager.source.ref": "old"}},
	}
	got, err := renderComposeRelease(
		base,
		current,
		[]Item{{SourceID: "source-service", SourceName: "service", Ref: "uat"}},
		map[string]string{"source-service": "center-cv-service:uat-new"},
		map[string][]string{"source-service": {"worker"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"center-cv-ingress:uat-old", "center-cv-service:uat-new", "com.center-manager.image.repository: center-cv-service", "com.center-manager.source.id: source-service"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("rendered Compose does not contain %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "removed:old") {
		t.Fatalf("rendered Compose added a service absent from the base:\n%s", got)
	}
}

func TestParseComposeServicesAndImageRepository(t *testing.T) {
	const composeConfig = `name: center
services:
  center-cv-service-0:
    image: center-cv-service:release-1.2.0
  center-cv-service-1:
    image: center-cv-service:release-1.2.0
  rabbitmq:
    image: rabbitmq:4.3.5-management
  builder:
    build:
      context: .
`
	services, err := parseComposeServices(composeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 3 {
		t.Fatalf("services = %#v", services)
	}
	repository := imageRepository("center-cv-service:release-1.2.0")
	matched := []string{}
	for name, serviceImage := range services {
		if imageRepository(serviceImage) == repository {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)
	if len(matched) != 2 || matched[0] != "center-cv-service-0" || matched[1] != "center-cv-service-1" {
		t.Fatalf("matched = %#v", matched)
	}
	if _, ok := services["builder"]; ok {
		t.Fatalf("build-only service should be skipped: %#v", services)
	}
}

func TestImageRepositoryStripsTagAndDigest(t *testing.T) {
	cases := map[string]string{
		"center-cv-service:release-1.2.0":  "center-cv-service",
		"registry.local/app/api:v1":        "registry.local/app/api",
		"nginx":                            "nginx",
		"app@sha256:abcdef":                "app",
		"registry.local/app@sha256:abcdef": "registry.local/app",
	}
	for reference, expected := range cases {
		if got := imageRepository(reference); got != expected {
			t.Fatalf("imageRepository(%q) = %q, want %q", reference, got, expected)
		}
	}
}

func TestCheckSummaryIncludesEveryFailure(t *testing.T) {
	got := checkSummary(CheckResult{Error: "SSH 失败", MissingImages: []string{"api:v1"}, MissingServices: []string{"api（Compose 未引用镜像 api）"}})
	for _, expected := range []string{"SSH 失败", "缺少镜像 api:v1", "Compose 未引用镜像 api"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("summary %q does not contain %q", got, expected)
		}
	}
}

func TestCombineSSHOutput(t *testing.T) {
	cases := []struct {
		stdout string
		stderr string
		want   string
	}{
		{stdout: "compose config\n", want: "compose config\n"},
		{stderr: "warning\n", want: "warning\n"},
		{stdout: "compose config\r\n", stderr: "warning\n", want: "compose config\nwarning\n"},
	}
	for _, test := range cases {
		if got := combineSSHOutput(test.stdout, test.stderr); got != test.want {
			t.Fatalf("combineSSHOutput(%q, %q) = %q, want %q", test.stdout, test.stderr, got, test.want)
		}
	}
}

func TestReleasePreservesInheritedLabelsAndUnselectedConfiguration(t *testing.T) {
	base := `# retain comment
x-common: &common
  image: worker:old
  labels:
    com.center-manager.image.repository: worker-source
    custom: retained
services:
  worker:
    <<: *common
  other:
    image: other:${VERSION:-stable}
`
	result, err := renderComposeRelease(base, nil, []Item{{SourceID: "source", Ref: "main"}}, map[string]string{"source": "worker:new"}, map[string][]string{"source": {"worker"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"retain comment", "${VERSION:-stable}", "custom: retained", "com.center-manager.image.repository: worker-source", "worker:new"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("lost %q in %s", expected, result)
		}
	}
}
