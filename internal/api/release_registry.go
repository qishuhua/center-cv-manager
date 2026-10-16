package api

import (
	"context"
	"fmt"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/registry"
	"center-cv-manager/internal/sshx"
)

// registryForwardPort 是镜像分发时在中心回环地址上申请的反向隧道端口；
// 被占用时 RequestRemoteForward 会退回远端自动分配的端口。
const registryForwardPort = 5000

// registryReleaseClient 返回软件发布使用的镜像仓库客户端与设置。软件发布
// 硬依赖镜像仓库：版本必须先构建发布，源码不再发送到中心构建。
func (s *Server) registryReleaseClient() (*registry.Client, domain.RegistrySettings, error) {
	settings, err := s.registries.Settings()
	if err != nil {
		return nil, settings, err
	}
	if !settings.Configured {
		return nil, settings, fmt.Errorf("软件发布依赖镜像仓库，请先在「软件仓库」页配置：%w", registry.ErrNotConfigured)
	}
	client, err := s.registries.Client()
	if err != nil {
		return nil, settings, err
	}
	return client, settings, nil
}

// centerReleasePlatform 解析本次发布使用的加速卡平台：中心配置优先，其次
// 最近一次检测结果，均缺失时返回空串（由调用方现场探测）。
func centerReleasePlatform(center domain.Center) string {
	if center.Accelerator != "" && center.Accelerator != "auto" {
		return center.Accelerator
	}
	if center.DetectedAccelerator != "" && center.DetectedAccelerator != "auto" {
		return center.DetectedAccelerator
	}
	return ""
}

// manifestDigestForPlatform 查询版本镜像的 digest：优先带平台后缀的标签，
// 未命中回退通用（无后缀）标签——平台无关构建一次发布、全部平台可用。
// 返回命中的标签与 digest；两者皆空表示该版本在本平台不可用。
func manifestDigestForPlatform(ctx context.Context, client *registry.Client, repository string, item deploy.Item, platform string) (string, string, error) {
	tag := build.ReleaseRegistryTag(item.Ref, item.Commit, platform)
	digest, err := client.ManifestDigest(ctx, repository, tag)
	if err != nil || digest != "" {
		return tag, digest, err
	}
	shared := build.ReleaseVersionBody(item.Ref, item.Commit)
	digest, err = client.ManifestDigest(ctx, repository, shared)
	if err != nil || digest != "" {
		return shared, digest, err
	}
	return "", "", nil
}

// prepareRegistrySoftwareRelease 是软件发布的镜像准备阶段：核对镜像仓库中
// 该中心平台的版本镜像（不构建——版本必须已在「软件仓库」页发布），再经
// SSH 反向隧道把镜像按 digest 拉到中心、重打成本地发布标签。manifest 经
// 隧道回到管理端，镜像层由 registry 307 重定向到 OSS 签名 URL 直下。
func (s *Server) prepareRegistrySoftwareRelease(ctx context.Context, centerID string, missing []deploy.Item, r releaseReporter) error {
	client, settings, err := s.registryReleaseClient()
	if err != nil {
		return err
	}
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		return err
	}
	platform := centerReleasePlatform(center)
	if platform == "" {
		r.Log("正在自动识别中心加速卡类型")
		if platform, err = s.builds.DetectCenterAccelerator(ctx, centerID); err != nil {
			return err
		}
	}
	r.Logf("阶段 2/4：核对镜像仓库版本（平台 %s，共 %d 个）", build.AcceleratorName(platform), len(missing))
	pulls := make([]registry.PullItem, 0, len(missing))
	for _, item := range missing {
		src, err := s.sources.Get(item.SourceID)
		if err != nil {
			return err
		}
		repository := registry.RepoPath(src)
		tag, digest, err := manifestDigestForPlatform(ctx, client, repository, item, platform)
		if err != nil {
			return err
		}
		if digest == "" {
			return fmt.Errorf("软件 %s 的镜像版本 %s 未发布 %s 平台镜像，请先在「软件仓库」页构建发布", item.SourceName, build.ReleaseVersionBody(item.Ref, item.Commit), build.AcceleratorName(platform))
		}
		r.Logf("%s 命中仓库镜像 %s:%s（%s）", item.SourceName, repository, tag, digest)
		pulls = append(pulls, registry.PullItem{Repository: repository, Digest: digest, LocalTag: item.ExpectedImage})
	}

	r.Message("阶段 3/4：分发镜像到中心")
	distribute := phaseReporter{reporter: r, prefix: "阶段 3/4：镜像分发", start: 45, end: 65}
	cfg, socket, connection, err := s.centers.Connection(centerID)
	if err != nil {
		return err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	if err = s.centers.RememberHostKey(connection, fingerprint); err != nil {
		return err
	}
	_, forwardTarget, err := registry.ParseEndpoint(settings.Endpoint)
	if err != nil {
		return err
	}
	forward, err := sshx.RequestRemoteForward(remote.SSH, registryForwardPort, forwardTarget)
	if err != nil {
		return err
	}
	defer forward.Close()
	distribute.Logf("反向隧道 %s → %s 已建立（manifest 经 SSH 回传，镜像层从 OSS 直下）", forward.Addr(), forwardTarget)
	if err = registry.PullAndTag(ctx, remote.Client, forward.Addr(), pulls, distribute); err != nil {
		return err
	}
	distribute.Progress(100)
	r.Log("阶段 4/4：开始部署已锁定的镜像")
	return nil
}
