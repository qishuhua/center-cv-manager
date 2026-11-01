package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/task"

	"github.com/gin-gonic/gin"
)

func (s *Server) getModelConvertSettings(c *gin.Context) {
	settings, err := s.models.ConvertSettings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, settings)
}

func (s *Server) saveModelConvertSettings(c *gin.Context) {
	var value domain.ModelConvertSettings
	if err := c.ShouldBindJSON(&value); err != nil {
		fail(c, 400, err)
		return
	}
	for _, platform := range []struct {
		name   string
		conf   domain.ModelConvertPlatform
	}{
		{"nvidia", value.Nvidia},
		{"denglin", value.Denglin},
	} {
		if platform.conf.CenterID == "" && platform.conf.Image == "" && platform.conf.Command == "" {
			continue // 未配置的平台整组留空
		}
		if !platform.conf.Configured() {
			fail(c, 400, errors.New(platform.name+" 平台转换配置不完整：构建机、镜像、命令均不能为空"))
			return
		}
		if _, err := s.centers.GetPublic(platform.conf.CenterID); err != nil {
			fail(c, 400, fmt.Errorf("%s 平台构建机不存在: %w", platform.name, err))
			return
		}
	}
	value.UpdatedAt = time.Now()
	if err := s.models.SaveConvertSettings(value); err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, value)
}

// convertModel 把模型版本的 ONNX 在指定平台构建机上转换为 TensorRT/登临格式并作为
// 新版本入仓；构建机占用与转换进度在任务日志中体现，源版本摘要不匹配立即拒绝。
func (s *Server) convertModel(c *gin.Context) {
	var in struct {
		Platform       string `json:"platform"`
		ManifestKey    string `json:"manifest_key"`
		ManifestDigest string `json:"manifest_digest"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if in.Platform != "nvidia" && in.Platform != "denglin" {
		fail(c, 400, errors.New("platform 必须是 nvidia 或 denglin"))
		return
	}
	settings, err := s.models.ConvertSettings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	conf := settings.Denglin
	if in.Platform == "nvidia" {
		conf = settings.Nvidia
	}
	if !conf.Configured() {
		fail(c, 400, errors.New(in.Platform+" 平台转换未配置，请先在模型仓库页保存转换设置"))
		return
	}
	buildCenter, err := s.centers.GetPublic(conf.CenterID)
	if err != nil {
		fail(c, 400, fmt.Errorf("%s 平台构建机不存在: %w", in.Platform, err))
		return
	}
	versions, err := s.models.Versions()
	if err != nil {
		fail(c, 500, err)
		return
	}
	var source modelasset.Version
	found := false
	for _, version := range versions {
		if version.ManifestKey == in.ManifestKey {
			source, found = version, true
			break
		}
	}
	if !found {
		fail(c, 404, errors.New("模型版本不存在或尚未就绪"))
		return
	}
	if in.ManifestDigest == "" || in.ManifestDigest != source.ManifestDigest {
		fail(c, 409, errors.New("模型 Manifest 已变化，请刷新后重新转换"))
		return
	}
	platform, manifestKey, manifestDigest := in.Platform, in.ManifestKey, in.ManifestDigest
	platformLabel := map[string]string{"nvidia": "NVIDIA TensorRT", "denglin": "登临 engine"}[platform]
	submitted := s.tasks.Submit("MODEL_CONVERT", map[string]string{
		"platform": platformLabel, "manifest_key": manifestKey,
		"model": source.Model, "source_version": source.Version,
		"build_center": buildCenter.Name, "operator": releaseOperator(c),
	}, func(ctx context.Context, h *task.Handle) error {
		version, err := s.models.ConvertVersion(ctx, platform, manifestKey, manifestDigest, h)
		if err != nil {
			return err
		}
		h.Logf("新版本 %s:%s 已入仓（digest %s），可在版本发布页选择发布", version.Model, version.Version, version.ManifestDigest[:12])
		h.Message("模型转换完成")
		return nil
	})
	ok(c, submitted)
}
