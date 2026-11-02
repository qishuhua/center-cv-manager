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

// ApplyBusinessSettings 把中心的业务模块托管设置幂等同步到远端：
// module.env 维护三组托管变量——错分检测（WMS 地址、共享鉴权、ingress 轮询
// 开关与轮询周期，错分由 ingress 统一控制：开关打开即启动摄像机轮询与
// 计划落格 ZeroMQ 广播，Worker 订阅本地核验；9/10 核验结果由
// video-transcode 读 WMS 地址与鉴权直发 missort 接收端，普通告警走业务
// 回调）、
// Debug 模式（LOG_LEVEL=debug）、存图（CV_SAVE_DEBUG_IMAGE=1）、仅解码
// （CV_DECODE_ONLY_MODE=1）与飞书告警测试（TEST_ALARM_ENABLED、
// FEISHU_WEBHOOK_URL）。compose.yml 的错分/告警插值行经 Ensure* 幂等兜底
// （防远端手工删改）。写入只置 ConfigChanged 等待整体发布生效；调用方需先
// 持久化中心字段。末尾另同步两处不等待发布的设置：网关播放器静态页
// （ensureGatewayPlayer，置 gateway 待发布）与监控平台告警开关的中心侧
// alertmanager 静默（ensurePlatformAlerts，SIGHUP 即时生效）。legacy 中心
// 不支持。
func (s *Service) ApplyBusinessSettings(ctx context.Context, centerID string) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error {
		remote, c, err := s.connect(centerID)
		if err != nil {
			return err
		}
		defer remote.Close()
		if c.Deployment == nil {
			return fmt.Errorf("旧中心（未模块化）不支持错分检测、Debug 模式、存图、仅解码模式与飞书告警测试")
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
		newCompose := templates.EnsureIngressPollEnv(oldCompose)
		newCompose = templates.EnsureWorkerZMQEnv(newCompose)
		newCompose = templates.EnsureTranscodeAlarmEnv(newCompose)
		newCompose = templates.EnsureWorkerLogLevelEnv(newCompose)
		newCompose = templates.EnsureWorkerSaveImageEnv(newCompose)
		newCompose = templates.EnsureWorkerDecodeOnlyEnv(newCompose)
		newCompose = templates.EnsureTranscodeTestAlarmEnv(newCompose)
		newEnv := templates.ApplyMisSortModuleEnv(oldEnv, templates.MisSortOptions{
			Enabled: enabled, WMSURL: c.WMSURL, MisAuth: c.MisAuth, IntervalSeconds: c.MisSortIntervalSeconds,
		})
		newEnv = templates.ApplyLogLevelModuleEnv(newEnv, c.DebugLogEnabled)
		newEnv = templates.ApplySaveImageModuleEnv(newEnv, c.SaveImageEnabled)
		newEnv = templates.ApplyDecodeOnlyModuleEnv(newEnv, c.DecodeOnlyEnabled)
		newEnv = templates.ApplyTestAlarmModuleEnv(newEnv, c.TestAlarmEnabled, c.FeishuWebhookURL, centerPlayerURL(c))
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
			// 中间状态都能通过 docker compose config。
			candidate := m
			candidate.ComposeFile = tmpCompose
			if output, cfgErr := runSSH(ctx, remote.SSH, modular.CommandWithEnvFile(candidate, tmpEnv)+" config --quiet", ""); cfgErr != nil {
				return fmt.Errorf("Compose 校验失败，未替换原配置: %s %w", strings.TrimSpace(output), cfgErr)
			}
			backup := fmt.Sprint(time.Now().UnixNano())
			for _, target := range []string{composePath, envPath} {
				if _, err = runSSH(ctx, remote.SSH, "cp -p "+shellQuote(target)+" "+shellQuote(target+".bak."+backup), ""); err != nil {
					return err
				}
			}
			// 替换顺序只需保证固定、可预期：先 module.env 后 compose。
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
			}
		}
		// 网关播放器静态页与飞书告警测试联动（FEISHU_PLAYER_URL 指向它）：
		// 存量中心在此幂等补齐 player.html/挂载/location，变化置 gateway 待发布。
		if err = s.ensureGatewayPlayer(ctx, remote, &c); err != nil {
			return err
		}
		// 监控平台告警开关同步中心侧 alertmanager（Webhook 文件 + SIGHUP 即时
		// 生效，不置待发布），与管理端通知静默、统一监控剔除同口径。
		if err = s.ensurePlatformAlerts(ctx, remote, &c); err != nil {
			return err
		}
		return s.centers.UpdateDeployment(centerID, c.Deployment)
	})
}
