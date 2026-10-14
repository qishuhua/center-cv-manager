package provision

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"gopkg.in/yaml.v3"
)

// Track configuration contents, not only the expanded Compose document. Writable
// data directories, model repositories and publication bookkeeping are excluded.
func configurationPaths(c domain.Center, file, resolved string) ([]string, []string, error) {
	var doc struct {
		Services map[string]struct {
			Volumes []struct {
				Type, Source, Target string
				ReadOnly             bool `yaml:"read_only"`
			} `yaml:"volumes"`
		} `yaml:"services"`
		Configs map[string]struct {
			File string `yaml:"file"`
		} `yaml:"configs"`
		Secrets map[string]struct {
			File string `yaml:"file"`
		} `yaml:"secrets"`
	}
	if err := yaml.Unmarshal([]byte(resolved), &doc); err != nil {
		return nil, nil, err
	}
	root := path.Dir(file)
	roots := map[string]bool{root: true}
	excluded := map[string]bool{modular.ModelsRoot(c): true}
	if c.Deployment != nil {
		excluded[c.Deployment.LogsRoot] = true
		excluded[c.Deployment.MediaRoot] = true
	}
	for _, svc := range doc.Services {
		for _, mount := range svc.Volumes {
			if mount.Type != "bind" || !path.IsAbs(mount.Source) {
				continue
			}
			// Runtime data and host device mounts are not configuration inputs.
			if runtimeMount(mount.Source, mount.Target) {
				excluded[mount.Source] = true
				continue
			}
			if mount.ReadOnly {
				roots[mount.Source] = true
			} else if mount.Source != root {
				excluded[mount.Source] = true
			}
		}
	}
	for _, v := range doc.Configs {
		if v.File != "" {
			roots[absoluteConfigPath(root, v.File)] = true
		}
	}
	for _, v := range doc.Secrets {
		if v.File != "" {
			roots[absoluteConfigPath(root, v.File)] = true
		}
	}
	// Explicit readonly mount roots must not bypass excluded data parents.
	include := []string{}
	exclude := []string{}
	for p := range roots {
		skip := false
		for e := range excluded {
			if e != "" && (p == e || strings.HasPrefix(p, e+"/")) {
				skip = true
			}
		}
		if !skip {
			include = append(include, p)
		}
	}
	for p := range excluded {
		if path.IsAbs(p) && p != root {
			exclude = append(exclude, p)
		}
	}
	sort.Strings(include)
	sort.Strings(exclude)
	return include, exclude, nil
}

func absoluteConfigPath(root, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(root, p)
}

func runtimeMount(source, target string) bool {
	for _, p := range []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/var/log", "/var/lib/docker", "/data/models", "/data/logs", "/data/media-resources"} {
		if source == p || strings.HasPrefix(source, p+"/") || target == p || strings.HasPrefix(target, p+"/") {
			return true
		}
	}
	return source == "/" || source == "/etc" || source == "/usr" || source == "/var"
}

func configurationHashCommand(roots, excluded []string) string {
	// find emits content hashes and names; no configuration values leave the center.
	parts := []string{"set -o pipefail", "("}
	for _, root := range roots {
		q := shellQuote(root)
		prune := "-name '.center-manager' -o -name '.git' -o -name '*.bak.*' -o -name '*.tmp.*'"
		for _, p := range excluded {
			prune += " -o \\( -type d -a -path " + shellQuote(p) + " \\)"
		}
		parts = append(parts, "if [ -d "+q+" ]; then find -L "+q+" \\( "+prune+" \\) -prune -o -type f -exec sha256sum -- {} + || exit 1; elif [ -f "+q+" ]; then sha256sum -- "+q+" || exit 1; else printf '%s\\n' "+shellQuote("MISSING "+root)+"; fi")
	}
	// No separator directly after '(' in shell syntax.
	script := parts[0] + "; ( " + strings.Join(parts[2:], "; ") + " ) | LC_ALL=C sort -u"
	return "bash -c " + shellQuote(script)
}

func configurationRevision(ctx context.Context, remote *sshx.DockerClient, c domain.Center, file, resolved string) (string, error) {
	roots, excluded, err := configurationPaths(c, file, resolved)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("无法确定配置目录")
	}
	output, err := runSSH(ctx, remote.SSH, configurationHashCommand(roots, excluded), "")
	if err != nil {
		return "", fmt.Errorf("读取配置文件指纹失败: %w", err)
	}
	return modular.Hash(output), nil
}
