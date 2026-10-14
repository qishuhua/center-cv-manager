package provision

import "testing"

func TestLegacyComposeSelection(t *testing.T) {
	primary := "/home/jt/deploy/docker-compose.yml"
	for _, file := range []string{"docker-compose.infra.yml", "docker-compose.monitoring.yml", "docker-compose.logging.yml", "docker-compose.gateway.yml", "compose.business.yaml"} {
		got, err := legacyComposePath(primary, file)
		if err != nil || got != "/home/jt/deploy/"+file {
			t.Fatalf("%s: %s %v", file, got, err)
		}
	}
	for _, file := range []string{"../docker-compose.yml", "/etc/compose.yml", "docker-compose.yml.bak.123", "module.env", "compose.yml\nother", "compose.foo/bar.yml"} {
		if _, err := legacyComposePath(primary, file); err == nil {
			t.Fatalf("accepted %q", file)
		}
	}
	for _, file := range []string{"", "docker-compose.yml"} {
		if got, err := legacyComposePath(primary, file); err != nil || got != primary {
			t.Fatal(got, err)
		}
	}
}
