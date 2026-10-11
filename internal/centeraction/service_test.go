package centeraction

import (
	"strings"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
)

func TestComposeCommand(t *testing.T) {
	cases := map[string]string{RestartCompose: "restart", RedeployCompose: "up -d", StopCompose: "stop"}
	for action, want := range cases {
		got, _ := composeCommand(action)
		if got != want {
			t.Fatalf("composeCommand(%s) = %s, want %s", action, got, want)
		}
	}
	if command, _ := composeCommand("invalid"); command != "" {
		t.Fatalf("unexpected command %q", command)
	}
}

func TestCommandPreviewUsesConfiguredComposeFile(t *testing.T) {
	got := CommandPreview("/home/deploy user/compose.yml", RedeployCompose, "")
	want := "docker compose -f '/home/deploy user/compose.yml' up -d"
	if got != want {
		t.Fatalf("CommandPreview() = %q, want %q", got, want)
	}
}

func TestCommandPreviewQuotesContainerID(t *testing.T) {
	got := CommandPreview("", RestartContainer, "container-id")
	if got != "docker restart 'container-id'" {
		t.Fatalf("unexpected preview: %q", got)
	}
}

func TestRequiredLocalImages(t *testing.T) {
	config := `services:
  worker-a:
    image: center-cv-service:denglin
    pull_policy: never
  worker-b:
    image: center-cv-service:denglin
    pull_policy: never
  prometheus:
    image: prom/prometheus:v3
    pull_policy: missing
  built:
    build: .
`
	images, err := requiredLocalImages(config)
	if err != nil {
		t.Fatal(err)
	}
	services := images["center-cv-service:denglin"]
	if len(images) != 1 || len(services) != 2 || services[0] != "worker-a" || services[1] != "worker-b" {
		t.Fatalf("required images = %#v", images)
	}
}

func TestValidAction(t *testing.T) {
	for _, action := range []string{RestartContainer, RestartCompose, RedeployCompose, StopCompose, RemoveAllContainers} {
		if !validAction(action) {
			t.Fatalf("action %s should be valid", action)
		}
	}
	if validAction("prune_system") {
		t.Fatal("unexpected action accepted")
	}
}

func TestWholeComposeStartActionsRepairRuntimePaths(t *testing.T) {
	for _, action := range []string{RedeployCompose, RestartCompose} {
		if !repairsRuntimePaths(action) {
			t.Fatalf("action %s must repair flat runtime model paths before starting services", action)
		}
	}
	for _, action := range []string{RestartContainer, StopCompose, RemoveAllContainers} {
		if repairsRuntimePaths(action) {
			t.Fatalf("action %s must not mutate model paths", action)
		}
	}
}

func TestComposeStepErrorIncludesRemoteOutput(t *testing.T) {
	err := composeStepError("Compose 配置校验", "service worker has neither an image nor a build context\n", assertError("exit status 1"))
	want := "Compose 配置校验失败：service worker has neither an image nor a build context"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

func TestComposeStepErrorExplainsEmptyOutput(t *testing.T) {
	err := composeStepError("Compose 文件检查", "", assertError("exit status 1"))
	if !strings.Contains(err.Error(), "远端命令未返回错误详情") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompletedComposeJob(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		exitCode   int
		restart    containertypes.RestartPolicyMode
		wantResult bool
	}{
		{name: "successful one-shot", status: "exited", exitCode: 0, restart: "no", wantResult: true},
		{name: "failed one-shot", status: "exited", exitCode: 7, restart: "no", wantResult: false},
		{name: "running service", status: "running", exitCode: 0, restart: "unless-stopped", wantResult: false},
		{name: "stopped long-running service", status: "exited", exitCode: 0, restart: "unless-stopped", wantResult: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := dockertypes.ContainerJSON{ContainerJSONBase: &dockertypes.ContainerJSONBase{
				State:      &dockertypes.ContainerState{Status: tt.status, ExitCode: tt.exitCode},
				HostConfig: &containertypes.HostConfig{RestartPolicy: containertypes.RestartPolicy{Name: tt.restart}},
			}}
			if got := completedComposeJob(info); got != tt.wantResult {
				t.Fatalf("completedComposeJob() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
