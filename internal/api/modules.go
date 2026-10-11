package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func (s *Server) listModules(c *gin.Context) {
	v, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	ok(c, modular.Modules(v))
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
		Action        string   `json:"action"`
		Services      []string `json:"services"`
		Revision      string   `json:"revision"`
		AdminPassword string   `json:"admin_password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if in.Action != "deploy" && in.Action != "restart" && in.Action != "stop" {
		fail(c, 400, fmt.Errorf("无效的模块操作"))
		return
	}
	token, _ := c.Cookie(authCookieName)
	if err := s.auth.VerifyPassword(token, in.AdminPassword); err != nil {
		fail(c, 403, err)
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
	params := moduleTaskParams(id, moduleID, in.Action, in.Services)
	params["revision"] = in.Revision
	ok(c, s.tasks.Submit("MODULE_OPERATION", params, func(ctx context.Context, h *task.Handle) error {
		return s.provisioning.ModuleAction(ctx, id, moduleID, in.Action, in.Services, in.Revision, h)
	}))
}

func moduleTaskParams(id, moduleID, action string, services []string) map[string]string {
	data, _ := json.Marshal(services)
	return map[string]string{"center_id": id, "module_id": moduleID, "action": action, "services": string(data)}
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
		Revision      string `json:"revision"`
		AdminPassword string `json:"admin_password"`
		Rollback      bool   `json:"rollback"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	token, _ := c.Cookie(authCookieName)
	if err := s.auth.VerifyPassword(token, in.AdminPassword); err != nil {
		fail(c, 403, err)
		return
	}
	id := c.Param("id")
	action := "migrate"
	if in.Rollback {
		action = "rollback"
	}
	ok(c, s.tasks.Submit("MODULE_MIGRATION", map[string]string{"center_id": id, "revision": in.Revision, "action": action}, func(ctx context.Context, h *task.Handle) error {
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
