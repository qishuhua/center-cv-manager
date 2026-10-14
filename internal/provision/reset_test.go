package provision

import (
	"github.com/docker/docker/api/types"
	"reflect"
	"testing"
)

func TestResetConfigPathsPreservesDataAndImages(t *testing.T) {
	got := resetConfigPaths("/srv/deploy", "docker-compose.yml", []string{"docker-compose.yml", "docker-compose.yml.bak.1", "infra", "monitoring", "data", "models", "images", "logs", "media-resources", "unrelated.txt", "../models"}, nil)
	want := []string{"/srv/deploy/docker-compose.yml", "/srv/deploy/docker-compose.yml.bak.1", "/srv/deploy/infra", "/srv/deploy/monitoring"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths=%v, want %v", got, want)
	}
}

func TestResetRejectsUnsafePathsAndProtectedData(t *testing.T) {
	for _, root := range []string{"/", "/home", "/var/lib/docker", "relative", "/srv/../data", "/srv\nother"} {
		if validateResetRoot(root) == nil {
			t.Fatalf("accepted root %q", root)
		}
	}
	if err := validateResetRoot("/srv/deploy"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target, resolved string
		protected        []string
	}{
		{"/srv/models", "/srv/models", nil},
		{"/srv/deploy/monitoring", "/data/models", nil},
		{"/srv/deploy/business", "/srv/deploy/business", []string{"/srv/deploy/business/models"}},
		{"/srv/deploy/business", "/srv/deploy/business", []string{"/srv/deploy"}},
	} {
		if validateResetTarget("/srv/deploy", tc.target, tc.resolved, tc.protected) == nil {
			t.Fatalf("accepted unsafe target %+v", tc)
		}
	}
	if err := validateResetTarget("/srv/deploy", "/srv/deploy/infra", "/srv/deploy/infra", []string{"/srv/data/models"}); err != nil {
		t.Fatal(err)
	}
}

func TestResetOnlySelectsOwnedComposeProjects(t *testing.T) {
	cs := []types.Container{{ID: "managed", Labels: map[string]string{"com.docker.compose.project": "ours"}}, {ID: "unrelated", Labels: map[string]string{"com.docker.compose.project": "other"}}, {ID: "manual"}}
	got := managedResetContainers(cs, map[string]bool{"ours": true, "": true})
	if len(got) != 1 || got[0].ID != "managed" {
		t.Fatalf("unexpected destructive scope: %#v", got)
	}
}
