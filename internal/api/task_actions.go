package api

import (
	"github.com/gin-gonic/gin"
)

func (s *Server) cancelAllTasks(c *gin.Context) { ok(c, s.tasks.CancelAll()) }

func (s *Server) deleteTask(c *gin.Context) {
	if err := s.tasks.Delete(c.Param("id")); err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, true)
}

// 任务不提供重试：失败任务的原参数快照可能已过时（版本未发布、源码已删除、
// 配置已变更），一律回到对应入口重新提交；运行中的任务可停止。
