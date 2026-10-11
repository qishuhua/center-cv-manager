package store

import (
	"path/filepath"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
)

func TestModuleStateRoundTripAndLegacyCompatibility(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := domain.Center{ID: "legacy", Name: "Legacy", Host: "localhost", Port: 22, Username: "test", AuthType: "password", DockerSocket: "/var/run/docker.sock", ComposeFile: "/opt/center/deploy/docker-compose.yml", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = st.SaveCenter(c); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCenter(c.ID)
	if err != nil || got.Deployment != nil {
		t.Fatal(got, err)
	}
	c.ID = "modular"
	c.Deployment = modular.NewDeployment("/home/cv/deploy/docker-compose.yml", 2)
	c.Deployment.Modules[0].AppliedRevision = "test-revision"
	if err = st.SaveCenter(c); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetCenter(c.ID)
	if err != nil || got.Deployment == nil || got.Deployment.Modules[0].AppliedRevision != "test-revision" {
		t.Fatal(got, err)
	}
	all, err := st.ListCenters()
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
}
