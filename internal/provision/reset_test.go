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

func TestLegacyResetProjectsWithoutComposeFiles(t *testing.T) {
	container := func(id, project, files, dir string) types.Container {
		return types.Container{ID: id, Labels: map[string]string{
			"com.docker.compose.project":              project,
			"com.docker.compose.project.config_files": files,
			"com.docker.compose.project.working_dir":  dir,
		}}
	}
	ours := container("ours", "business", "/srv/deploy/docker-compose.yml", "/srv/deploy")
	for _, tc := range []struct {
		name       string
		containers []types.Container
		want       map[string]bool
		wantErr    bool
	}{
		{name: "already removed containers and directory", want: map[string]bool{}},
		{name: "deleted configuration with surviving containers", containers: []types.Container{ours}, want: map[string]bool{"business": true}},
		{name: "multiple compose projects", containers: []types.Container{ours, container("infra", "infra", "/srv/deploy/compose.infra.yml", "/srv/deploy")}, want: map[string]bool{"business": true, "infra": true}},
		{name: "unrelated containers retained", containers: []types.Container{ours, container("other", "other", "/srv/deploy-other/docker-compose.yml", "/srv/deploy-other"), {ID: "manual"}}, want: map[string]bool{"business": true}},
		{name: "project name collision", containers: []types.Container{ours, container("other", "business", "/elsewhere/compose.yml", "/elsewhere")}, wantErr: true},
		{name: "unknown member of managed project", containers: []types.Container{ours, container("unknown", "business", "", "")}, wantErr: true},
		{name: "missing provenance in deployment directory", containers: []types.Container{container("unknown", "business", "", "/srv/deploy")}, wantErr: true},
		{name: "config outside directory", containers: []types.Container{container("outside", "business", "/srv/other/compose.yml", "/srv/deploy")}, wantErr: true},
		{name: "multiple files in directory", containers: []types.Container{container("merged", "business", "/srv/deploy/compose.yml,/srv/deploy/compose.override.yml", "/srv/deploy")}, want: map[string]bool{"business": true}},
		{name: "mixed directory configuration", containers: []types.Container{container("mixed", "business", "/srv/deploy/compose.yml,/elsewhere/compose.yml", "/srv/deploy")}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := legacyResetProjects("/srv/deploy", "docker-compose.yml", tc.containers)
			if (err != nil) != tc.wantErr {
				t.Fatalf("projects=%v, error=%v", got, err)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("projects=%v, want %v", got, tc.want)
			}
		})
	}
}
