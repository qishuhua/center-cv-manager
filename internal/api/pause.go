package api

import (
	"context"
	"fmt"

	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func (s *Server) pauseCenter(c *gin.Context) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || !in.Confirm {
		fail(c, 400, fmt.Errorf("请确认暂停中心：全部受管服务将停止、容器移除，数据卷、镜像与配置保留"))
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	finish := notifyCenterOperation(s.notificationSender(center), "⏸️", "暂停中心", releaseOperator(c), center.Name, "全部受管服务将按依赖反序停止并移除容器，外部访问随之中断；数据卷、镜像与配置保留，可随时恢复。")
	ok(c, s.tasks.Submit("PAUSE_CENTER", map[string]string{"center_id": center.ID, "center_name": center.Name, "confirm": "true"}, func(ctx context.Context, h *task.Handle) error {
		err := s.provisioning.PauseCenter(ctx, center.ID, h)
		finish(err, "")
		return err
	}))
}

func (s *Server) resumeCenter(c *gin.Context) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || !in.Confirm {
		fail(c, 400, fmt.Errorf("请确认恢复中心：将按模块依赖顺序重新启动全部服务并等待就绪"))
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	finish := notifyCenterOperation(s.notificationSender(center), "▶️", "恢复中心", releaseOperator(c), center.Name, "将按模块依赖顺序重新启动全部服务并等待就绪。")
	ok(c, s.tasks.Submit("RESUME_CENTER", map[string]string{"center_id": center.ID, "center_name": center.Name, "confirm": "true"}, func(ctx context.Context, h *task.Handle) error {
		err := s.provisioning.ResumeCenter(ctx, center.ID, h)
		finish(err, "")
		return err
	}))
}
