package provision

import (
	"center-cv-manager/internal/modular"
	"github.com/docker/docker/api/types"
	"testing"
)

func TestPublishAction(t *testing.T) {
	if publishAction(true) != "deploy" || publishAction(false) != "restart" {
		t.Fatal("incorrect action")
	}
}
func TestComposeProjectPreservesExistingOwnership(t *testing.T) {
	cfg, err := modular.Parse("name: wrong-default\nservices:\n  prometheus: {image: prom, container_name: prometheus}\n  grafana: {image: grafana, container_name: grafana}\n")
	if err != nil {
		t.Fatal(err)
	}
	cs := []types.Container{{Names: []string{"/prometheus"}, Labels: map[string]string{"com.docker.compose.project": "deploy", "com.docker.compose.service": "prometheus"}}}
	if got, err := inferComposeProject(cfg, cs); err != nil || got != "deploy" {
		t.Fatal(got, err)
	}
	cs = append(cs, types.Container{Names: []string{"/grafana"}, Labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "grafana"}})
	if _, err := inferComposeProject(cfg, cs); err == nil {
		t.Fatal("accepted conflicting ownership")
	}
}
func TestPublishedHashRequiresEveryService(t *testing.T) {
	cs := []types.Container{{Labels: map[string]string{"com.docker.compose.project": "deploy", "com.docker.compose.service": "a", "com.docker.compose.config-hash": "one"}}}
	if !hashesMatch("a one", "deploy", cs) {
		t.Fatal("expected match")
	}
	for _, output := range []string{"", "a changed", "a one\nb two", "warning text extra"} {
		if hashesMatch(output, "deploy", cs) {
			t.Fatal("incorrect match", output)
		}
	}
	if hashesMatch("a one", "other", cs) {
		t.Fatal("matched different project")
	}
}

func TestFilterHashLinesKeepsOnlyScopedServices(t *testing.T) {
	output := "a one\nb two\nc three\nwarning text extra"
	if got := filterHashLines(output, []string{"a", "c"}); got != "a one\nc three" {
		t.Fatal(got)
	}
	if got := filterHashLines(output, nil); got != "" {
		t.Fatal("empty scope must filter everything:", got)
	}
	cs := []types.Container{{Labels: map[string]string{"com.docker.compose.project": "deploy", "com.docker.compose.service": "a", "com.docker.compose.config-hash": "one"}}}
	// 范围外服务的漂移不应阻塞只核验 a 的软件发布。
	if !hashesMatch(filterHashLines(output, []string{"a"}), "deploy", cs) {
		t.Fatal("out-of-scope drift must not block scoped verification")
	}
}

func TestPublicationTracksSavedAndFailedChanges(t *testing.T) {
	state := composePublishedState{Content: "published-text", Resolved: "published-config"}
	if publicationChanged(state, "published-text", "published-config") {
		t.Fatal("unchanged snapshot marked dirty")
	}
	if !publicationChanged(state, "edited-text", "published-config") {
		t.Fatal("saved text modification lost")
	}
	if !publicationChanged(state, "published-text", "changed-environment") {
		t.Fatal("resolved config modification lost")
	}
	if publicationChanged(state, "published-text", "published-config") {
		t.Fatal("reverted content remains dirty")
	}
	state.Failed = true
	if !publicationChanged(state, "published-text", "published-config") {
		t.Fatal("failed publish must remain pending")
	}
}
