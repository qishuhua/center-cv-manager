package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/source"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/gin-gonic/gin"
)

// buildCenterSoftware 在指定中心上构建某个软件选定版本的镜像。
// 构建与发布解耦：这里只负责构建（打 ReleaseImageTag 标签与来源标签），
// 可选的"构建后立即发布"由前端在构建任务成功后走统一发布端点完成。
func (s *Server) buildCenterSoftware(c *gin.Context) {
	var in struct {
		SourceID string `json:"source_id"`
		Ref      string `json:"ref"`
		Commit   string `json:"commit"`
		Kind     string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.SourceID) == "" {
		fail(c, 400, fmt.Errorf("请选择需要构建的软件和版本"))
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
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	src, err := s.sources.Get(in.SourceID)
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
	imageTag := build.ReleaseImageTag(src, in.Ref, commit)
	versions := fmt.Sprintf("%s:%s@%s", src.Name, in.Ref, shortCommit(commit))
	sourceID, sourceName, ref := src.ID, src.Name, in.Ref
	ok(c, s.tasks.Submit("BUILD_IMAGE", map[string]string{
		"center_id":    center.ID,
		"center_name":  center.Name,
		"source_id":    sourceID,
		"source_name":  sourceName,
		"ref":          ref,
		"commit":       commit,
		"image_tag":    imageTag,
		"dockerfile":   "auto",
		"no_cache":     "false",
		"build_args":   "{}",
		"versions":     versions,
	}, func(ctx context.Context, h *task.Handle) error {
		return centerlock.Within(ctx, center.ID, func(ctx context.Context) error {
			h.Message(fmt.Sprintf("阶段 1/2：准备 %s 源码（%s@%s）", sourceName, ref, shortCommit(commit)))
			checkout, err := s.sources.EnsureCheckout(ctx, src, ref, commit, h)
			if err != nil {
				return err
			}
			h.Message(fmt.Sprintf("阶段 2/2：在中心 %s 上构建镜像 %s", center.Name, imageTag))
			if err = s.builds.Run(ctx, build.Options{
				CenterID:    center.ID,
				ContextDir:  checkout.Path,
				Dockerfile:  "auto",
				ImageTag:    imageTag,
				SourceID:    sourceID,
				SourceName:  sourceName,
				SourceRef:   ref,
				Commit:      commit,
			}, h); err != nil {
				return err
			}
			h.Message(fmt.Sprintf("镜像构建完成：%s，可在发布时选择该版本", imageTag))
			return nil
		})
	}))
}

type softwareImage struct {
	ID         string `json:"id"`
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	Image      string `json:"image"`
	CreatedAt  int64  `json:"created_at"`
	Size       int64  `json:"size"`
	InUse      bool   `json:"in_use"`
}

// centerSoftwareImages 列出中心上由本管理端构建的软件镜像（按来源标签过滤，
// 与部署校验 findImage 的匹配口径一致），并标记是否仍被容器引用，供发布选择
// 与镜像管理（删除）使用。
func (s *Server) centerSoftwareImages(c *gin.Context) {
	cfg, socket, _, e := s.centers.Connection(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	remote, _, e := sshx.NewDocker(cfg, socket)
	if e != nil {
		fail(c, 400, e)
		return
	}
	defer remote.Close()
	ctx := c.Request.Context()
	images, e := remote.Client.ImageList(ctx, image.ListOptions{})
	if e != nil {
		fail(c, 400, e)
		return
	}
	containers, e := remote.Client.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		fail(c, 400, e)
		return
	}
	inUse := map[string]bool{}
	for _, container := range containers {
		inUse[container.ImageID] = true
	}
	out := make([]softwareImage, 0, len(images))
	for _, summary := range images {
		sourceID := summary.Labels[build.LabelSourceID]
		ref := summary.Labels[build.LabelSourceRef]
		if sourceID == "" || ref == "" {
			continue
		}
		tags := make([]string, 0, len(summary.RepoTags))
		for _, tag := range summary.RepoTags {
			if tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		if len(tags) == 0 {
			continue
		}
		out = append(out, softwareImage{
			ID:         summary.ID,
			SourceID:   sourceID,
			SourceName: summary.Labels[build.LabelSourceName],
			Ref:        ref,
			Commit:     summary.Labels[build.LabelCommit],
			Image:      strings.Join(tags, "、"),
			CreatedAt:  summary.Created,
			Size:       summary.Size,
			InUse:      inUse[summary.ID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	ok(c, out)
}

// deleteCenterSoftwareImage 删除中心上由本管理端构建的业务镜像。
// 只允许删除带来源标签的软件镜像；被任何容器（含已停止）引用的镜像拒绝删除。
func (s *Server) deleteCenterSoftwareImage(c *gin.Context) {
	var in struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		fail(c, 400, fmt.Errorf("请选择要删除的镜像"))
		return
	}
	cfg, socket, _, e := s.centers.Connection(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	remote, _, e := sshx.NewDocker(cfg, socket)
	if e != nil {
		fail(c, 400, e)
		return
	}
	defer remote.Close()
	ctx := c.Request.Context()
	inspect, _, e := remote.Client.ImageInspectWithRaw(ctx, in.ID)
	if e != nil {
		fail(c, 404, fmt.Errorf("镜像不存在或已删除"))
		return
	}
	if inspect.Config == nil || inspect.Config.Labels[build.LabelSourceID] == "" {
		fail(c, 400, fmt.Errorf("只能删除本管理端构建的业务镜像"))
		return
	}
	containers, e := remote.Client.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		fail(c, 400, e)
		return
	}
	for _, container := range containers {
		if container.ImageID == inspect.ID {
			fail(c, 409, fmt.Errorf("镜像正在被容器 %s 使用，不能删除", strings.Join(container.Names, "、")))
			return
		}
	}
	if _, e = remote.Client.ImageRemove(ctx, inspect.ID, image.RemoveOptions{Force: false, PruneChildren: false}); e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, true)
}
