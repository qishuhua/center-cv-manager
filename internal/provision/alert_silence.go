package provision

import (
	"context"
	"fmt"
	"path"
	"strings"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/templates"
	"github.com/docker/docker/errdefs"
)

// alertmanagerContainer 是中心 monitoring 模板里 alertmanager 的容器名
// （nvidia/denglin 两平台一致）：Webhook 文件变更后对其发 SIGHUP 重载配置。
const alertmanagerContainer = "alertmanager"

// alertWebhookFile 返回中心侧 alertmanager 的飞书 Webhook 文件路径
// （monitoring 模块目录下，经 bind mount 挂为 /run/secrets/feishu-webhook-url）。
func alertWebhookFile(m domain.ComposeModule) string {
	return path.Join(path.Dir(m.ComposeFile), "alerting", "feishu-webhook-url")
}

// alertWebhookValue 返回中心侧 feishu-webhook-url 的期望内容：平台告警关闭
// （test_mode）→ 占位地址（发送必然失败仅记日志）；告警启用 → 全局飞书
// Webhook；未配置时为空（与初始化渲染「未配置即空文件 + 无 receivers」同口径）。
func alertWebhookValue(testMode bool, configuredWebhook string) string {
	if testMode {
		return templates.SilencedFeishuWebhook
	}
	return configuredWebhook
}

// ensurePlatformAlerts 把「平台告警」开关同步到中心侧 alertmanager：静默中心
// 的 monitoring/alerting/feishu-webhook-url 换成占位地址，重新打开后恢复全局
// 飞书 Webhook。文件是 bind mount、写入即对容器可见，再向 alertmanager 容器
// 发 SIGHUP 重载（url_file 仅在配置加载时读取）即时生效，无需整体发布、不
// 重启监控栈。幂等：内容一致时零写入；上次同步失败的中心在任意一次保存时
// 自动重试。调用方须持有中心锁；legacy 中心与初始化前/重置后的中心不适用
// （后者由初始化渲染直接带上）。
func (s *Service) ensurePlatformAlerts(ctx context.Context, remote *sshx.DockerClient, c *domain.Center) error {
	if c.Deployment == nil {
		return nil
	}
	m, err := modular.Find(*c, "monitoring")
	if err != nil || !m.Enabled {
		// 自定义部署可能没有 monitoring 模块：无中心侧告警可静默。
		return nil
	}
	webhookFile := alertWebhookFile(m)
	current, readErr := readRemoteFile(ctx, remote.SSH, webhookFile)
	if readErr != nil {
		// 监控模块文件尚未落地（初始化中途/自定义模块）：不视为错误，
		// 下次初始化渲染或模块文件就位后的保存会自动补上。
		return nil
	}
	// 启用状态下需要真实 Webhook：解密失败时报错退出，绝不因凭据异常
	// 把已有 Webhook 收敛成空值；静默中心用不到凭据，直接占位。
	configured := ""
	if !c.TestMode {
		config, cfgErr := s.alerts.Config()
		if cfgErr != nil {
			return fmt.Errorf("读取飞书告警凭据失败: %w", cfgErr)
		}
		configured = config.FeishuWebhook
	}
	desired := alertWebhookValue(c.TestMode, configured)
	if strings.TrimSpace(current) == strings.TrimSpace(desired) {
		return nil
	}
	// 写入前先取发布计划：模块此前处于干净的已发布状态时，写入后把发布
	// 记录的 Files 指纹推进到新值——该文件内容不参与 Compose 容器配置
	// hash，运行中容器仍与配置一致，无需整体发布；模块本就有未发布变更
	// 或计划失败（如 Compose 被改坏）时不动发布记录，指纹差异随下次整体
	// 发布一并落账。
	pre, planErr := s.composePublishPlan(ctx, remote, *c, m.ID)
	advancePublishState := false
	if planErr == nil && !pre.Changed {
		if state, stateErr := readPublishedState(ctx, remote, pre.File); stateErr == nil && state.Content != "" {
			advancePublishState = true
		}
	}
	// 0644：alertmanager 以 65534 运行，需可读（与 prepareModules 落地权限一致）。
	if err = atomicRemoteWrite(ctx, remote.SSH, webhookFile, strings.TrimSpace(desired)+"\n", "0644"); err != nil {
		return fmt.Errorf("写入中心告警 Webhook 文件失败: %w", err)
	}
	// 容器不在运行（监控模块暂停/未启动）时略过：下次启动自然读到新文件。
	if err = remote.Client.ContainerKill(ctx, alertmanagerContainer, "SIGHUP"); err != nil && !errdefs.IsNotFound(err) && !errdefs.IsConflict(err) {
		return fmt.Errorf("平台告警开关已写入，但重载中心 Alertmanager 失败（重启监控模块后生效）: %w", err)
	}
	if advancePublishState {
		post, postErr := s.composePublishPlan(ctx, remote, *c, m.ID)
		if postErr != nil {
			return fmt.Errorf("平台告警开关已生效，但刷新监控模块发布指纹失败（可忽略，整体发布时自动对齐）: %w", postErr)
		}
		if err = writePublishedState(ctx, remote, post.File, composePublishedState{Content: post.contentRevision, Resolved: post.resolvedRevision, Files: post.filesRevision}); err != nil {
			return fmt.Errorf("平台告警开关已生效，但更新监控模块发布指纹失败: %w", err)
		}
	}
	return nil
}
