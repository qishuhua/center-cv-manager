package provision

import (
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var composeFilename = regexp.MustCompile(`^(docker-compose|compose)([._-][a-zA-Z0-9_-]+)*\.ya?ml$`)

func legacyComposePath(primary, file string) (string, error) {
	if file == "" || file == path.Base(primary) {
		return primary, nil
	}
	if !composeFilename.MatchString(file) || path.Base(file) != file {
		return "", fmt.Errorf("无效的 Compose 文件名")
	}
	return path.Join(path.Dir(primary), file), nil
}
func legacyComposeTarget(ctx context.Context, remote *sshx.DockerClient, primary string, files []string) (string, error) {
	file := ""
	if len(files) > 0 {
		file = files[0]
	}
	target, err := legacyComposePath(primary, file)
	if err != nil {
		return "", err
	}
	// Only edit existing regular Compose files within this center's deployment directory.
	cmd := "root=$(realpath " + shellQuote(path.Dir(primary)) + ") && target=$(realpath " + shellQuote(target) + ") && [ -f \"$target\" ] && [ \"$(dirname \"$target\")\" = \"$root\" ]"
	if _, err = runSSH(ctx, remote.SSH, cmd, ""); err != nil {
		return "", fmt.Errorf("Compose 不存在、不可访问或超出中心部署目录")
	}
	return target, nil
}
func (s *Service) ListComposeFiles(ctx context.Context, id string) ([]domain.ComposeModule, error) {
	remote, c, err := s.connect(id)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	if c.Deployment != nil {
		return modular.Modules(c), nil
	}
	out, err := runSSH(ctx, remote.SSH, "find "+shellQuote(path.Dir(c.ComposeFile))+" -maxdepth 1 -type f -printf '%f\\n'", "")
	if err != nil {
		return nil, fmt.Errorf("读取中心 Compose 列表失败: %w", err)
	}
	result := []domain.ComposeModule{}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "compose.center-manager.override.yml" || (name != path.Base(c.ComposeFile) && !composeFilename.MatchString(name)) {
			continue
		}
		title := name
		for key, label := range map[string]string{"infra": "中间件", "monitoring": "监控", "logging": "日志", "gateway": "网关", "business": "业务"} {
			if strings.Contains(name, "."+key+".") {
				title = label
			}
		}
		if name == path.Base(c.ComposeFile) {
			title = "业务"
		}
		result = append(result, domain.ComposeModule{ID: name, Name: title, ComposeFile: path.Join(path.Dir(c.ComposeFile), name), Enabled: true, Status: "existing"})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ComposeFile == c.ComposeFile {
			return true
		}
		if result[j].ComposeFile == c.ComposeFile {
			return false
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}
