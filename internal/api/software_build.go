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

// deleteCenterSoftwareImage 删除中心上由本管理端构建的业务镜像，支持多选：
// 批量 {"ids":[...]} 与旧版单镜像 {"id":"..."} 入参可并存，合并去重。
// 只允许删除带来源标签的软件镜像；被任何容器（含已停止）引用的镜像拒绝删除；
// 多选时先整体校验，任一镜像不满足条件则全部不删除。
// 删除按镜像 ID force 执行：分发链路按 digest 拉取后 retag，镜像 ID 除本地
// 发布标签外还挂着拉取产生的 digest 引用（同内容多版本标签同样叠加引用），
// 非 force 删除会被 daemon 以 referenced in multiple repositories 拒绝。
// 前置校验已排除非本管理端镜像与容器占用，force 只解除本镜像自身的全部
// 引用，不影响其他镜像。
func (s *Server) deleteCenterSoftwareImage(c *gin.Context) {
	var in struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, fmt.Errorf("请选择要删除的镜像"))
		return
	}
	targets := make([]string, 0, len(in.IDs)+1)
	for _, id := range append(in.IDs, in.ID) {
		if id = strings.TrimSpace(id); id != "" {
			targets = append(targets, id)
		}
	}
	if targets = uniqueStrings(targets); len(targets) == 0 {
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
	containers, e := remote.Client.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		fail(c, 400, e)
		return
	}
	usersByImage := map[string][]string{}
	for _, item := range containers {
		usersByImage[item.ImageID] = append(usersByImage[item.ImageID], cleanContainerNames(item.Names)...)
	}
	// 先整体校验再逐个删除：问题按镜像汇总（标签便于对照列表），任一
	// 不满足即全部不删，避免删一半才发现有占用。
	type imageProblem struct {
		status  int
		message string
	}
	deletable, labels := make([]string, 0, len(targets)), map[string]string{}
	problems := make([]imageProblem, 0)
	for _, id := range targets {
		inspect, _, e := remote.Client.ImageInspectWithRaw(ctx, id)
		if e != nil {
			problems = append(problems, imageProblem{404, imageLabel(id, nil) + "：镜像不存在或已删除"})
			continue
		}
		if inspect.Config == nil || inspect.Config.Labels[build.LabelSourceID] == "" {
			problems = append(problems, imageProblem{400, imageLabel(inspect.ID, inspect.RepoTags) + "：只能删除本管理端构建的业务镜像"})
			continue
		}
		if users, used := usersByImage[inspect.ID]; used {
			problems = append(problems, imageProblem{409, fmt.Sprintf("%s：镜像正在被容器 %s 使用，不能删除", imageLabel(inspect.ID, inspect.RepoTags), strings.Join(users, "、"))})
			continue
		}
		// 同一镜像可能以标签与 ID 两种引用传入，按 inspect.ID 去重后只删一次。
		if labels[inspect.ID] == "" {
			labels[inspect.ID] = imageLabel(inspect.ID, inspect.RepoTags)
			deletable = append(deletable, inspect.ID)
		}
	}
	if len(problems) > 0 {
		messages := make([]string, 0, len(problems))
		for _, problem := range problems {
			messages = append(messages, problem.message)
		}
		// 单镜像沿用原状态码（404 不存在 / 400 非本管理端 / 409 被占用）；多选整体校验失败统一 400。
		status := 400
		if len(targets) == 1 {
			status = problems[0].status
		}
		fail(c, status, fmt.Errorf("以下镜像不能删除：%s", strings.Join(messages, "；")))
		return
	}
	for index, imageID := range deletable {
		if _, e = remote.Client.ImageRemove(ctx, imageID, image.RemoveOptions{Force: true, PruneChildren: false}); e != nil {
			fail(c, 400, fmt.Errorf("已删除 %d/%d 个镜像，%s 删除失败: %w", index, len(deletable), labels[imageID], e))
			return
		}
	}
	ok(c, true)
}

// imageLabel 取镜像在提示信息中的标识：优先首个仓库标签（与镜像管理列表
// 展示一致），无标签时退回 ID 缩写。
func imageLabel(id string, repoTags []string) string {
	if len(repoTags) > 0 {
		return repoTags[0]
	}
	return shortImageID(id)
}

// shortImageID 去掉 sha256: 前缀取 12 位缩写（与 commit 缩写同长）。
func shortImageID(id string) string {
	trimmed := strings.TrimPrefix(id, "sha256:")
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
}
