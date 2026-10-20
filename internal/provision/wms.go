package provision

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/templates"
)

const chuteReadyURL = "http://127.0.0.1:8091/readyz"

// ApplyBusinessWMS 把中心的错分检测配置（wms_url/mis_auth）幂等同步
// 到远端 business 模块：module.env 维护三个托管变量（机构编码不再配置，
// 两服务用 Compose 插值默认值），compose.yml 增删错分审核服务块并补齐
// ingress 轮询插值行。写入只置 ConfigChanged 等待整体发布生效，
// 不会启动或停止容器；调用方需先持久化中心字段。legacy 中心不支持。
func (s *Service) ApplyBusinessWMS(ctx context.Context, centerID string) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error {
		remote, c, err := s.connect(centerID)
		if err != nil {
			return err
		}
		defer remote.Close()
		if c.Deployment == nil {
			return fmt.Errorf("旧中心（未模块化）不支持错分检测")
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
		enabled := c.WMSURL != ""
		newCompose, err := templates.ApplyChuteService(oldCompose, c.Deployment.LogsRoot)
		if err != nil {
			return err
		}
		if newCompose, err = templates.EnsureIngressPollEnv(newCompose); err != nil {
			return err
		}
		newEnv := templates.ApplyMisSortModuleEnv(oldEnv, templates.MisSortOptions{
			Enabled: enabled, WMSURL: c.WMSURL, MisAuth: c.MisAuth,
		})
		composeChanged := newCompose != oldCompose
		envChanged := newEnv != oldEnv
		if !composeChanged && !envChanged {
			return nil
		}
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
		// 中间状态都能通过 docker compose config。
		candidate := m
		candidate.ComposeFile = tmpCompose
		if _, err = runSSH(ctx, remote.SSH, modular.CommandWithEnvFile(candidate, tmpEnv)+" config --quiet", ""); err != nil {
			return fmt.Errorf("Compose 校验失败，未替换原配置: %w", err)
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
		if err = s.centers.UpdateDeployment(centerID, c.Deployment); err != nil {
			return err
		}
		return nil
	})
}

// syncChuteReadyURL 保证 business 模块就绪探活含错分审核服务项（服务始终
// 部署，只增不删；模块 Wait 对不在操作范围内的服务会自动过滤该项）。
func syncChuteReadyURL(urls []string) []string {
	if containsString(urls, chuteReadyURL) {
		return urls
	}
	return append(urls, chuteReadyURL)
}
