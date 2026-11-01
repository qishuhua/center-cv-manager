package provision

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/templates"
	"golang.org/x/crypto/ssh"
)

// centerPlayerURL 是本中心网关的告警回放播放器入口，供飞书告警消息链接跳转。
func centerPlayerURL(c domain.Center) string {
	return fmt.Sprintf("http://%s:%d/player.html", c.Host, c.ConsolePort)
}

// ensureGatewayPlayer 幂等部署网关播放器静态页（存量中心补齐；新初始化中心经
// RenderModules 直接渲染）：nginx/player.html 落盘、gateway compose 补挂载行、
// nginx.conf 补 /player.html location，均只增不删。任一文件有变化即置 gateway
// ConfigChanged，等网关整体发布生效（挂载变化会 force-recreate nginx 容器）。
// player.html 是管理端自有静态资产，内容与模板不一致时直接覆盖；compose 与
// nginx.conf 可能含远端手改，替换前备份。调用方须持有中心锁。
func (s *Service) ensureGatewayPlayer(ctx context.Context, remote *sshx.DockerClient, c *domain.Center) error {
	if c.Deployment == nil {
		return nil
	}
	m, err := modular.Find(*c, "gateway")
	if err != nil {
		return err
	}
	playerHTML, err := templates.GatewayPlayerHTML()
	if err != nil {
		return err
	}
	dir := path.Dir(m.ComposeFile)
	playerPath := path.Join(dir, "nginx", "player.html")
	nginxPath := path.Join(dir, "nginx", "nginx.conf")
	envPath := path.Join(dir, "module.env")

	changed := false
	if current, readErr := readRemoteFile(ctx, remote.SSH, playerPath); readErr != nil || current != playerHTML {
		if _, mkErr := runSSH(ctx, remote.SSH, "umask 022; mkdir -p "+shellQuote(path.Dir(playerPath)), ""); mkErr != nil {
			return fmt.Errorf("创建网关 nginx 目录失败: %w", mkErr)
		}
		if err = atomicRemoteWrite(ctx, remote.SSH, playerPath, playerHTML, "0644"); err != nil {
			return fmt.Errorf("写入播放器静态页失败: %w", err)
		}
		changed = true
	}

	oldCompose, err := readRemoteFile(ctx, remote.SSH, m.ComposeFile)
	if err != nil {
		return fmt.Errorf("读取网关 Compose 失败，请先完成初始化: %w", err)
	}
	newCompose := templates.EnsureGatewayPlayerMount(oldCompose)
	if newCompose != oldCompose {
		if err = s.captureComposeBaseline(ctx, remote, *c, m.ID); err != nil {
			return err
		}
		stamp := time.Now().UnixNano()
		tmpCompose := fmt.Sprintf("%s.validate.%d", m.ComposeFile, stamp)
		if err = uploadRemoteFile(ctx, remote.SSH, tmpCompose, newCompose, "0644"); err != nil {
			return err
		}
		defer func() {
			_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(tmpCompose), "")
		}()
		candidate := m
		candidate.ComposeFile = tmpCompose
		if output, cfgErr := runSSH(ctx, remote.SSH, modular.CommandWithEnvFile(candidate, envPath)+" config --quiet", ""); cfgErr != nil {
			return fmt.Errorf("网关 Compose 校验失败，未替换原配置: %s %w", strings.TrimSpace(output), cfgErr)
		}
		if err = atomicRemoteWrite(ctx, remote.SSH, m.ComposeFile, newCompose, "0644"); err != nil {
			return err
		}
		_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(tmpCompose), "")
		changed = true
	}

	oldNginx, err := readRemoteFile(ctx, remote.SSH, nginxPath)
	if err != nil {
		return fmt.Errorf("读取网关 nginx.conf 失败: %w", err)
	}
	newNginx := templates.EnsureGatewayPlayerLocation(oldNginx)
	if newNginx != oldNginx {
		if err = atomicRemoteWrite(ctx, remote.SSH, nginxPath, newNginx, "0644"); err != nil {
			return fmt.Errorf("写入网关 nginx.conf 失败: %w", err)
		}
		changed = true
	}

	if changed {
		for i := range c.Deployment.Modules {
			module := &c.Deployment.Modules[i]
			if module.ID != m.ID {
				continue
			}
			if newCompose != oldCompose {
				module.Revision = modular.Hash(newCompose)
			}
			module.ConfigChanged = true
			module.UpdatedAt = time.Now()
		}
	}
	return nil
}

// atomicRemoteWrite 先备份远端原文件（存在时），再经临时文件原子替换。
func atomicRemoteWrite(ctx context.Context, client *ssh.Client, remotePath, content, mode string) error {
	backup := fmt.Sprintf("%s.bak.%d", remotePath, time.Now().UnixNano())
	if _, err := runSSH(ctx, client, "if [ -f "+shellQuote(remotePath)+" ]; then cp -p "+shellQuote(remotePath)+" "+shellQuote(backup)+"; fi", ""); err != nil {
		return err
	}
	stamp := time.Now().UnixNano()
	tmp := fmt.Sprintf("%s.tmp.%d", remotePath, stamp)
	if err := uploadRemoteFile(ctx, client, tmp, content, mode); err != nil {
		return err
	}
	defer func() {
		_, _ = runSSH(context.Background(), client, "rm -f "+shellQuote(tmp), "")
	}()
	if _, err := runSSH(ctx, client, "mv "+shellQuote(tmp)+" "+shellQuote(remotePath), ""); err != nil {
		return err
	}
	return nil
}
