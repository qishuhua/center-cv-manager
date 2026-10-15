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

// registryReleaseEnabled 判断本次发布是否走镜像仓库模式：已配置且探活通过、
// 本地 Docker 引擎可用。任一条件不满足时回退既有的中心远端构建路径，并在
// 任务日志说明原因，发布不会因仓库未就绪而被阻塞。
func (s *Server) registryReleaseEnabled(ctx context.Context, r releaseReporter) bool {
	settings, err := s.registries.Settings()
	if err != nil || !settings.Configured {
		return false
	}
	client, err := registry.NewClient(settings.Endpoint)
	if err != nil {
		r.Logf("镜像仓库配置无效（%v），回退中心远端构建", err)
		return false
	}
	if err = client.Ping(ctx); err != nil {
		r.Logf("%v，回退中心远端构建", err)
		return false
	}
	if !build.LocalDockerAvailable() {
		r.Log("本地 Docker 引擎不可用（compose 部署需挂载 /var/run/docker.sock），回退中心远端构建")
		return false
	}
	return true
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

// prepareRegistrySoftwareRelease 是镜像仓库模式的软件发布准备：按中心平台在
// 本地构建一次并推送仓库（仓库已有同标签镜像时校验 digest 后直接复用，跨中心
// 天然去重），再经 SSH 反向隧道把镜像按 digest 拉到中心、重打成本地发布标签。
// manifest 经隧道回到管理端，镜像层由 registry 307 重定向到 OSS 签名 URL 直下。
func (s *Server) prepareRegistrySoftwareRelease(ctx context.Context, centerID string, missing []deploy.Item, r releaseReporter) error {
	settings, err := s.registries.Settings()
	if err != nil {
		return err
	}
	_, host, err := registry.ParseEndpoint(settings.Endpoint)
	if err != nil {
		return err
	}
	client, err := registry.NewClient(settings.Endpoint)
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
	r.Logf("阶段 2/4：按 %s 平台准备镜像（仓库 %s，共 %d 个）", build.AcceleratorName(platform), host, len(missing))
	pulls := make([]registry.PullItem, 0, len(missing))
	for index, item := range missing {
		src, err := s.sources.Get(item.SourceID)
		if err != nil {
			return err
		}
		start := 5 + float64(index)*40/float64(len(missing))
		end := 5 + float64(index+1)*40/float64(len(missing))
		repository := registry.RepoPath(src)
		tag := build.ReleaseRegistryTag(src, item.Ref, item.Commit, platform)
		digest, err := client.ManifestDigest(ctx, repository, tag)
		if err != nil {
			return err
		}
		if digest != "" {
			r.Logf("%s 命中仓库镜像 %s:%s（%s），跳过构建", item.SourceName, repository, tag, digest)
		} else {
			checkout, err := s.sources.EnsureCheckout(ctx, src, item.Ref, item.Commit, phaseReporter{reporter: r, prefix: fmt.Sprintf("阶段 2/4：准备 %s 源码", item.SourceName), start: start, end: start + (end-start)*0.4})
			if err != nil {
				return fmt.Errorf("准备 %s:%s@%s 源码失败: %w", item.SourceName, item.Ref, shortCommit(item.Commit), err)
			}
			digest, err = s.builds.RunLocal(ctx, build.LocalOptions{
				ContextDir: checkout.Path, Dockerfile: "auto", RegistryRef: registry.Reference(host, repository, tag),
				SourceID: src.ID, SourceName: src.Name, SourceRef: item.Ref, Commit: item.Commit, Accelerator: platform,
			}, phaseReporter{reporter: r, prefix: fmt.Sprintf("阶段 2/4：构建并推送 %s", item.SourceName), start: start + (end-start)*0.4, end: end})
			if err != nil {
				return fmt.Errorf("构建 %s 失败: %w", item.SourceName, err)
			}
		}
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
