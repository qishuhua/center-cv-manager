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

// RestartModels 模型发布后的业务模块重启：模型同步只切换符号链接，需要
// 重启业务 Compose 的常驻服务让进程重新加载新模型。模块化中心重启整个
// 业务模块（ingress/Worker/transcode 一并，跳过 init 服务与停止的服务，
// restart 不触发配置应用）；legacy 单 Compose 与中间件同文件，仍只按
// Worker 服务名前缀重启。before 是联合发布前的容器快照：本次软件发布已
// 重建的容器已看到新模型挂载，跳过避免双重重启。
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
		// 只有挂模型的业务模块参与；其余模块一个容器都不动。
		if !m.Enabled || len(m.ModelServices) == 0 {
			continue
		}
		project := m.Project
		excluded := map[string]bool{}
		for _, n := range m.InitServices {
			excluded[n] = true
		}
		isTarget := func(string) bool { return true }
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
			for name, spec := range parsed.Services {
				if strings.HasSuffix(name, "-init") || spec.Restart == "no" {
					excluded[name] = true
				}
			}
			isTarget = func(name string) bool {
				return name == "center-cv-service" || strings.HasPrefix(name, "center-cv-service-")
			}
			if project == "" {
				return fmt.Errorf("无法识别旧中心 Compose 项目名")
			}
		}
		targets := []string{}
		seen := map[string]bool{}
		for _, v := range containers {
			name := v.Labels["com.docker.compose.service"]
			if excluded[name] || !isTarget(name) || v.Labels["com.docker.compose.project"] != project || v.Labels["com.docker.compose.oneoff"] == "True" {
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
			if !seen[name] {
				seen[name] = true
				targets = append(targets, name)
			}
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
		count += len(targets)
	}
	r.Logf("已重启业务模块 %d 个服务", count)
	return nil
}
