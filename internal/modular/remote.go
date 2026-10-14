package modular

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"golang.org/x/crypto/ssh"
)

func Run(ctx context.Context, client *ssh.Client, command, stdin string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var output, stderr bytes.Buffer
	session.Stdout = &output
	session.Stderr = &stderr
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}
	if err = session.Start(command); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = session.Close()
		<-done
		err = ctx.Err()
	}
	return output.String() + stderr.String(), err
}

func CheckOwnership(ctx context.Context, remote *sshx.DockerClient, m domain.ComposeModule, cfg Config) error {
	if m.Project == "" {
		return nil
	}
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers {
		for service, spec := range cfg.Services {
			if spec.ContainerName == "" {
				continue
			}
			for _, n := range c.Names {
				if strings.TrimPrefix(n, "/") == spec.ContainerName && (c.Labels["com.docker.compose.project"] != m.Project || c.Labels["com.docker.compose.service"] != service) {
					return fmt.Errorf("容器 %s 属于其他项目/服务，必须先制定迁移计划", spec.ContainerName)
				}
			}
		}
	}
	return nil
}

// Inspect checks actual containers (including one-shot exit codes), not the
// last recorded module status. Every expected service must be accounted for.
type initFailure struct {
	service string
	code    int
}

func (e *initFailure) Error() string {
	return fmt.Sprintf("初始化服务 %s 已失败 (exit %d)，请查看该容器日志", e.service, e.code)
}

func Inspect(ctx context.Context, remote *sshx.DockerClient, m domain.ComposeModule, services []string) error {
	opts := containertypes.ListOptions{All: true}
	if m.Project != "" {
		opts.Filters = filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+m.Project))
	} else {
		return fmt.Errorf("旧中心需使用原有 Compose 状态检查")
	}
	containers, err := remote.Client.ContainerList(ctx, opts)
	if err != nil {
		return err
	}
	init := map[string]bool{}
	for _, n := range m.InitServices {
		init[n] = true
	}
	for _, name := range services {
		found := false
		for _, c := range containers {
			if c.Labels["com.docker.compose.project"] != m.Project || c.Labels["com.docker.compose.service"] != name || strings.EqualFold(c.Labels["com.docker.compose.oneoff"], "true") {
				continue
			}
			found = true
			info, err := remote.Client.ContainerInspect(ctx, c.ID)
			if err != nil {
				return err
			}
			state := info.State
			if state == nil {
				return fmt.Errorf("%s 状态不可读", name)
			}
			if init[name] {
				if state.Status == "exited" && state.ExitCode != 0 {
					return &initFailure{service: name, code: state.ExitCode}
				}
				if state.Status != "exited" || state.ExitCode != 0 {
					return fmt.Errorf("初始化服务 %s: %s (exit %d)", name, state.Status, state.ExitCode)
				}
			} else if !state.Running || state.Restarting || (state.Health != nil && state.Health.Status != "healthy") {
				return fmt.Errorf("服务 %s 尚未就绪: %s", name, state.Status)
			}
		}
		if !found {
			return fmt.Errorf("服务 %s 尚未创建", name)
		}
	}
	return nil
}

func Wait(ctx context.Context, remote *sshx.DockerClient, m domain.ComposeModule, services []string, urls []string) error {
	// Older built-in deployments included the optional chute service probe.
	// Keep custom probes, but do not wait on a service absent from this operation.
	if m.ID == "business" {
		hasChute := false
		for _, service := range services {
			if service == "chute-verification-service" {
				hasChute = true
			}
		}
		if !hasChute {
			filtered := make([]string, 0, len(urls))
			for _, u := range urls {
				if u != "http://127.0.0.1:8091/readyz" {
					filtered = append(filtered, u)
				}
			}
			urls = filtered
		}
	}
	timeout := m.Timeout
	if timeout < 10 {
		timeout = 600
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var last error
	for {
		last = Inspect(waitCtx, remote, m, services)
		var failed *initFailure
		if errors.As(last, &failed) {
			return fmt.Errorf("模块 %s 等待失败: %w", m.ID, last)
		}
		if last == nil {
			for _, u := range urls {
				_, last = Run(waitCtx, remote.SSH, "code=$(curl --silent --show-error --max-time 5 -o /dev/null -w '%{http_code}' "+centerpath.ShellQuote(u)+") && case \"$code\" in 2??) ;; *) exit 1;; esac", "")
				if last != nil {
					last = fmt.Errorf("就绪检查 %s: %w", u, last)
					break
				}
			}
		}
		if last == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("模块 %s 等待失败: %v: %w", m.ID, last, waitCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}
