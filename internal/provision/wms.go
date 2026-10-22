package provision

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/templates"
)

const chuteReadyURL = "http://127.0.0.1:8091/readyz"

// ApplyBusinessSettings 把中心的业务模块托管设置幂等同步到远端：
// module.env 维护两组托管变量——错分检测（wms_url/mis_auth，机构编码不再
// 配置，两服务用 Compose 插值默认值）与 Debug 模式（LOG_LEVEL=debug），
// compose.yml 增删错分审核服务块、补齐 ingress 轮询插值行与 Worker 公共
// 环境锚点的 LOG_LEVEL 插值行。写入只置 ConfigChanged 等待整体发布生效，
// 不会启动或停止容器；调用方需先持久化中心字段。legacy 中心不支持。
func (s *Service) ApplyBusinessSettings(ctx context.Context, centerID string) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error {
		remote, c, err := s.connect(centerID)
		if err != nil {
			return err
		}
		defer remote.Close()
		if c.Deployment == nil {
			return fmt.Errorf("旧中心（未模块化）不支持错分检测与 Debug 模式")
		}
		// 初始化未开始或重置后的中心没有远端模块文件；下次初始化渲染会
		// 直接带上当前配置，无需在此同步。
		if c.Deployment.Status == "preparing" || c.Deployment.Status == "reset" {
			return nil
		}
		m, err := modular.Find(c, "business")
		if err != nil {
			return err
		}
		composePath := m.ComposeFile
		envPath := path.Join(path.Dir(m.ComposeFile), "module.env")
		oldCompose, err := readRemoteFile(ctx, remote.SSH, composePath)
		if err != nil {
			return fmt.Errorf("读取业务 Compose 失败，请先完成初始化: %w", err)
		}
		oldEnv, err := readRemoteFile(ctx, remote.SSH, envPath)
		if err != nil {
			return fmt.Errorf("读取业务 module.env 失败: %w", err)
		}
		// 启用 = 配置非空且开关打开：关闭开关时配置值保留在中心记录里，
		// module.env 的托管变量被移除（回落 Compose 插值默认值），重新打开
		// 后再同步回来。
		enabled := c.WMSURL != "" && c.MisSortEnabled
		newCompose, err := templates.ApplyChuteService(oldCompose, c.Deployment.LogsRoot)
		if err != nil {
			return err
		}
		if newCompose, err = templates.EnsureIngressPollEnv(newCompose); err != nil {
			return err
		}
		newCompose = templates.EnsureWorkerLogLevelEnv(newCompose)
		newEnv := templates.ApplyMisSortModuleEnv(oldEnv, templates.MisSortOptions{
			Enabled: enabled, WMSURL: c.WMSURL, MisAuth: c.MisAuth,
		})
		newEnv = templates.ApplyLogLevelModuleEnv(newEnv, c.DebugLogEnabled)
		composeChanged := newCompose != oldCompose
		envChanged := newEnv != oldEnv
		if composeChanged || envChanged {
			if err = s.captureComposeBaseline(ctx, remote, c, "business"); err != nil {
				return err
			}
			stamp := time.Now().UnixNano()
			tmpCompose := fmt.Sprintf("%s.validate.%d", composePath, stamp)
			tmpEnv := fmt.Sprintf("%s.validate.%d", envPath, stamp)
			if err = uploadRemoteFile(ctx, remote.SSH, tmpCompose, newCompose, "0644"); err != nil {
				return err
			}
			defer func() {
				_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(tmpCompose)+" "+shellQuote(tmpEnv), "")
			}()
			if err = uploadRemoteFile(ctx, remote.SSH, tmpEnv, newEnv, "0600"); err != nil {
				return err
			}
			// 成对校验：候选 compose 与候选 module.env 一起解析，确保落盘后任意
			// 中间状态都能通过 docker compose config。命令输出（含 stderr）
			// 与 chute 块插入点上下文随错误带出，便于定位老模板中心的兼容问题。
			candidate := m
			candidate.ComposeFile = tmpCompose
			if output, cfgErr := runSSH(ctx, remote.SSH, modular.CommandWithEnvFile(candidate, tmpEnv)+" config --quiet", ""); cfgErr != nil {
				return fmt.Errorf("Compose 校验失败，未替换原配置: %s %w\n插入点上下文:\n%s", strings.TrimSpace(output), cfgErr, chuteInsertContext(newCompose))
			}
			backup := fmt.Sprint(time.Now().UnixNano())
			for _, target := range []string{composePath, envPath} {
				if _, err = runSSH(ctx, remote.SSH, "cp -p "+shellQuote(target)+" "+shellQuote(target+".bak."+backup), ""); err != nil {
					return err
				}
			}
			// 错分审核服务始终部署（插值默认兜底），替换顺序只需保证固定、可预期：
			// 先 module.env 后 compose。
			order := []string{envPath + "\x00" + tmpEnv, composePath + "\x00" + tmpCompose}
			for _, move := range order {
				target, temporary, _ := strings.Cut(move, "\x00")
				if _, err = runSSH(ctx, remote.SSH, "mv "+shellQuote(temporary)+" "+shellQuote(target), ""); err != nil {
					return err
				}
			}
			for i := range c.Deployment.Modules {
				module := &c.Deployment.Modules[i]
				if module.ID != m.ID {
					continue
				}
				if composeChanged {
					module.Revision = modular.Hash(newCompose)
				}
				if envChanged {
					module.FilesRevision = modular.Hash(module.FilesRevision + "module.env" + newEnv)
				}
				module.ConfigChanged = true
				module.Status = "prepared"
				module.UpdatedAt = time.Now()
				module.ReadyURLs = syncChuteReadyURL(module.ReadyURLs)
			}
		}
		if err = s.applyGatewayFallEvents(ctx, remote, c); err != nil {
			return err
		}
		if err = s.centers.UpdateDeployment(centerID, c.Deployment); err != nil {
			return err
		}
		return nil
	})
}

