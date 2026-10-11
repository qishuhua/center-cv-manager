package modular

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		})
	}
}
