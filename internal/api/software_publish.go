package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/registry"
	"center-cv-manager/internal/source"
	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

// publishSourceImage 是代码仓库页的「构建并发布」入口：在本地构建选定版本的
// 全部平台变体（登临/英伟达都成功才推送，平台无关源码一次构建三标签），
// 推送到镜像仓库供各中心拉取。不再面向具体中心，也不触发部署。
func (s *Server) publishSourceImage(c *gin.Context) {
	var in struct {
		Ref    string `json:"ref"`
		Commit string `json:"commit"`
		Kind   string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Ref) == "" {
		fail(c, 400, fmt.Errorf("请选择需要构建的软件版本"))
		return
	}
	if err := source.ValidateRef(in.Ref); err != nil {
		fail(c, 400, fmt.Errorf("版本无效"))
		return
	}
	commit := strings.ToLower(strings.TrimSpace(in.Commit))
	if err := source.ValidateCommit(commit); err != nil {
		fail(c, 400, fmt.Errorf("commit 无效，请刷新版本"))
		return
	}
	src, err := s.sources.Get(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	verifyCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	err = s.sources.VerifySelection(verifyCtx, src, in.Ref, in.Kind, commit)
	cancel()
	if err != nil {
		fail(c, 400, fmt.Errorf("%s: %w", src.Name, err))
		return
	}
	// 镜像仓库模式是明确目标，不回退中心远端构建。
	settings, err := s.registries.Settings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	if !settings.Configured {
		fail(c, 400, registry.ErrNotConfigured)
		return
	}
	client, err := registry.NewClient(settings.Endpoint)
	if err != nil {
		fail(c, 400, err)
		return
	}
	pingCtx, pingCancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	err = client.Ping(pingCtx)
	pingCancel()
	if err != nil {
		fail(c, 502, err)
		return
	}
	if !build.LocalDockerAvailable() {
		fail(c, 400, fmt.Errorf("本地 Docker 引擎不可用（compose 部署需挂载 /var/run/docker.sock），无法构建镜像"))
		return
	}
	sourceID, sourceName, ref := src.ID, src.Name, in.Ref
	ok(c, s.tasks.Submit("PUBLISH_IMAGE", map[string]string{
		"source_id": sourceID, "source_name": sourceName, "ref": ref, "commit": commit,
		"versions": softwareVersionText(sourceName, ref, commit),
	}, func(ctx context.Context, h *task.Handle) error {
		h.Message(fmt.Sprintf("阶段 1/3：准备 %s 源码（%s@%s）", sourceName, ref, shortCommit(commit)))
		checkout, err := s.sources.EnsureCheckout(ctx, src, ref, commit, h)
		if err != nil {
			return err
		}
		variants, err := build.DetectBuildVariants(checkout.Path)
		if err != nil {
			return err
		}
		_, host, err := registry.ParseEndpoint(settings.Endpoint)
		if err != nil {
			return err
		}
		repository := registry.RepoPath(src)
		// 先构建全部变体（任一失败即终止，不推送），全部成功后统一推送：
		// 仓库里的版本要么完整覆盖所有平台，要么不存在。
		planned := make([]string, 0, 3)
		for _, variant := range variants {
			tag := build.ReleaseRegistryTag(ref, commit, variantName(variant))
			options := build.LocalOptions{
				ContextDir: checkout.Path, Dockerfile: "auto", RegistryRef: registry.Reference(host, repository, tag),
				SourceID: sourceID, SourceName: sourceName, SourceRef: ref, Commit: commit, Accelerator: variant,
			}
			if variant == "universal" {
				// 主标签是 -nvidia（variantName），补齐 -denglin 与 -cpu，
				// 同一镜像三平台标签，纯 CPU 中心也能拉取。
				options.ExtraTags = []string{
					registry.Reference(host, repository, build.ReleaseRegistryTag(ref, commit, "denglin")),
					registry.Reference(host, repository, build.ReleaseRegistryTag(ref, commit, "cpu")),
				}
			}
			h.Message(fmt.Sprintf("阶段 2/3：构建 %s 镜像", build.AcceleratorName(variant)))
			if err = s.builds.BuildLocal(ctx, options, h); err != nil {
				return fmt.Errorf("构建 %s 失败（未推送任何镜像）: %w", build.AcceleratorName(variant), err)
			}
			planned = append(planned, options.RegistryRef)
			planned = append(planned, options.ExtraTags...)
		}
		h.Message("阶段 3/3：推送镜像仓库")
		for _, ref := range planned {
			if _, err = s.builds.PushRef(ctx, ref, h); err != nil {
				return err
			}
		}
		h.Message(fmt.Sprintf("发布完成：%s 共 %d 个平台镜像已入库，各中心可按显卡类型拉取", repository, len(planned)))
		return nil
	}))
}

// variantName 把 universal 变体的主标签定为 nvidia（ExtraTags 补齐其余平台），
// 其余变体标签即平台名。
func variantName(variant string) string {
	if variant == "universal" {
		return "nvidia"
	}
	return variant
}

// centerRegistryVersions 列出镜像仓库中匹配中心显卡平台的软件版本，供中心
// 详情页选择切换；中心平台未知时提示先做硬件检测。
func (s *Server) centerRegistryVersions(c *gin.Context) {
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	platform := centerReleasePlatform(center)
	if platform == "" {
		fail(c, 400, fmt.Errorf("未检测到中心显卡类型，请先在中心列表执行硬件检测"))
		return
	}
	list, err := s.registries.ListVersions(c.Request.Context())
	if err != nil {
		status := 400
		if err != registry.ErrNotConfigured {
			status = 502
		}
		fail(c, status, err)
		return
	}
	ok(c, registry.VersionsForPlatform(list, platform))
}

// releaseCenterSoftware 从镜像仓库选定版本发布到中心：经反向隧道按 digest
// 拉取镜像并重打本地标签，然后走统一发布编排（模型健康校验、Compose 更新、
// 服务重建、发布记录一应俱全）。
func (s *Server) releaseCenterSoftware(c *gin.Context) {
	var in struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Repository) == "" || strings.TrimSpace(in.Tag) == "" {
		fail(c, 400, fmt.Errorf("请选择要发布的镜像版本"))
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	settings, err := s.registries.Settings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	if !settings.Configured {
		fail(c, 400, registry.ErrNotConfigured)
		return
	}
	client, err := registry.NewClient(settings.Endpoint)
	if err != nil {
		fail(c, 400, err)
		return
	}
	info, err := client.ImageInfo(c.Request.Context(), in.Repository, in.Tag)
	if err != nil {
		fail(c, 502, err)
		return
	}
	sourceID := info.Labels[build.LabelSourceID]
	ref := info.Labels[build.LabelSourceRef]
	commit := info.Labels[build.LabelCommit]
	if sourceID == "" || ref == "" || commit == "" {
		fail(c, 400, fmt.Errorf("镜像 %s:%s 缺少来源标签，无法发布", in.Repository, in.Tag))
		return
	}
	src, err := s.sources.Get(sourceID)
	if err != nil {
		fail(c, 400, fmt.Errorf("镜像对应的软件仓库已删除，无法发布"))
		return
	}
	item := deploy.Item{SourceID: src.ID, SourceName: src.Name, Ref: ref, Commit: commit, ExpectedImage: build.ReleaseImageTag(src, ref, commit)}
	centerID, centerName := center.ID, center.Name
	versions := softwareVersionText(src.Name, ref, commit)
	// 与统一发布一致：开始/结束向飞书群发送通知（尽力而为，不影响任务）。
	notifier := newReleaseNotifier(s.alerts, releaseOperator(c), []string{centerName + "：" + versions})
	created := s.tasks.Submit("RELEASE_SOFTWARE", map[string]string{
		"center_id": centerID, "center_name": centerName, "repository": in.Repository, "tag": in.Tag, "versions": versions,
	}, func(ctx context.Context, h *task.Handle) error {
		err := s.runRelease(ctx, centerID, []deploy.Item{item}, nil, h)
		notifier.record(centerName, err)
		return err
	})
	s.watchReleaseNotificationsAsync(notifier, []*domain.Task{created})
	ok(c, created)
}
