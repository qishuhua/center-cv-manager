package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

type Reporter interface {
	Log(string)
	Logf(string, ...any)
}

// PullItem 是一次待分发的镜像：仓库相对路径、不可变 digest 与中心上的本地
// 发布标签（与既有远端构建产物同名，发布校验逻辑保持不变）。
type PullItem struct {
	Repository string
	Digest     string
	LocalTag   string
}

// PullAndTag 经已建立的远端转发地址把镜像按 digest 拉到中心 Docker，再重打
// 成本地发布标签。按 digest 拉取保证内容寻址，杜绝同名标签旧镜像蒙混过关；
// 拉取后复核本地 RepoDigest 与仓库一致。
func PullAndTag(ctx context.Context, docker *client.Client, forwardAddr string, items []PullItem, r Reporter) error {
	for _, item := range items {
		reference := forwardAddr + "/" + item.Repository + "@" + item.Digest
		r.Logf("拉取 %s → %s", reference, item.LocalTag)
		stream, err := docker.ImagePull(ctx, reference, image.PullOptions{})
		if err != nil {
			return fmt.Errorf("拉取 %s 失败: %w", item.Repository, err)
		}
		if err = logPullProgress(stream, r); err != nil {
			return fmt.Errorf("拉取 %s 失败: %w", item.Repository, err)
		}
		if err = docker.ImageTag(ctx, reference, item.LocalTag); err != nil {
			return fmt.Errorf("重打本地标签 %s 失败: %w", item.LocalTag, err)
		}
		inspect, _, err := docker.ImageInspectWithRaw(ctx, item.LocalTag)
		if err != nil {
			return fmt.Errorf("校验 %s 失败: %w", item.LocalTag, err)
		}
		if !hasRepoDigest(inspect.RepoDigests, item.Digest) {
			return fmt.Errorf("镜像 %s 的实际 digest 与镜像仓库不一致", item.LocalTag)
		}
		r.Logf("%s 已就绪（%s）", item.LocalTag, shortDigest(item.Digest))
	}
	return nil
}

// logPullProgress 消费 dockerd 的 JSON 进度流：层级进度条逐条重复，只记录
// 关键节点（Pulling from / Digest / Status / 层完成态），与 docker CLI 输出
// 关键行一致。
func logPullProgress(stream io.ReadCloser, r Reporter) error {
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	reported := map[string]bool{}
	for {
		var message struct {
			Status      string `json:"status"`
			ID          string `json:"id"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if message.Error != "" {
			return fmt.Errorf("%s", message.Error)
		}
		if message.ErrorDetail != nil && message.ErrorDetail.Message != "" {
			return fmt.Errorf("%s", message.ErrorDetail.Message)
		}
		if message.Status == "" {
			continue
		}
		if message.ID == "" {
			r.Log(message.Status)
			continue
		}
		if message.Status == "Already exists" || message.Status == "Pull complete" {
			key := message.ID + ":" + message.Status
			if !reported[key] {
				reported[key] = true
				r.Logf("%s: %s", message.ID, message.Status)
			}
		}
	}
}

func hasRepoDigest(repoDigests []string, digest string) bool {
	for _, value := range repoDigests {
		if index := strings.Index(value, "@"); index >= 0 && value[index+1:] == digest {
			return true
		}
	}
	return false
}
