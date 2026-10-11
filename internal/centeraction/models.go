package centeraction

import (
	"context"
	"fmt"
	"strings"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	containertypes "github.com/docker/docker/api/types/container"
)

// RuntimeSnapshot lets joint releases distinguish containers actually recreated
// by Compose from containers whose image was already current.
func (s *Service) RuntimeSnapshot(ctx context.Context, id string) (map[string]string, error) {
	cfg, socket, c, err := s.centers.Connection(id)
	if err != nil {
		return nil, err
	}
	remote, fp, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(c, fp)
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, v := range containers {
		result[v.ID] = v.State
	}
	return result, nil
}

// before is a pre-activation snapshot for joint releases. Newly created
// containers already see the new model mount and must not be restarted twice.
func (s *Service) RestartModels(ctx context.Context, id string, before map[string]string, r Reporter) error {
	unlock, err := centerlock.AcquireContext(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, socket, c, err := s.centers.Connection(id)
	if err != nil {
		return err
	}
	remote, fp, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(c, fp)
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}
	count := 0
	for _, m := range modular.Modules(c) {
		if !m.Enabled {
			continue
		}
		models := map[string]bool{}
		for _, n := range m.ModelServices {
			models[n] = true
		}
		project := m.Project
		if c.Deployment == nil {
			text, e := runSSH(ctx, remote.SSH, modular.Command(m)+" config")
			if e != nil {
				return e
			}
			parsed, e := modular.Parse(text)
			if e != nil {
				return e
			}
			project = parsed.Name
			for n := range parsed.Services {
				if n == "center-cv-service" || strings.HasPrefix(n, "center-cv-service-") {
					models[n] = true
				}
			}
			if project == "" {
				return fmt.Errorf("无法识别旧中心 Compose 项目名")
			}
		}
		targets := []string{}
		for _, v := range containers {
			name := v.Labels["com.docker.compose.service"]
			if !models[name] || v.Labels["com.docker.compose.project"] != project || v.Labels["com.docker.compose.oneoff"] == "True" {
				continue
			}
			if before != nil {
				if _, existed := before[v.ID]; !existed && v.State == "running" {
					r.Logf("%s 已在本次发布重建，无需再次重启", name)
					continue
				}
			}
			if v.State != "running" {
				continue
			}
			targets = append(targets, name)
			count++
		}
		if len(targets) == 0 {
			continue
		}
		command := modular.Command(m) + " restart"
		for _, n := range targets {
			command += " " + shellQuote(n)
		}
		r.Log("$ " + command)
		if output, e := runSSH(ctx, remote.SSH, command); e != nil {
			return fmt.Errorf("应用模型失败: %s: %w", output, e)
		}
		m.Project = project
		if err = modular.Wait(ctx, remote, m, targets, modular.ReadyURLsForServices(m, targets)); err != nil {
			return err
		}
	}
	r.Logf("已重启 %d 个模型使用服务", count)
	return nil
}
