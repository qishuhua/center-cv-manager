package centerpath

import "testing"

func TestHomeWorkspaceLayout(t *testing.T) {
	compose := "/home/ops/.local/share/center-manager/deploy/docker-compose.yml"
	if got := WorkspaceRoot(compose); got != "/home/ops/.local/share/center-manager" {
		t.Fatalf("WorkspaceRoot() = %q", got)
	}
	if got := ModelsRoot(compose); got != "/home/ops/.local/share/center-manager/data/models" {
		t.Fatalf("ModelsRoot() = %q", got)
	}
	if got := LogsRoot(compose); got != "/home/ops/.local/share/center-manager/data/logs" {
		t.Fatalf("LogsRoot() = %q", got)
	}
	if got := MediaResourcesRoot(compose); got != "/home/ops/.local/share/center-manager/data/media-resources" {
		t.Fatalf("MediaResourcesRoot() = %q", got)
	}
}

func TestLegacyLayoutRemainsCompatible(t *testing.T) {
	if got := ModelsRoot("/opt/center/deploy/docker-compose.yml"); got != "/data/models" {
		t.Fatalf("legacy ModelsRoot() = %q", got)
	}
	if got := ModelsRoot("/opt/center/compose.yml"); got != "/data/models" {
		t.Fatalf("older legacy ModelsRoot() = %q", got)
	}
	if got := MediaResourcesRoot("/opt/center/deploy/docker-compose.yml"); got != "/data/media-resources" {
		t.Fatalf("legacy MediaResourcesRoot() = %q", got)
	}
}

func TestShellQuoteExpandsOnlyHomePrefix(t *testing.T) {
	if got := ShellQuote("~/.local/share/center manager"); got != `"$HOME"/'.local/share/center manager'` {
		t.Fatalf("ShellQuote() = %q", got)
	}
	if got := ShellQuote("$HOME/file"); got != `'$HOME/file'` {
		t.Fatalf("environment expression must stay literal: %q", got)
	}
}
