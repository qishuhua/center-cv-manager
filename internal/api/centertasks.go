package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// centerTasks 查询中心业务任务的运行状态：经中心网关只读拉取 ingress
// 聚合的全部任务清单（数据源为中心 Redis）。只读、不改远端状态，viewer
// 可访问；网关不可达或软件版本不支持时返回 502 并带可操作的错误信息。
// 整包返回，分页展示由前端完成（任务量千级）。
func (s *Server) centerTasks(c *gin.Context) {
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	tasks, err := s.cvTasks.List(c.Request.Context(), center)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	ok(c, tasks)
}
