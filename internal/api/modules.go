package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/provision"
	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func (s *Server) listModules(c *gin.Context) {
	v, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	files, err := s.provisioning.ListComposePublications(ctx, v.ID)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, files)
}
func (s *Server) saveModuleMetadata(c *gin.Context) {
	var m domain.ComposeModule
	if err := c.ShouldBindJSON(&m); err != nil {
		fail(c, 400, err)
		return
	}
	if c.Param("module") != "" {
		m.ID = c.Param("module")
	}
	if err := s.provisioning.SaveModuleMetadata(c.Request.Context(), c.Param("id"), m); err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, true)
}
func (s *Server) readModule(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	file := c.DefaultQuery("file", "compose.yml")
	v, err := s.provisioning.ReadModule(ctx, c.Param("id"), c.Param("module"), file)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, v)
}
func (s *Server) saveModule(c *gin.Context) {
	var in struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	v, err := s.provisioning.SaveModule(ctx, c.Param("id"), c.Param("module"), c.DefaultQuery("file", "compose.yml"), in.Content, in.Revision)
	if err != nil {
		fail(c, 409, err)
		return
	}
	ok(c, v)
}
func (s *Server) moduleAction(c *gin.Context) {
	var in struct {
		Action   string   `json:"action"`
		Services []string `json:"services"`
		Revision string   `json:"revision"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if len(in.Services) > 0 {
		fail(c, 400, fmt.Errorf("Compose 必须整体发布，不支持单独操作服务"))
		return
	}
	if in.Action != "deploy" && in.Action != "restart" && in.Action != "stop" {
		fail(c, 400, fmt.Errorf("无效的模块操作"))
		return
	}
	id, moduleID := c.Param("id"), c.Param("module")
	v, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	if _, err = modular.Find(v, moduleID); err != nil {
		fail(c, 404, err)
		return
	}
	// Params retain the exact authorized scope for retry.
	params := moduleTaskParams(id, v.Name, moduleID, in.Action, in.Services)
	params["revision"] = in.Revision
	ok(c, s.tasks.Submit("MODULE_OPERATION", params, func(ctx context.Context, h *task.Handle) error {
		return s.provisioning.ModuleAction(ctx, id, moduleID, in.Action, in.Services, in.Revision, h)
	}))
}

func moduleTaskParams(id, centerName, moduleID, action string, services []string) map[string]string {
	data, _ := json.Marshal(services)
	return map[string]string{"center_id": id, "center_name": centerName, "module_id": moduleID, "action": action, "services": string(data)}
}

// mcpModuleAction 是 MCP 的单步模块操作入口：内部先经 PlanModule 取操作计划并把
// revision 绑进任务（配置变化后任务会被拒绝执行，与网页两步流程同一套保护），再提交
// MODULE_OPERATION。与网页端 plan/confirm 不同，确认环节由 MCP 客户端在调用前完成
//（工具标注 destructiveHint）；仅支持模块整体操作，不接收服务清单。
func (s *Server) mcpModuleAction(c *gin.Context) {
	var in struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if in.Action != "deploy" && in.Action != "restart" && in.Action != "stop" {
		fail(c, 400, fmt.Errorf("无效的模块操作"))
		return
	}
	id, moduleID := c.Param("id"), c.Param("module")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	plan, err := s.provisioning.PlanModule(ctx, id, moduleID, in.Action, nil)
	if err != nil {
		fail(c, 400, err)
		return
	}
	v, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	params := moduleTaskParams(id, v.Name, moduleID, in.Action, nil)
	params["revision"] = plan.Revision
	params["operator"] = releaseOperator(c)
	ok(c, s.tasks.Submit("MODULE_OPERATION", params, func(ctx context.Context, h *task.Handle) error {
		return s.provisioning.ModuleAction(ctx, id, moduleID, in.Action, nil, plan.Revision, h)
	}))
}

func (s *Server) migrationPlan(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	plan, err := s.provisioning.PlanMigration(ctx, c.Param("id"))
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, plan)
}

func (s *Server) migrateCenter(c *gin.Context) {
	var in struct {
		Revision string `json:"revision"`
		Rollback bool   `json:"rollback"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	id := c.Param("id")
	center, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	action := "migrate"
	if in.Rollback {
		action = "rollback"
	}
	ok(c, s.tasks.Submit("MODULE_MIGRATION", map[string]string{"center_id": id, "center_name": center.Name, "revision": in.Revision, "action": action}, func(ctx context.Context, h *task.Handle) error {
		if in.Rollback {
			return s.provisioning.RollbackMigration(ctx, id, h)
		}
		return s.provisioning.Migrate(ctx, id, in.Revision, h)
	}))
}

func (s *Server) planModule(c *gin.Context) {
	var in struct {
		Action   string   `json:"action"`
		Services []string `json:"services"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	plan, err := s.provisioning.PlanModule(ctx, c.Param("id"), c.Param("module"), in.Action, in.Services)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, plan)
}

func (s *Server) moduleHistory(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	files, err := s.provisioning.ModuleHistory(ctx, c.Param("id"), c.Param("module"))
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, files)
}
func (s *Server) deleteModule(c *gin.Context) {
	if err := s.provisioning.DeleteModule(c.Request.Context(), c.Param("id"), c.Param("module")); err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, true)
}

func (s *Server) prepareModuleFiles(c *gin.Context) {
	var in provision.ModuleSetup
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if err := in.Validate(); err != nil {
		fail(c, 400, err)
		return
	}
	id := c.Param("id")
	center, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	ok(c, s.tasks.Submit("PREPARE_MODULES", map[string]string{"center_id": id, "center_name": center.Name}, func(ctx context.Context, h *task.Handle) error {
		return centerlock.Within(ctx, id, func(ctx context.Context) error { return s.provisioning.PrepareModuleFiles(ctx, id, in, h) })
	}))
}

func (s *Server) planComposePublish(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	plan, err := s.provisioning.PlanComposePublish(ctx, c.Param("id"), c.Param("module"))
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, plan)
}
func (s *Server) publishCompose(c *gin.Context) {
	var in struct {
		Revision         string `json:"revision"`
		ConfirmUnchanged bool   `json:"confirm_unchanged"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	id, file := c.Param("id"), c.Param("module")
	center, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	params := map[string]string{"center_id": id, "center_name": center.Name, "module_id": file, "action": "publish"}
	ok(c, s.tasks.Submit("COMPOSE_PUBLISH", params, func(ctx context.Context, h *task.Handle) error {
		return s.provisioning.PublishCompose(ctx, id, file, in.Revision, in.ConfirmUnchanged, h)
	}))
}

// mcpComposePublish 是 MCP 的单步 Compose 整体发布入口：先经 PlanComposePublish 取
// 计划并绑定 revision（配置在执行前变化会被拒绝），再提交 COMPOSE_PUBLISH。内容未变
// 时须 confirm_unchanged=true 才会整体重启，与网页两步流程同一套保护；确认环节由
// MCP 客户端在调用前完成（工具标注 destructiveHint）。
func (s *Server) mcpComposePublish(c *gin.Context) {
	var in struct {
		ConfirmUnchanged bool `json:"confirm_unchanged"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	id, file := c.Param("id"), c.Param("module")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	plan, err := s.provisioning.PlanComposePublish(ctx, id, file)
	if err != nil {
		fail(c, 400, err)
		return
	}
	center, err := s.centers.GetPublic(id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	params := map[string]string{"center_id": id, "center_name": center.Name, "module_id": file, "action": "publish", "operator": releaseOperator(c)}
	ok(c, s.tasks.Submit("COMPOSE_PUBLISH", params, func(ctx context.Context, h *task.Handle) error {
		return s.provisioning.PublishCompose(ctx, id, file, plan.Revision, in.ConfirmUnchanged, h)
	}))
}