// chuteInsertContext 返回候选 Compose 中 chute 服务头行前后的片段，用于
// 校验失败时的诊断输出（插入位置错位是老模板中心最常见的同步失败原因）。
func chuteInsertContext(compose string) string {
	lines := strings.Split(compose, "\n")
	for i, line := range lines {
		if strings.TrimLeft(line, " \t") == templates.ChuteServiceName+":" {
			start, end := i-6, i+4
			if start < 0 {
				start = 0
			}
			if end > len(lines) {
				end = len(lines)
			}
			return strings.Join(lines[start:end], "\n")
		}
	}
	return "(未找到 chute 服务块)"
}

// applyGatewayFallEvents 幂等补齐 gateway nginx 的错分事件反代（服务仅回环
// 监听，局域网经 /fall-events/ 访问）：模板演进前初始化的存量中心由这里
// 自动补上，gateway 模块置待发布。nginx 文件缺失（异常布局）时跳过，不
// 阻断 business 同步。
func (s *Service) applyGatewayFallEvents(ctx context.Context, remote *sshx.DockerClient, c domain.Center) error {
	g, err := modular.Find(c, "gateway")
	if err != nil {
		return nil
	}
	nginxPath := path.Join(path.Dir(g.ComposeFile), "nginx", "nginx.conf")
	oldNginx, readErr := readRemoteFile(ctx, remote.SSH, nginxPath)
	if readErr != nil {
		return nil
	}
	newNginx := templates.EnsureFallEventsProxy(oldNginx)
	if newNginx == oldNginx {
		return nil
	}
	stamp := time.Now().UnixNano()
	tmpNginx := fmt.Sprintf("%s.validate.%d", nginxPath, stamp)
	if err = uploadRemoteFile(ctx, remote.SSH, tmpNginx, newNginx, "0644"); err != nil {
		return err
	}
	defer func() {
		_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(tmpNginx), "")
	}()
	backup := fmt.Sprint(time.Now().UnixNano())
	if _, err = runSSH(ctx, remote.SSH, "cp -p "+shellQuote(nginxPath)+" "+shellQuote(nginxPath+".bak."+backup), ""); err != nil {
		return err
	}
	if _, err = runSSH(ctx, remote.SSH, "mv "+shellQuote(tmpNginx)+" "+shellQuote(nginxPath), ""); err != nil {
		return err
	}
	for i := range c.Deployment.Modules {
		module := &c.Deployment.Modules[i]
		if module.ID != g.ID {
			continue
		}
		module.FilesRevision = modular.Hash(module.FilesRevision + "nginx/nginx.conf" + newNginx)
		module.ConfigChanged = true
		module.Status = "prepared"
		module.UpdatedAt = time.Now()
	}
	return nil
}

// syncChuteReadyURL 保证 business 模块就绪探活含错分审核服务项（服务始终
// 部署，只增不删；模块 Wait 对不在操作范围内的服务会自动过滤该项）。
func syncChuteReadyURL(urls []string) []string {
	if containsString(urls, chuteReadyURL) {
		return urls
	}
	return append(urls, chuteReadyURL)
}
