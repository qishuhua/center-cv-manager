package api

import (
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/task"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
)

func (s *Server) resetCenterDeployment(c *gin.Context) {
	var in struct {
		ConfirmReset bool `json:"confirm_reset"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || !in.ConfirmReset {
		fail(c, 400, fmt.Errorf("请确认删除部署配置和受管 Compose 容器；镜像、数据卷及模型文件保留"))
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	ok(c, s.tasks.Submit("RESET_CENTER", map[string]string{"center_id": center.ID, "center_name": center.Name, "confirm_reset": "true"}, func(ctx context.Context, h *task.Handle) error { return s.runCenterReset(ctx, center.ID, h) }))
}

func (s *Server) runCenterReset(ctx context.Context, id string, h *task.Handle) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		if err := s.provisioning.ResetCenter(ctx, id, h); err != nil {
			return err
		}
		if _, err := s.models.SetModelSetPinned(ctx, id, false); err != nil {
			return fmt.Errorf("中心已重置，但模型版本组解绑失败，请重试重置: %w", err)
		}
		return nil
	})
}
