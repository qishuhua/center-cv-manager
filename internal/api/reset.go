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
	finish := notifyCenterOperation(s.alerts, "⚙️", "重置中心", releaseOperator(c), center.Name, "将删除受管 Compose 容器与部署配置，服务立即下线；镜像、数据卷与模型文件保留。")
	ok(c, s.tasks.Submit("RESET_CENTER", map[string]string{"center_id": center.ID, "center_name": center.Name, "confirm_reset": "true"}, func(ctx context.Context, h *task.Handle) error {
		err := s.runCenterReset(ctx, center.ID, h)
		finish(err, "")
		return err
	}))
}

func (s *Server) runCenterReset(ctx context.Context, id string, h *task.Handle) error {
	return centerlock.Within(ctx, id, func(ctx context.Context) error {
		if err := s.provisioning.ResetCenter(ctx, id, h); err != nil {
			return err
		}
		if _, _, err := s.models.SetModelSetPinned(ctx, id, false); err != nil {
			return fmt.Errorf("中心已重置，但模型版本组解绑失败，请重试重置: %w", err)
		}
		return nil
	})
}
