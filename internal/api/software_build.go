package api

import (
	"fmt"
	"sort"
	"strings"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/sshx"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/gin-gonic/gin"
)

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
