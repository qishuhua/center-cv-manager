package modular

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	"github.com/docker/docker/client"
)

func TestInspectRequiresEveryServiceAndSuccessfulInit(t *testing.T) {
	for _, scenario := range []string{"healthy", "failed-init", "missing", "foreign", "unhealthy"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/containers/json") {
					if !strings.Contains(r.URL.Query().Get("filters"), "com.docker.compose.project=center-cv-infra") {
						t.Error("project filter missing")
					}
					project := "center-cv-infra"
					if scenario == "foreign" {
						project = "unrelated"
					}
					items := []any{map[string]any{"Id": "rabbit", "Labels": map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": "rabbitmq"}}}
					if scenario != "missing" {
						items = append(items, map[string]any{"Id": "init", "Labels": map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": "rabbitmq-init"}})
					}
					_ = json.NewEncoder(w).Encode(items)
					return
				}
				if strings.Contains(r.URL.Path, "/containers/init/") {
					code := 0
					if scenario == "failed-init" {
						code = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "init", "State": map[string]any{"Status": "exited", "ExitCode": code}})
					return
				}
				health := "healthy"
				if scenario == "unhealthy" {
					health = "unhealthy"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "rabbit", "State": map[string]any{"Running": true, "Status": "running", "Health": map[string]any{"Status": health}}})
			}))
			defer server.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"), client.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			err = Inspect(context.Background(), &sshx.DockerClient{Client: cli}, domain.ComposeModule{Project: "center-cv-infra", InitServices: []string{"rabbitmq-init"}}, []string{"rabbitmq", "rabbitmq-init"})
			if (err == nil) != (scenario == "healthy") {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
			if scenario == "failed-init" {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err = Wait(ctx, &sshx.DockerClient{Client: cli}, domain.ComposeModule{ID: "infra", Project: "center-cv-infra", InitServices: []string{"rabbitmq-init"}}, []string{"rabbitmq", "rabbitmq-init"}, nil)
				var failed *initFailure
				if !errors.As(err, &failed) || failed.code != 1 {
					t.Fatalf("expected immediate init failure, got %v", err)
				}
			}
		})
	}
}

func TestBusinessReadyURLsForServices(t *testing.T) {
	business := NewDeployment("/tmp/deploy/compose.yml", 1).Modules[2]
	if got := ReadyURLsForServices(business, []string{"video-transcode-service"}); got[0] != "http://127.0.0.1:8092/readyz" {
		t.Fatalf("video-transcode-service should map to its 8092 readyz probe, got %v", got)
	}
	if got := ReadyURLsForServices(business, []string{"center-cv-ingress"}); got[0] != "http://127.0.0.1:8090/readyz" {
		t.Fatalf("center-cv-ingress should map to its 8090 readyz probe, got %v", got)
	}
}
