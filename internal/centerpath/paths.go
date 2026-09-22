// Package centerpath defines the host-side layout used on managed centers.
package centerpath

import (
	"path"
	"strings"
)

const (
	// DefaultComposeFile is intentionally relative to the SSH user's home. It
	// is resolved to an absolute path during center initialization.
	DefaultComposeFile = "~/.local/share/center-manager/deploy/docker-compose.yml"
	legacyWorkspace    = "/opt/center"
)

// ValidRemoteFile accepts an absolute POSIX path or a path anchored at the
// SSH user's home. Other shell expansion is deliberately not supported.
func ValidRemoteFile(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	return path.IsAbs(value) || strings.HasPrefix(value, "~/")
}

// WorkspaceRoot returns the directory shared by deployment assets and
// persistent data. A custom compose file outside a deploy directory keeps its
// persistent data beside that file.
func WorkspaceRoot(composeFile string) string {
	if composeFile == "" {
		composeFile = DefaultComposeFile
	}
	directory := path.Dir(path.Clean(composeFile))
	if path.Base(directory) == "deploy" {
		return path.Dir(directory)
	}
	return directory
}

func ModelsRoot(composeFile string) string {
	if legacyLayout(composeFile) {
		return "/data/models"
	}
	return path.Join(WorkspaceRoot(composeFile), "data", "models")
}

func LogsRoot(composeFile string) string {
	if legacyLayout(composeFile) {
		return "/data/logs"
	}
	return path.Join(WorkspaceRoot(composeFile), "data", "logs")
}

func MediaResourcesRoot(composeFile string) string {
	if legacyLayout(composeFile) {
		return "/data/media-resources"
	}
	return path.Join(WorkspaceRoot(composeFile), "data", "media-resources")
}

func legacyLayout(composeFile string) bool {
	cleaned := path.Clean(composeFile)
	return cleaned == legacyWorkspace || strings.HasPrefix(cleaned, legacyWorkspace+"/")
}

// ShellQuote quotes a shell argument while expanding only a leading ~/ from a
// trusted path. It never evaluates arbitrary environment variables.
func ShellQuote(value string) string {
	quote := func(input string) string { return "'" + strings.ReplaceAll(input, "'", "'\\''") + "'" }
	if value == "~" {
		return `"$HOME"`
	}
	if strings.HasPrefix(value, "~/") {
		return `"$HOME"/` + quote(strings.TrimPrefix(value, "~/"))
	}
	return quote(value)
}
