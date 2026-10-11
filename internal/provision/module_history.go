package provision

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/modular"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

var backupName = regexp.MustCompile(`^compose\.yml\.bak\.[0-9]+$`)

func (s *Service) ModuleHistory(ctx context.Context, id, moduleID string) ([]string, error) {
	remote, c, err := s.connect(id)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return nil, err
	}
	out, err := runSSH(ctx, remote.SSH, "find "+shellQuote(path.Dir(m.ComposeFile))+" -maxdepth 1 -type f -name 'compose.yml.bak.*' -printf '%f\\n'", "")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range strings.Fields(out) {
		if backupName.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// Unregister does not remove files or volumes. An active project must be
// explicitly stopped and its containers removed by an operator before removal.
func (s *Service) DeleteModule(ctx context.Context, id, moduleID string) error {
	unlock, err := centerlock.AcquireContext(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	remote, c, err := s.connect(id)
	if err != nil {
		return err
	}
	defer remote.Close()
	if c.Deployment == nil {
		return fmt.Errorf("不能移除兼容部署登记")
	}
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return err
	}
	for _, other := range c.Deployment.Modules {
		for _, dep := range other.DependsOn {
			if dep == moduleID {
				return fmt.Errorf("模块 %s 仍依赖此模块", other.Name)
			}
		}
	}
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+m.Project))})
	if err != nil {
		return err
	}
	if len(containers) > 0 {
		return fmt.Errorf("项目仍有容器，不能移除模块登记；停止服务不会移除容器")
	}
	remaining := c.Deployment.Modules[:0]
	for _, other := range c.Deployment.Modules {
		if other.ID != moduleID {
			remaining = append(remaining, other)
		}
	}
	c.Deployment.Modules = remaining
	return s.centers.UpdateDeployment(id, c.Deployment)
}
