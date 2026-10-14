package api

import (
	"context"
	"encoding/json"
	"fmt"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/task"
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

func (s *Server) retryTask(c *gin.Context) {
	previous, found := s.tasks.Get(c.Param("id"))
	if !found {
		fail(c, 404, fmt.Errorf("任务不存在"))
		return
	}
	if previous.Status != task.Failed {
		fail(c, 409, fmt.Errorf("只有失败的任务可以重试"))
		return
	}
	p := make(map[string]string, len(previous.Params)+1)
	for k, v := range previous.Params {
		p[k] = v
	}
	p["retry_of"] = previous.ID
	var run task.Executor
	switch previous.Type {
	case "DOWNLOAD_SOURCE":
		src, err := s.store.GetSource(p["source_id"])
		if err != nil {
			s.storeError(c, err)
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			_, err := s.sources.Download(ctx, src, p["ref"], h)
			return err
		}
	case "INITIALIZE_CENTER":
		center, err := s.centers.GetPublic(p["center_id"])
		if err != nil {
			s.storeError(c, err)
			return
		}
		var items []deploy.Item
		var models []modelasset.ReleaseItem
		if json.Unmarshal([]byte(p["items"]), &items) != nil || json.Unmarshal([]byte(p["models"]), &models) != nil {
			fail(c, 400, fmt.Errorf("旧初始化任务未保存版本选择，请重新打开初始化部署选择版本"))
			return
		}
		result, err := s.submitCenterInitialization(center, items, models)
		if err != nil {
			fail(c, 409, err)
			return
		}
		ok(c, result)
		return
	case "BASE_IMAGE_UPDATE":
		run = func(ctx context.Context, h *task.Handle) error {
			return s.baseImages.UpgradeCenter(ctx, p["center_id"], p["restart_stack"] == "true", h)
		}
	case "MODEL_SYNC":
		var keys []string
		if json.Unmarshal([]byte(p["keys"]), &keys) != nil || len(keys) == 0 {
			fail(c, 400, fmt.Errorf("任务未保存模型列表，请在模型仓库中重新选择并发布"))
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			return s.models.SyncCenter(ctx, p["center_id"], keys, h)
		}
	case "MODEL_RELEASE":
		var items []modelasset.ReleaseItem
		if json.Unmarshal([]byte(p["items"]), &items) != nil || len(items) == 0 {
			fail(c, 400, fmt.Errorf("旧任务未保存模型版本快照，请重新创建模型发布"))
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			return s.runModelRelease(ctx, p["center_id"], items, p["restart_stack"] == "true", h)
		}
	case "BUILD_IMAGE":
		checkout, err := s.store.GetCheckout(p["source_id"], p["ref"])
		if err != nil {
			s.storeError(c, err)
			return
		}
		var args map[string]string
		if p["build_args"] == "" {
			fail(c, 400, fmt.Errorf("旧任务未保存完整构建参数，请在镜像构建中重新提交"))
			return
		}
		if err := json.Unmarshal([]byte(p["build_args"]), &args); err != nil {
			fail(c, 400, err)
			return
		}
		options := build.Options{CenterID: p["center_id"], ContextDir: checkout.Path, Dockerfile: p["dockerfile"], ImageTag: p["image_tag"], NoCache: p["no_cache"] == "true", BuildArgs: args, SourceID: p["source_id"], SourceName: p["source_name"], SourceRef: p["ref"], Commit: checkout.Commit}
		run = func(ctx context.Context, h *task.Handle) error { return s.builds.Run(ctx, options, h) }
	case "DEPLOY_STACK":
		var items []deploy.Item
		if json.Unmarshal([]byte(p["items"]), &items) != nil || len(items) == 0 {
			fail(c, 400, fmt.Errorf("旧任务未保存部署版本列表，请在部署任务中重新提交"))
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			_, err := s.deployments.Run(ctx, p["center_id"], items, h)
			return err
		}
	case "RELEASE_VERSION":
		var items []deploy.Item
		if p["items"] != "" && json.Unmarshal([]byte(p["items"]), &items) != nil {
			fail(c, 400, fmt.Errorf("任务保存的软件版本快照无效，请重新创建版本发布"))
			return
		}
		var modelItems []modelasset.ReleaseItem
		if p["model_items"] != "" && json.Unmarshal([]byte(p["model_items"]), &modelItems) != nil {
			fail(c, 400, fmt.Errorf("任务保存的模型版本快照无效，请重新创建版本发布"))
			return
		}
		if len(items) == 0 && len(modelItems) == 0 {
			fail(c, 400, fmt.Errorf("旧任务未保存软件或模型版本快照，请重新创建版本发布"))
			return
		}
		for _, item := range items {
			if item.Commit == "" {
				fail(c, 400, fmt.Errorf("旧任务未锁定源码 commit，请重新创建版本发布"))
				return
			}
		}
		run = func(ctx context.Context, h *task.Handle) error {
			return s.runRelease(ctx, p["center_id"], items, modelItems, h)
		}
	case "RESET_CENTER":
		if p["confirm_reset"] != "true" {
			fail(c, 400, fmt.Errorf("重置任务缺少确认记录，请重新点击重置并确认"))
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			return s.runCenterReset(ctx, p["center_id"], h)
		}
	case "MODULE_MIGRATION":
		run = func(ctx context.Context, h *task.Handle) error {
			if p["action"] == "rollback" {
				return s.provisioning.RollbackMigration(ctx, p["center_id"], h)
			}
			return s.provisioning.Migrate(ctx, p["center_id"], p["revision"], h)
		}
	case "MODULE_OPERATION":
		var services []string
		if err := json.Unmarshal([]byte(p["services"]), &services); err != nil {
			fail(c, 400, err)
			return
		}
		run = func(ctx context.Context, h *task.Handle) error {
			return s.provisioning.ModuleAction(ctx, p["center_id"], p["module_id"], p["action"], services, p["revision"], h)
		}
	case "CENTER_OPERATION":
		// Replay only the stored, previously authorized operation; no new action parameters are accepted.
		run = func(ctx context.Context, h *task.Handle) error {
			return s.centerActions.Run(ctx, p["center_id"], p["action"], p["container_id"], h)
		}
	default:
		fail(c, 400, fmt.Errorf("不支持重试此任务类型"))
		return
	}
	ok(c, s.tasks.Submit(previous.Type, p, run))
}
