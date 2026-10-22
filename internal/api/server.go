package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"center-cv-manager/internal/alerting"
	"center-cv-manager/internal/auth"
	"center-cv-manager/internal/baseimage"
	"center-cv-manager/internal/build"
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centeraction"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/monitor"
	"center-cv-manager/internal/provision"
	"center-cv-manager/internal/registry"
	"center-cv-manager/internal/source"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
)

type Server struct {
	initializationMu sync.Mutex
	auth             *auth.Manager
	centers          *center.Service
	centerActions    *centeraction.Service
	sources          *source.Service
	builds           *build.Service
	deployments      *deploy.Service
	provisioning     *provision.Service
	baseImages       *baseimage.Service
	models           *modelasset.Service
	alerts           *alerting.Service
	registries       *registry.Service
	monitors         *monitor.Service
	store            *store.Store
	tasks            *task.Manager
	sourceRoot       string
	static           fs.FS
}

func New(authManager *auth.Manager, centers *center.Service, centerActions *centeraction.Service, sources *source.Service, builds *build.Service, deployments *deploy.Service, provisioning *provision.Service, baseImages *baseimage.Service, models *modelasset.Service, alerts *alerting.Service, registries *registry.Service, monitors *monitor.Service, st *store.Store, tasks *task.Manager, sourceRoot string, static fs.FS) *Server {
	return &Server{auth: authManager, centers: centers, centerActions: centerActions, sources: sources, builds: builds, deployments: deployments, provisioning: provisioning, baseImages: baseImages, models: models, alerts: alerts, registries: registries, monitors: monitors, store: st, tasks: tasks, sourceRoot: sourceRoot, static: static}
}

type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, envelope{Code: 0, Message: "ok", Data: data})
}
func fail(c *gin.Context, status int, err error) {
	c.JSON(status, envelope{Code: status, Message: err.Error()})
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Logger(), gin.Recovery())
	root := r.Group("/api/v1")
	root.GET("/health", func(c *gin.Context) { ok(c, map[string]any{"status": "ok", "time": time.Now()}) })
	root.GET("/auth/status", s.authStatus)
	root.POST("/auth/login", s.login)
	root.POST("/auth/logout", s.logout)
	root.POST("/auth/change-password", s.changePassword)
	root.POST("/auth/skip-password-change", s.skipPasswordChange)
	// 查看用户可访问的只读数据接口；硬件检测只读取远端状态并刷新本地缓存。
	api := root.Group("")
	api.Use(s.requireAuth())
	api.GET("/centers", s.listCenters)
	api.POST("/centers/:id/detect", s.detectCenter)
	api.GET("/centers/:id/modules", s.listModules)
	api.GET("/centers/:id/modules/:module/revisions", s.moduleHistory)
	api.GET("/centers/:id/modules/:module/compose", s.readModule)
	api.GET("/centers/:id/compose", s.getCenterCompose)
	api.GET("/centers/:id/images", s.centerImages)
	api.GET("/centers/:id/software/images", s.centerSoftwareImages)
	api.GET("/centers/:id/software/registry-versions", s.centerRegistryVersions)
	api.GET("/centers/:id/overview", s.centerOverview)
	api.GET("/centers/:id/models", s.centerModels)
	api.GET("/overview", s.overview)
	api.GET("/sources", s.listSources)
	api.GET("/sources/:id/versions", s.versions)
	api.GET("/sources/:id/checkouts", s.checkouts)
	api.GET("/base-images", s.baseImageCatalog)
	api.GET("/models", s.modelCatalog)
	api.GET("/models/empty-folders", s.emptyModelFolders)
	api.GET("/tasks", s.listTasks)
	api.GET("/tasks/:id", s.getTask)
	api.GET("/tasks/:id/logs", s.taskLogs)
	api.GET("/tasks/:id/events", s.taskEvents)
	api.GET("/settings/status", s.settingsStatus)
	api.GET("/registry/versions", s.listRegistryVersions)
	api.GET("/monitoring/status", s.monitoringStatus)

	// 管理员专属：一切变更操作、凭据、SSH 终端和用户管理。
	admin := api.Group("")
	admin.Use(s.requireAdmin())
	admin.POST("/centers", s.saveCenter)
	admin.PUT("/centers/:id", s.saveCenter)
	admin.DELETE("/centers/:id", s.deleteCenter)
	admin.POST("/centers/:id/test", s.testCenter)
	admin.POST("/centers/:id/initialize", s.initializeCenter)
	admin.POST("/centers/:id/reset", s.resetCenterDeployment)
	admin.POST("/centers/:id/pause", s.pauseCenter)
	admin.POST("/centers/:id/resume", s.resumeCenter)
	admin.POST("/centers/:id/actions", s.runCenterAction)
	admin.POST("/centers/:id/modules/:module/publish-plan", s.planComposePublish)
	admin.POST("/centers/:id/modules/:module/publish", s.publishCompose)
	admin.POST("/centers/:id/modules/prepare", s.prepareModuleFiles)
	admin.POST("/centers/:id/migration/plan", s.migrationPlan)
	admin.POST("/centers/:id/migration", s.migrateCenter)
	admin.POST("/centers/:id/modules", s.saveModuleMetadata)
	admin.PUT("/centers/:id/modules/:module", s.saveModuleMetadata)
	admin.DELETE("/centers/:id/modules/:module", s.deleteModule)
	admin.PUT("/centers/:id/modules/:module/compose", s.saveModule)
	admin.POST("/centers/:id/modules/:module/actions", s.moduleAction)
	admin.POST("/centers/:id/modules/:module/plan", s.planModule)
	admin.PUT("/centers/:id/compose", s.saveCenterCompose)
	admin.PUT("/centers/:id/models/pin", s.setCenterModelPin)
	admin.GET("/centers/:id/terminal", func(c *gin.Context) {
		centerID := c.Param("id")
		websocket.Handler(func(ws *websocket.Conn) { s.terminalSocket(centerID, ws) }).ServeHTTP(c.Writer, c.Request)
	})
	admin.GET("/git-credential", s.getGitCredential)
	admin.PUT("/git-credential", s.saveGitCredential)
	admin.GET("/oss-credential", s.getOSSCredential)
	admin.PUT("/oss-credential", s.saveOSSCredential)
	admin.GET("/registry", s.getRegistrySettings)
	admin.PUT("/registry", s.saveRegistrySettings)
	admin.GET("/registry/storage", s.getRegistryStorage)
	admin.PUT("/registry/storage", s.saveRegistryStorage)
	admin.POST("/registry/test", s.testRegistry)
	admin.DELETE("/registry/versions", s.deleteRegistryVersion)
	admin.GET("/alert-credential", s.getAlertCredential)
	admin.PUT("/alert-credential", s.saveAlertCredential)
	admin.POST("/base-images/upgrade", s.upgradeBaseImages)
	admin.DELETE("/models/:model", s.deleteModel)
	admin.POST("/model-uploads", s.createModelUpload)
	admin.PUT("/model-uploads/:id/file", s.uploadModelFile)
	admin.POST("/model-uploads/:id/finalize", s.finalizeModelUpload)
	admin.DELETE("/model-uploads/:id", s.abortModelUpload)
	admin.POST("/model-releases/plan", s.modelReleasePlan)
	admin.POST("/model-releases", s.releaseModels)
	admin.POST("/sources", s.saveSource)
	admin.PUT("/sources/:id", s.saveSource)
	admin.DELETE("/sources/:id", s.deleteSource)
	admin.POST("/sources/:id/download", s.download)
	admin.POST("/sources/:id/publish", s.publishSourceImage)
	admin.POST("/centers/:id/software/release", s.releaseCenterSoftware)
	admin.DELETE("/centers/:id/software/images", s.deleteCenterSoftwareImage)
	admin.POST("/releases/plan", s.releasePlan)
	admin.POST("/releases", s.release)
	admin.POST("/tasks/:id/cancel", s.cancelTask)
	admin.POST("/tasks/cancel-all", s.cancelAllTasks)
	admin.GET("/users", s.listUsers)
	// 账号体系与审计记录类操作仅限内建 admin 账号，不随管理权限下放。
	superAdmin := admin.Group("")
	superAdmin.Use(s.requireSuperAdmin())
	superAdmin.DELETE("/tasks/:id", s.deleteTask)
	superAdmin.POST("/users", s.createUser)
	superAdmin.PUT("/users/:username/role", s.setUserRole)
	superAdmin.POST("/users/:username/reset-password", s.resetUserPassword)
	superAdmin.DELETE("/users/:username", s.deleteUser)
	if s.static != nil {
		r.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if path == "/" {
				path = "/index.html"
			}
			data, err := fs.ReadFile(s.static, strings.TrimPrefix(path, "/"))
			if err != nil {
				data, err = fs.ReadFile(s.static, "index.html")
			}
			if err != nil {
				c.Status(404)
				return
			}
			// Revalidate the SPA entry after deployments; hashed assets are
			// immutable and safe to cache aggressively.
			if path == "/index.html" || !fs.ValidPath(strings.TrimPrefix(path, "/")) || strings.HasPrefix(strings.TrimSpace(string(data)), "<!doctype html>") {
				c.Header("Cache-Control", "no-store")
			} else if strings.HasPrefix(path, "/assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			ctype := http.DetectContentType(data)
			if filepath.Ext(path) == ".js" {
				ctype = "application/javascript"
			} else if filepath.Ext(path) == ".css" {
				ctype = "text/css"
			}
			c.Data(200, ctype, data)
		})
	}
	return r
}
func (s *Server) listCenters(c *gin.Context) {
	v, e := s.centers.List()
	if e != nil {
		fail(c, 500, e)
		return
	}
	ok(c, v)
}
func (s *Server) saveCenter(c *gin.Context) {
	var v domain.Center
	if e := c.ShouldBindJSON(&v); e != nil {
		fail(c, 400, e)
		return
	}
	if id := c.Param("id"); id != "" {
		v.ID = id
	}
	var old domain.Center
	if v.ID != "" {
		if stored, err := s.centers.GetPublic(v.ID); err == nil {
			old = stored
		}
	}
	out, e := s.centers.Save(v)
	if e != nil {
		fail(c, 400, e)
		return
	}
	// 业务模块托管设置（错分检测、Debug 模式）同步到远端 business 模块
	//（module.env + Compose）：保存即幂等同步，不比较新旧值——远端文件
	// 无变化时只读比对、零写入，而上次同步失败的中心（记录已更新、远端
	// 仍旧值）在任意一次保存时自动重试，避免漂移被后续"值未变化"的保存
	// 掩盖。初始化前/重置后的中心等初始化渲染时带上。
	if old.ID != "" && old.Deployment != nil && out.Deployment != nil {
		status := out.Deployment.Status
		if status != "preparing" && status != "reset" {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
			defer cancel()
			if syncErr := s.provisioning.ApplyBusinessSettings(ctx, out.ID); syncErr != nil {
				log.Printf("业务模块托管设置同步失败 center=%s(%s): %v", out.ID, out.Name, syncErr)
				fail(c, 500, fmt.Errorf("中心已保存，但业务模块托管设置同步失败: %w", syncErr))
				return
			}
		}
	}
	// 统一监控：中心清单变化后重渲染 targets，prometheus file_sd 与仪表盘
	// 生成器自动生效（监控地址为空的新中心也会从这里进入渲染循环）。
	if err := s.syncMonitoring("中心已保存"); err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, out)
}

func (s *Server) submitCenterInitialization(v domain.Center, items []deploy.Item, models []modelasset.ReleaseItem, operator string) (*domain.Task, error) {
	s.initializationMu.Lock()
	defer s.initializationMu.Unlock()
	current, err := s.centers.GetPublic(v.ID)
	if err != nil {
		return nil, err
	}
	if err = validateInitialization(current, len(items)); err != nil {
		return nil, err
	}
	if s.tasks.HasActive("INITIALIZE_CENTER", "center_id", v.ID) {
		return nil, fmt.Errorf("该中心正在初始化，请查看现有任务")
	}
	softwareJSON, _ := json.Marshal(items)
	modelsJSON, _ := json.Marshal(models)
	params := map[string]string{"center_id": v.ID, "center_name": v.Name, "items": string(softwareJSON), "models": string(modelsJSON)}
	finish := notifyCenterOperation(s.notificationSender(current), "🛠️", "初始化中心", operator, v.Name, "将上传模块配置、按依赖顺序启动全部服务，并发布所选业务代码与模型版本；初始化期间服务分批上线。")
	return s.tasks.Submit("INITIALIZE_CENTER", params, func(ctx context.Context, h *task.Handle) error {
		var successDetail string
		err := centerlock.Within(ctx, v.ID, func(ctx context.Context) error {
			current, err := s.centers.GetPublic(v.ID)
			if err != nil {
				return err
			}
			if err = validateInitialization(current, len(items)); err != nil {
				return err
			}
			if err = s.provisioning.Initialize(ctx, v.ID, h); err != nil {
				return err
			}
			current, err = s.centers.GetPublic(v.ID)
			if err != nil {
				return err
			}
			if current.Deployment != nil {
				current.Deployment.ReselectionRequired = true
				if err = s.centers.UpdateDeployment(v.ID, current.Deployment); err != nil {
					return err
				}
			}
			credential, err := s.baseImages.Credential()
			if err != nil {
				return err
			}
			if credential.Configured {
				if err = s.baseImages.UpgradeCenter(ctx, v.ID, false, h); err != nil {
					return err
				}
			}
			if err = s.provisioning.ResumeModules(ctx, v.ID, h); err != nil {
				return err
			}
			var modelTargets []modelasset.ReleaseItem
			if len(models) > 0 {
				plan := s.checkUnifiedRelease(ctx, v.ID, items, models)
				if !plan.Ready {
					return fmt.Errorf("初始化版本检查失败: %s", plan.Error)
				}
				modelTargets = plan.ModelPlan.Targets
				if err = s.runReleaseLocked(ctx, v.ID, items, modelTargets, h); err != nil {
					return err
				}
			} else {
				// 未选择模型：跳过统一发布里"中心已有健康模型"的防呆（普通
				// 发布仍保留），只分发业务镜像；业务模块就绪后 ResumeModules
				// 停在等待业务发布，网关与监控等模型发布后经初始化入口续跑。
				if err = s.runSoftwareRelease(ctx, v.ID, items, h); err != nil {
					return err
				}
			}
			finished, err := s.centers.GetPublic(v.ID)
			if err != nil {
				return err
			}
			// 未选模型的初始化以等待业务发布收尾（预期成功态）；带模型初始化
			// 与其他非就绪状态仍视为失败。
			if finished.Deployment != nil && finished.Deployment.Status != "ready" &&
				!(len(models) == 0 && finished.Deployment.Status == "waiting_business") {
				return fmt.Errorf("初始化未完成，当前状态: %s", finished.Deployment.Status)
			}
			// 汇总本次实际发布的业务镜像与模型版本，随完成通知发送到飞书。
			lines := make([]string, 0, len(items)+len(modelTargets)+2)
			lines = append(lines, "业务镜像：")
			for _, item := range items {
				lines = append(lines, "- "+softwareVersionText(item.SourceName, item.Ref, item.Commit))
			}
			if len(models) == 0 {
				lines = append(lines, "模型：未部署（等待模型发布后继续完成网关与监控）")
			} else {
				lines = append(lines, "模型：")
				for _, item := range modelTargets {
					variant := item.Platform
					if item.HardwareProfile != "" {
						variant += "-" + item.HardwareProfile
					}
					lines = append(lines, fmt.Sprintf("- %s:%s[%s]@%s", item.Model, item.Version, variant, item.ManifestDigest[:12]))
				}
			}
			successDetail = strings.Join(lines, "\n")
			if len(models) == 0 {
				h.Message("初始化完成：业务镜像已部署，等待发布模型（发布后经初始化入口继续完成网关与监控）")
			} else {
				h.Message("初始化部署完成，所选业务代码与模型已发布，全部模块已就绪")
			}
			return nil
		})
		finish(err, successDetail)
		return err
	}), nil
}

func validateInitialization(c domain.Center, softwareCount int) error {
	if c.InitializedAt != nil || (c.Deployment != nil && c.Deployment.Status == "ready") {
		return fmt.Errorf("中心已经完成初始化；如需重新初始化，请先重置服务器")
	}
	if softwareCount == 0 {
		return fmt.Errorf("初始化前必须选择业务镜像版本；模型可选，不选则停在等待业务发布，待模型发布后续跑网关与监控")
	}
	return nil
}

func (s *Server) detectCenter(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	info, err := s.provisioning.DetectHardware(ctx, c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ok(c, info)
}

func (s *Server) initializeCenter(c *gin.Context) {
	centerID := c.Param("id")
	v, err := s.centers.GetPublic(centerID)
	if err != nil {
		s.storeError(c, err)
		return
	}
	var in struct {
		Items  []deploymentSelection  `json:"items"`
		Models []modelasset.Selection `json:"model_selections"`
	}
	if err = c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	if err = validateInitialization(v, len(in.Items)); err != nil {
		fail(c, 409, err)
		return
	}
	items, err := s.prepareDeploymentItems(c.Request.Context(), in.Items)
	if err != nil {
		fail(c, 400, err)
		return
	}
	models, err := s.models.PrepareSelections(in.Models)
	if err != nil {
		fail(c, 400, err)
		return
	}
	result, err := s.submitCenterInitialization(v, items, models, releaseOperator(c))
	if err != nil {
		fail(c, 409, err)
		return
	}
	ok(c, result)
}

func (s *Server) getCenterCompose(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	document, err := s.provisioning.ReadCompose(ctx, c.Param("id"), c.Query("file"))
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, document)
}

func (s *Server) saveCenterCompose(c *gin.Context) {
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
	document, err := s.provisioning.SaveCompose(ctx, c.Param("id"), in.Content, c.Query("file"), in.Revision)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, document)
}
func (s *Server) deleteCenter(c *gin.Context) {
	e := s.centers.Delete(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	// 被删中心若还在 targets 里会继续失联告警，同步失败必须提示用户。
	if err := s.syncMonitoring("中心已删除"); err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, true)
}
func (s *Server) testCenter(c *gin.Context) {
	fp, e := s.centers.Test(c.Param("id"))
	if e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, map[string]string{"status": "connected", "host_key": fp})
}

func (s *Server) runCenterAction(c *gin.Context) {
	var in struct {
		Action      string `json:"action" binding:"required"`
		ContainerID string `json:"container_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, fmt.Errorf("请选择需要执行的操作"))
		return
	}
	switch in.Action {
	case centeraction.RestartContainer:
		if strings.TrimSpace(in.ContainerID) == "" {
			fail(c, 400, fmt.Errorf("缺少需要重启的容器 ID"))
			return
		}
	case centeraction.RestartCompose, centeraction.RedeployCompose, centeraction.StopCompose, centeraction.RemoveAllContainers:
	default:
		fail(c, 400, fmt.Errorf("不支持的中心操作"))
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	action, containerID := in.Action, in.ContainerID
	params := map[string]string{"center_id": center.ID, "center_name": center.Name, "action": action}
	if containerID != "" {
		params["container_id"] = containerID
	}
	if command := centeraction.CommandPreview(center.ComposeFile, action, containerID); command != "" {
		params["command"] = command
	}
	created := s.tasks.Submit("CENTER_OPERATION", params, func(ctx context.Context, h *task.Handle) error {
		return s.centerActions.Run(ctx, center.ID, action, containerID, h)
	})
	ok(c, created)
}
func (s *Server) centerImages(c *gin.Context) {
	cfg, socket, _, e := s.centers.Connection(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	remote, _, e := sshx.NewDocker(cfg, socket)
	if e != nil {
		fail(c, 400, e)
		return
	}
	defer remote.Close()
	images, e := remote.Client.ImageList(c.Request.Context(), image.ListOptions{})
	if e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, images)
}

type overviewContainer struct {
	ID         string   `json:"id"`
	Names      []string `json:"names"`
	Image      string   `json:"image"`
	State      string   `json:"state"`
	Status     string   `json:"status"`
	SourceID   string   `json:"source_id,omitempty"`
	SourceName string   `json:"source_name,omitempty"`
	Version    string   `json:"version,omitempty"`
	Commit     string   `json:"commit,omitempty"`
}

type overviewVersion struct {
	SourceID       string `json:"source_id,omitempty"`
	SourceName     string `json:"source_name"`
	Version        string `json:"version"`
	Commit         string `json:"commit,omitempty"`
	ContainerCount int    `json:"container_count"`
}

type centerOverview struct {
	Center           domain.Center               `json:"center"`
	Online           bool                        `json:"online"`
	Error            string                      `json:"error,omitempty"`
	Accelerator      string                      `json:"accelerator,omitempty"`
	AcceleratorError string                      `json:"accelerator_error,omitempty"`
	Containers       []overviewContainer         `json:"containers"`
	Versions         []overviewVersion           `json:"versions"`
	Models           []modelasset.CurrentVersion `json:"models"`
	ModelsError      string                      `json:"models_error,omitempty"`
}

func (s *Server) overview(c *gin.Context) {
	centers, err := s.centers.List()
	if err != nil {
		fail(c, 500, err)
		return
	}
	result := make([]centerOverview, len(centers))
	requestContext := c.Request.Context()
	var wg sync.WaitGroup
	for index := range centers {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			center := centers[index]
			item := centerOverview{Center: center, Containers: []overviewContainer{}, Versions: []overviewVersion{}}
			cfg, socket, storedCenter, connectionErr := s.centers.Connection(center.ID)
			if connectionErr != nil {
				item.Error = connectionErr.Error()
				result[index] = item
				return
			}
			// 状态探测用紧超时（拨号+握手+Ping 共 5 秒预算）：黑洞型不通的
			// 中心不再拖满默认 12 秒 SSH 上限，仪表盘整体等待≈最慢一台。
			probeCtx, probeCancel := context.WithTimeout(requestContext, 5*time.Second)
			defer probeCancel()
			remote, fingerprint, connectionErr := sshx.NewDockerContext(probeCtx, cfg, socket)
			if connectionErr != nil {
				item.Error = connectionErr.Error()
				result[index] = item
				return
			}
			defer remote.Close()
			_ = s.centers.RememberHostKey(storedCenter, fingerprint)
			if _, pingErr := remote.Client.Ping(probeCtx); pingErr != nil {
				item.Error = "Docker 连接失败: " + pingErr.Error()
				result[index] = item
				return
			}
			item.Online = true
			item.Accelerator = storedCenter.DetectedAccelerator
			if item.Accelerator == "" {
				item.Accelerator = storedCenter.Accelerator
			}
			result[index] = item
		}()
	}
	wg.Wait()
	ok(c, result)
}

func (s *Server) centerOverview(c *gin.Context) {
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	sources, err := s.sources.List()
	if err != nil {
		fail(c, 500, err)
		return
	}
	item := centerOverview{Center: center, Containers: []overviewContainer{}, Versions: []overviewVersion{}, Models: []modelasset.CurrentVersion{}}
	cfg, socket, storedCenter, err := s.centers.Connection(center.ID)
	if err != nil {
		item.Error = err.Error()
		ok(c, item)
		return
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		item.Error = err.Error()
		ok(c, item)
		return
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(storedCenter, fingerprint)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	item.Accelerator, item.AcceleratorError = func() (string, string) {
		value, detectErr := build.ResolveAccelerator(ctx, remote, storedCenter)
		if detectErr != nil {
			return "unknown", detectErr.Error()
		}
		return value, ""
	}()
	item.Models, err = modelasset.CurrentVersionsOnClient(ctx, remote.SSH, modular.ModelsRoot(storedCenter))
	if err != nil {
		item.Models = []modelasset.CurrentVersion{}
		item.ModelsError = err.Error()
	}
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{})
	if err != nil {
		item.Error = "读取 Docker 容器失败: " + err.Error()
		ok(c, item)
		return
	}
	item.Online = true
	versionIndex := map[string]int{}
	for _, container := range containers {
		entry := overviewContainer{ID: container.ID, Names: cleanContainerNames(container.Names), Image: container.Image, State: container.State, Status: container.Status, SourceID: container.Labels[build.LabelSourceID], SourceName: container.Labels[build.LabelSourceName], Version: container.Labels[build.LabelSourceRef], Commit: container.Labels[build.LabelCommit]}
		if entry.SourceName == "" || entry.Version == "" {
			inferSourceVersion(&entry, sources)
		}
		item.Containers = append(item.Containers, entry)
		if entry.SourceName != "" && entry.Version != "" {
			key := entry.SourceID + "\x00" + entry.SourceName + "\x00" + entry.Version + "\x00" + entry.Commit
			if versionPosition, found := versionIndex[key]; found {
				item.Versions[versionPosition].ContainerCount++
			} else {
				versionIndex[key] = len(item.Versions)
				item.Versions = append(item.Versions, overviewVersion{SourceID: entry.SourceID, SourceName: entry.SourceName, Version: entry.Version, Commit: entry.Commit, ContainerCount: 1})
			}
		}
	}
	ok(c, item)
}

func (s *Server) centerModels(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	versions, err := s.models.CurrentVersions(ctx, c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	ok(c, versions)
}

func (s *Server) setCenterModelPin(c *gin.Context) {
	var in struct {
		Pinned bool `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	center, err := s.centers.GetPublic(c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	icon, action := "🔓", "解绑模型版本组"
	if in.Pinned {
		icon, action = "🔒", "固定模型版本组"
	}
	current, versions, err := s.models.SetModelSetPinned(ctx, center.ID, in.Pinned)
	if err != nil {
		notifyCenterResult(s.notificationSender(center), icon, action, releaseOperator(c), center.Name, "", err)
		fail(c, http.StatusConflict, err)
		return
	}
	successDetail := "解绑后可新增、删除或切换该中心的模型版本。"
	if in.Pinned {
		successDetail = "已固定 " + strconv.Itoa(len(versions)) + " 个模型版本：\n" + pinnedModelSummary(versions) +
			"\n固定后仅允许相同 Manifest 的幂等发布，调整任一模型前需整体解绑。"
	}
	notifyCenterResult(s.notificationSender(center), icon, action, releaseOperator(c), center.Name, successDetail, nil)
	ok(c, current)
}

// pinnedModelSummary 把固定快照压成与初始化完成通知同款的
// "- 模型:版本[平台]@digest前12位" 多行清单。
func pinnedModelSummary(versions []modelasset.CurrentVersion) string {
	entries := make([]string, 0, len(versions))
	for _, current := range versions {
		entries = append(entries, "- "+current.Model+":"+current.Version+"["+modelVariant(current)+"]@"+current.ManifestDigest[:12])
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// modelVariant 拼出模型格式标签：平台，或平台-硬件配置（NVIDIA plan）。
func modelVariant(current modelasset.CurrentVersion) string {
	if current.HardwareProfile != "" {
		return current.Platform + "-" + current.HardwareProfile
	}
	return current.Platform
}

func cleanContainerNames(names []string) []string {
	cleaned := make([]string, len(names))
	for i, name := range names {
		cleaned[i] = strings.TrimPrefix(name, "/")
	}
	return cleaned
}

func inferSourceVersion(container *overviewContainer, sources []domain.Source) {
	imageName, version := splitImageReference(container.Image)
	for _, source := range sources {
		if imageName == build.DefaultImageRepository(source) {
			container.SourceID = source.ID
			container.SourceName = source.Name
			container.Version = version
			return
		}
	}
}

func splitImageReference(value string) (string, string) {
	value = strings.SplitN(value, "@", 2)[0]
	lastSlash := strings.LastIndex(value, "/")
	lastColon := strings.LastIndex(value, ":")
	version := "latest"
	if lastColon > lastSlash {
		version = value[lastColon+1:]
		value = value[:lastColon]
	}
	if slash := strings.LastIndex(value, "/"); slash >= 0 {
		value = value[slash+1:]
	}
	return value, version
}

func (s *Server) listSources(c *gin.Context) {
	v, e := s.sources.List()
	if e != nil {
		fail(c, 500, e)
		return
	}
	ok(c, v)
}
func (s *Server) getGitCredential(c *gin.Context) {
	v, e := s.sources.Credential()
	if e != nil {
		fail(c, 500, e)
		return
	}
	ok(c, v)
}
func (s *Server) saveGitCredential(c *gin.Context) {
	var v domain.GitCredential
	if e := c.ShouldBindJSON(&v); e != nil {
		fail(c, 400, e)
		return
	}
	out, e := s.sources.SaveCredential(v)
	if e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, out)
}

// settingsStatus 返回各凭据的配置状态布尔值，供查看用户渲染页面提示，不暴露凭据内容。
func (s *Server) settingsStatus(c *gin.Context) {
	out := map[string]bool{"git_configured": false, "oss_configured": false, "alert_configured": false, "registry_configured": false}
	if v, err := s.sources.Credential(); err == nil {
		out["git_configured"] = v.Configured
	}
	if v, err := s.baseImages.Credential(); err == nil {
		out["oss_configured"] = v.Configured
	}
	if v, err := s.alerts.Credential(); err == nil {
		out["alert_configured"] = v.Configured
	}
	if v, err := s.registries.Settings(); err == nil {
		out["registry_configured"] = v.Configured
	}
	ok(c, out)
}

func (s *Server) getOSSCredential(c *gin.Context) {
	v, err := s.baseImages.Credential()
	if err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, v)
}

func (s *Server) saveOSSCredential(c *gin.Context) {
	var v domain.OSSCredential
	if err := c.ShouldBindJSON(&v); err != nil {
		fail(c, 400, err)
		return
	}
	out, err := s.baseImages.SaveCredential(v)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, out)
}

func (s *Server) getAlertCredential(c *gin.Context) {
	value, err := s.alerts.Credential()
	if err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, value)
}

func (s *Server) saveAlertCredential(c *gin.Context) {
	var value domain.AlertCredential
	if err := c.ShouldBindJSON(&value); err != nil {
		fail(c, 400, err)
		return
	}
	output, err := s.alerts.SaveCredential(value)
	if err != nil {
		fail(c, 400, err)
		return
	}
	// Webhook 同时是统一监控的通知出口：同步渲染并重载 alertmanager。
	if syncErr := s.monitors.Sync(); syncErr != nil {
		fail(c, 500, fmt.Errorf("凭据已保存，但统一监控配置同步失败: %w", syncErr))
		return
	}
	s.reloadAlertmanager(c)
	ok(c, output)
}

func (s *Server) baseImageCatalog(c *gin.Context) {
	archives, err := s.baseImages.Catalog()
	if err != nil {
		status := 400
		if !errors.Is(err, baseimage.ErrNotConfigured) {
			status = 502
		}
		fail(c, status, err)
		return
	}
	ok(c, archives)
}

func (s *Server) upgradeBaseImages(c *gin.Context) {
	var in struct {
		CenterIDs    []string `json:"center_ids"`
		RestartStack bool     `json:"restart_stack"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个中心服务器"))
		return
	}
	credential, err := s.baseImages.Credential()
	if err != nil || !credential.Configured {
		fail(c, 400, baseimage.ErrNotConfigured)
		return
	}
	created := make([]*domain.Task, 0, len(in.CenterIDs))
	for _, centerID := range in.CenterIDs {
		v, getErr := s.centers.GetPublic(centerID)
		if getErr != nil {
			s.storeError(c, getErr)
			return
		}
		centerID, centerName := centerID, v.Name
		params := map[string]string{"center_id": centerID, "center_name": centerName, "restart_stack": strconv.FormatBool(in.RestartStack)}
		created = append(created, s.tasks.Submit("BASE_IMAGE_UPDATE", params, func(ctx context.Context, h *task.Handle) error {
			return s.baseImages.UpgradeCenter(ctx, centerID, in.RestartStack, h)
		}))
	}
	ok(c, created)
}

func (s *Server) modelCatalog(c *gin.Context) {
	objects, err := s.models.Versions()
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, modelasset.ErrNotConfigured) {
			status = http.StatusBadGateway
		}
		fail(c, status, err)
		return
	}
	ok(c, objects)
}

func (s *Server) emptyModelFolders(c *gin.Context) {
	folders, err := s.models.EmptyModelFolders()
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, modelasset.ErrNotConfigured) {
			status = http.StatusBadGateway
		}
		fail(c, status, err)
		return
	}
	ok(c, folders)
}

func (s *Server) deleteModel(c *gin.Context) {
	if err := s.models.DeleteModel(c.Param("model")); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ok(c, map[string]any{"deleted": true})
}

func (s *Server) createModelUpload(c *gin.Context) {
	var input modelasset.ModelUpload
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	upload, err := s.models.CreateModelUpload(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, modelasset.ErrNotConfigured) {
			status = http.StatusConflict
		}
		fail(c, status, err)
		return
	}
	ok(c, upload)
}

func (s *Server) uploadModelFile(c *gin.Context) {
	size, err := strconv.ParseInt(c.Query("size"), 10, 64)
	if err != nil || c.Request.ContentLength != size {
		fail(c, http.StatusBadRequest, fmt.Errorf("上传文件大小与声明不一致"))
		return
	}
	if err = s.models.UploadModelFile(c.Param("id"), c.Query("format_id"), c.Query("path"), size, c.Request.Body); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ok(c, map[string]any{"uploaded": true})
}

func (s *Server) finalizeModelUpload(c *gin.Context) {
	var input modelasset.ModelUpload
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if input.UploadID != "" && input.UploadID != c.Param("id") {
		fail(c, http.StatusBadRequest, fmt.Errorf("upload_id 不一致"))
		return
	}
	input.UploadID = c.Param("id")
	version, err := s.models.FinalizeModelUpload(input)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ok(c, version)
}

func (s *Server) abortModelUpload(c *gin.Context) {
	if err := s.models.AbortModelUpload(c.Param("id")); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ok(c, map[string]any{"deleted": true})
}

type modelReleaseCenterPlan struct {
	Center domain.Center `json:"center"`
	modelasset.ReleasePlan
}

func (s *Server) modelReleasePlan(c *gin.Context) {
	var in struct {
		CenterIDs  []string               `json:"center_ids"`
		Selections []modelasset.Selection `json:"selections"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("请至少选择一个发布中心"))
		return
	}
	items, err := s.models.PrepareSelections(in.Selections)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	plans := make([]modelReleaseCenterPlan, len(in.CenterIDs))
	var wg sync.WaitGroup
	for index, centerID := range in.CenterIDs {
		index, centerID := index, centerID
		wg.Add(1)
		go func() {
			defer wg.Done()
			center, centerErr := s.store.GetCenter(centerID)
			if centerErr != nil {
				plans[index] = modelReleaseCenterPlan{ReleasePlan: modelasset.ReleasePlan{Error: centerErr.Error(), Action: "blocked"}}
				return
			}
			plans[index] = modelReleaseCenterPlan{Center: center, ReleasePlan: s.models.CheckRelease(c.Request.Context(), centerID, items)}
		}()
	}
	wg.Wait()
	ok(c, plans)
}

func (s *Server) releaseModels(c *gin.Context) {
	var in struct {
		CenterIDs    []string               `json:"center_ids"`
		Selections   []modelasset.Selection `json:"selections"`
		RestartStack bool                   `json:"restart_stack"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("请至少选择一个发布中心"))
		return
	}
	items, err := s.models.PrepareSelections(in.Selections)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	centers := make([]domain.Center, 0, len(in.CenterIDs))
	for _, centerID := range in.CenterIDs {
		center, centerErr := s.store.GetCenter(centerID)
		if centerErr != nil {
			s.storeError(c, centerErr)
			return
		}
		centers = append(centers, center)
	}
	resolvedByCenter := make(map[string][]modelasset.ReleaseItem, len(centers))
	for _, center := range centers {
		plan := s.models.CheckRelease(c.Request.Context(), center.ID, items)
		if !plan.Ready {
			fail(c, http.StatusConflict, fmt.Errorf("中心 %s 不允许本次模型发布: %s", center.Name, plan.Error))
			return
		}
		resolvedByCenter[center.ID] = plan.Targets
	}
	created := make([]*domain.Task, 0, len(centers))
	centerLines := make([]string, 0, len(centers))
	type modelReleaseJob struct {
		centerID     string
		centerName   string
		testMode     bool
		items        []modelasset.ReleaseItem
		encoded      string
		versionsText string
	}
	jobs := make([]modelReleaseJob, 0, len(centers))
	for _, center := range centers {
		centerID := center.ID
		resolvedItems := resolvedByCenter[centerID]
		versions := make([]string, 0, len(resolvedItems))
		for _, item := range resolvedItems {
			variant := item.Platform
			if item.HardwareProfile != "" {
				variant += "-" + item.HardwareProfile
			}
			versions = append(versions, item.Model+":"+item.Version+"["+variant+"]@"+item.ManifestDigest[:12])
		}
		encodedItems, _ := json.Marshal(resolvedItems)
		versionsText := strings.Join(versions, "、")
		jobs = append(jobs, modelReleaseJob{centerID: centerID, centerName: center.Name, testMode: center.TestMode, items: resolvedItems, encoded: string(encodedItems), versionsText: versionsText})
		// 告警控制中心不进入发布通知名单。
		if !center.TestMode {
			centerLines = append(centerLines, center.Name+"："+versionsText)
		}
	}
	// 与统一发布一致：开始/结束向飞书群发送通知（尽力而为，不影响任务）。
	var notifier *releaseNotifier
	if len(centerLines) > 0 {
		notifier = newReleaseNotifier(s.alerts, releaseOperator(c), centerLines)
	}
	var notifyTasks []*domain.Task
	for _, job := range jobs {
		params := map[string]string{
			"center_id": job.centerID, "center_name": job.centerName,
			"model_count": strconv.Itoa(len(job.items)), "versions": job.versionsText,
			"items": job.encoded, "restart_stack": strconv.FormatBool(in.RestartStack),
		}
		job := job
		notify := notifier != nil && !job.testMode
		created = append(created, s.tasks.Submit("MODEL_RELEASE", params, func(ctx context.Context, h *task.Handle) error {
			err := s.runModelRelease(ctx, job.centerID, job.items, in.RestartStack, h)
			if notify {
				notifier.record(job.centerName, err)
			}
			return err
		}))
		if notify {
			notifyTasks = append(notifyTasks, created[len(created)-1])
		}
	}
	if notifier != nil {
		s.watchReleaseNotificationsAsync(notifier, notifyTasks)
	}
	ok(c, created)
}

func (s *Server) runModelRelease(ctx context.Context, centerID string, items []modelasset.ReleaseItem, restartStack bool, h *task.Handle) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error { return s.runModelReleaseLocked(ctx, centerID, items, restartStack, h) })
}
func (s *Server) runModelReleaseLocked(ctx context.Context, centerID string, items []modelasset.ReleaseItem, restartStack bool, h *task.Handle) error {
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		return err
	}
	if err = validateReselection(center, 0, len(items)); err != nil {
		return err
	}

	if !restartStack {
		return s.models.ReleaseCenter(ctx, centerID, items, h)
	}
	h.Message("阶段 1/2：切换模型版本")
	if err := s.models.ReleaseCenter(ctx, centerID, items, phaseReporter{reporter: h, prefix: "阶段 1/2：模型发布", start: 0, end: 75}); err != nil {
		return err
	}
	h.Log("阶段 2/2：仅重启使用模型的服务")
	if err := s.centerActions.RestartModels(ctx, centerID, nil, h); err != nil {
		return err
	}
	return s.finishModuleInitialization(ctx, centerID, nil, h)
}

func (s *Server) saveSource(c *gin.Context) {
	var v domain.Source
	if e := c.ShouldBindJSON(&v); e != nil {
		fail(c, 400, e)
		return
	}
	if id := c.Param("id"); id != "" {
		v.ID = id
	}
	out, e := s.sources.Save(v)
	if e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, out)
}
func (s *Server) deleteSource(c *gin.Context) {
	id := c.Param("id")
	if e := s.sources.Delete(id); e != nil {
		s.storeError(c, e)
		return
	}
	path := filepath.Join(s.sourceRoot, id)
	if rel, e := filepath.Rel(s.sourceRoot, path); e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		_ = os.RemoveAll(path)
	}
	ok(c, true)
}
func (s *Server) versions(c *gin.Context) {
	c.Header("Cache-Control", "no-store, max-age=0")
	src, e := s.sources.Get(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	v, e := s.sources.Versions(ctx, src)
	if e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, v)
}
func (s *Server) checkouts(c *gin.Context) {
	v, e := s.store.ListCheckouts(c.Param("id"))
	if e != nil {
		fail(c, 500, e)
		return
	}
	ok(c, v)
}
func (s *Server) download(c *gin.Context) {
	src, e := s.sources.Get(c.Param("id"))
	if e != nil {
		s.storeError(c, e)
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if e = c.ShouldBindJSON(&in); e != nil || source.ValidateRef(in.Ref) != nil {
		fail(c, 400, fmt.Errorf("版本名称无效"))
		return
	}
	t := s.tasks.Submit("DOWNLOAD_SOURCE", map[string]string{"source_id": src.ID, "ref": in.Ref}, func(ctx context.Context, h *task.Handle) error {
		_, e := s.sources.Download(ctx, src, in.Ref, h)
		return e
	})
	ok(c, t)
}

type deploymentSelection struct {
	SourceID string `json:"source_id"`
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
	Kind     string `json:"kind"`
	// BuiltImage 表示该选择来自中心上已构建的镜像：跳过"远端 ref 仍指向该
	// commit"的实时复核（分支前进后旧镜像仍应可发布），镜像存在性由部署
	// 预检（deploy.Check 按来源标签匹配）把关。
	BuiltImage bool `json:"built_image"`
}

func (s *Server) prepareDeploymentItems(ctx context.Context, selections []deploymentSelection) ([]deploy.Item, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("请至少选择一个需要部署的软件")
	}
	// 软件发布只认镜像仓库：版本必须先在「软件仓库」页构建发布，源码不再
	// 发送到中心构建。
	client, _, err := s.registryReleaseClient()
	if err != nil {
		return nil, err
	}
	items := make([]deploy.Item, 0, len(selections))
	seenSources := map[string]bool{}
	for _, selection := range selections {
		if seenSources[selection.SourceID] {
			return nil, fmt.Errorf("同一个软件不能重复选择")
		}
		seenSources[selection.SourceID] = true
		src, err := s.sources.Get(selection.SourceID)
		if err != nil {
			return nil, err
		}
		if err = source.ValidateRef(selection.Ref); err != nil {
			return nil, fmt.Errorf("%s 的版本无效", src.Name)
		}
		commit := strings.ToLower(strings.TrimSpace(selection.Commit))
		if err = source.ValidateCommit(commit); err != nil {
			return nil, fmt.Errorf("%s 的 commit 无效，请刷新版本", src.Name)
		}
		expected := build.ReleaseImageTag(src, selection.Ref, commit)
		tags, err := client.Tags(ctx, registry.RepoPath(src))
		if err != nil {
			return nil, err
		}
		// TagsHaveVersion 比较的是版本主体（ref-commit12，不带仓库名）。
		if !registry.TagsHaveVersion(tags, build.ReleaseVersionBody(selection.Ref, commit)) {
			return nil, fmt.Errorf("%s 的镜像版本 %s 尚未构建发布到镜像仓库，请先在「软件仓库」页构建发布", src.Name, build.ReleaseVersionBody(selection.Ref, commit))
		}
		items = append(items, deploy.Item{
			SourceID:      src.ID,
			SourceName:    src.Name,
			Ref:           selection.Ref,
			Commit:        commit,
			ExpectedImage: expected,
		})
	}
	return items, nil
}

type releaseCenterPlan struct {
	ServiceMappings map[string][]string    `json:"service_mappings"`
	Center          domain.Center          `json:"center"`
	Ready           bool                   `json:"ready"`
	BuildRequired   bool                   `json:"build_required"`
	Error           string                 `json:"error,omitempty"`
	Images          map[string]string      `json:"images"`
	MissingImages   []string               `json:"missing_images"`
	MissingServices []string               `json:"missing_services"`
	ModelPlan       modelasset.ReleasePlan `json:"model_plan"`
}

func (s *Server) checkUnifiedRelease(ctx context.Context, centerID string, softwareItems []deploy.Item, modelItems []modelasset.ReleaseItem) releaseCenterPlan {
	plan := releaseCenterPlan{Images: map[string]string{}, ModelPlan: modelasset.ReleasePlan{Ready: true, Action: "keep_current", Current: map[string]modelasset.CurrentVersion{}}}
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		plan.Ready = false
		plan.Error = err.Error()
		return plan
	}
	if err = validateReselection(center, len(softwareItems), len(modelItems)); err != nil {
		plan.Ready = false
		plan.Error = err.Error()
		return plan
	}
	errors := make([]string, 0, 2)
	softwareReady := true
	if len(softwareItems) > 0 {
		check := s.deployments.Check(ctx, centerID, softwareItems)
		plan.BuildRequired = len(check.MissingImages) > 0
		plan.Images = check.Images
		plan.MissingImages = check.MissingImages
		plan.MissingServices = check.MissingServices
		plan.ServiceMappings = check.ServiceMappings
		softwareReady = check.Error == "" && len(check.MissingServices) == 0
		if e := validateRebuildSoftware(center, check); e != nil {
			softwareReady = false
			errors = append(errors, e.Error())
		}
		if check.Error != "" {
			errors = append(errors, check.Error)
		}
		if len(check.MissingServices) > 0 {
			errors = append(errors, strings.Join(check.MissingServices, "；"))
		}
		// 版本必须已在镜像仓库发布该中心平台的标签；混合显卡机型在预检期
		// 就暴露缺失，而不是等分发时失败。
		if client, _, clientErr := s.registryReleaseClient(); clientErr != nil {
			softwareReady = false
			errors = append(errors, clientErr.Error())
		} else if platform := centerReleasePlatform(center); platform != "" {
			for _, item := range softwareItems {
				src, srcErr := s.sources.Get(item.SourceID)
				if srcErr != nil {
					softwareReady = false
					errors = append(errors, srcErr.Error())
					continue
				}
				_, digest, digestErr := manifestDigestForPlatform(ctx, client, registry.RepoPath(src), item, platform)
				if digestErr != nil {
					softwareReady = false
					errors = append(errors, digestErr.Error())
				} else if digest == "" {
					softwareReady = false
					errors = append(errors, fmt.Sprintf("%s 的镜像版本 %s 未发布 %s 平台镜像，请先在「软件仓库」页构建发布", item.SourceName, build.ReleaseVersionBody(item.Ref, item.Commit), build.AcceleratorName(platform)))
				}
			}
		}
	}

	if len(modelItems) > 0 {
		plan.ModelPlan = s.models.CheckRelease(ctx, centerID, modelItems)
		if !plan.ModelPlan.Ready {
			errors = append(errors, plan.ModelPlan.Error)
		}
	} else if len(softwareItems) > 0 {
		current, err := s.models.CurrentVersions(ctx, centerID)
		if err != nil {
			plan.ModelPlan.Ready = false
			plan.ModelPlan.Error = "读取中心当前模型失败: " + err.Error()
			errors = append(errors, plan.ModelPlan.Error)
		} else {
			for _, item := range current {
				plan.ModelPlan.Current[item.Model] = item
			}
			if err = currentModelsReady(current); err != nil {
				plan.ModelPlan.Ready = false
				plan.ModelPlan.Error = err.Error()
				errors = append(errors, plan.ModelPlan.Error)
			}
		}
	}
	plan.Ready = softwareReady && plan.ModelPlan.Ready
	plan.Error = strings.Join(nonEmptyStrings(errors), "；")
	return plan
}

func currentModelsReady(current []modelasset.CurrentVersion) error {
	if len(current) == 0 {
		return fmt.Errorf("中心尚未部署模型：请选择模型版本，不能直接发布软件")
	}
	unhealthy := make([]string, 0)
	for _, item := range current {
		if !item.Healthy {
			detail := item.Model
			if item.HealthError != "" {
				detail += "（" + item.HealthError + "）"
			}
			unhealthy = append(unhealthy, detail)
		}
	}
	if len(unhealthy) > 0 {
		return fmt.Errorf("中心当前模型不健康，请选择模型版本重新发布: %s", strings.Join(unhealthy, "、"))
	}
	return nil
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func (s *Server) releasePlan(c *gin.Context) {
	var in struct {
		CenterIDs       []string               `json:"center_ids"`
		Items           []deploymentSelection  `json:"items"`
		ModelSelections []modelasset.Selection `json:"model_selections"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个发布中心"))
		return
	}
	if len(in.Items) == 0 && len(in.ModelSelections) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个软件版本或模型版本"))
		return
	}
	var items []deploy.Item
	var err error
	if len(in.Items) > 0 {
		items, err = s.prepareDeploymentItems(c.Request.Context(), in.Items)
		if err != nil {
			fail(c, 400, err)
			return
		}
	}
	var modelItems []modelasset.ReleaseItem
	if len(in.ModelSelections) > 0 {
		modelItems, err = s.models.PrepareSelections(in.ModelSelections)
		if err != nil {
			fail(c, 400, err)
			return
		}
	}
	plans := make([]releaseCenterPlan, len(in.CenterIDs))
	var wg sync.WaitGroup
	for index, centerID := range in.CenterIDs {
		index, centerID := index, centerID
		wg.Add(1)
		go func() {
			defer wg.Done()
			center, centerErr := s.store.GetCenter(centerID)
			if centerErr != nil {
				plans[index] = releaseCenterPlan{Error: centerErr.Error(), Images: map[string]string{}}
				return
			}
			plans[index] = s.checkUnifiedRelease(c.Request.Context(), centerID, items, modelItems)
			plans[index].Center = center
		}()
	}
	wg.Wait()
	ok(c, plans)
}

func (s *Server) release(c *gin.Context) {
	var in struct {
		CenterIDs       []string               `json:"center_ids"`
		Items           []deploymentSelection  `json:"items"`
		ModelSelections []modelasset.Selection `json:"model_selections"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个发布中心"))
		return
	}
	if len(in.Items) == 0 && len(in.ModelSelections) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个软件版本或模型版本"))
		return
	}
	var items []deploy.Item
	var err error
	if len(in.Items) > 0 {
		items, err = s.prepareDeploymentItems(c.Request.Context(), in.Items)
		if err != nil {
			fail(c, 400, err)
			return
		}
	}
	var modelItems []modelasset.ReleaseItem
	if len(in.ModelSelections) > 0 {
		modelItems, err = s.models.PrepareSelections(in.ModelSelections)
		if err != nil {
			fail(c, 400, err)
			return
		}
	}
	versions := make([]string, 0, len(items))
	for _, item := range items {
		versions = append(versions, softwareVersionText(item.SourceName, item.Ref, item.Commit))
	}
	encodedItems, _ := json.Marshal(items)
	releaseCenters := make([]domain.Center, 0, len(in.CenterIDs))
	for _, centerID := range in.CenterIDs {
		center, centerErr := s.store.GetCenter(centerID)
		if centerErr != nil {
			s.storeError(c, centerErr)
			return
		}
		releaseCenters = append(releaseCenters, center)
	}
	plans := checkReleaseCentersConcurrently(c.Request.Context(), releaseCenters, func(ctx context.Context, centerID string) releaseCenterPlan {
		return s.checkUnifiedRelease(ctx, centerID, items, modelItems)
	})
	releaseCenters, resolvedModels, blocked := readyReleaseCenters(releaseCenters, plans)
	if len(releaseCenters) == 0 {
		fail(c, http.StatusConflict, fmt.Errorf("没有中心通过发布前复检: %s", strings.Join(blocked, "；")))
		return
	}
	type releaseJob struct {
		center       domain.Center
		modelItems   []modelasset.ReleaseItem
		encoded      string
		versionsText string
	}
	jobs := make([]releaseJob, 0, len(releaseCenters))
	centerLines := make([]string, 0, len(releaseCenters))
	for _, center := range releaseCenters {
		centerModelItems := resolvedModels[center.ID]
		allVersions := append([]string{}, versions...)
		for _, item := range centerModelItems {
			variant := item.Platform
			if item.HardwareProfile != "" {
				variant += "-" + item.HardwareProfile
			}
			allVersions = append(allVersions, item.Model+":"+item.Version+"["+variant+"]@"+item.ManifestDigest[:12])
		}
		encodedModelItems, _ := json.Marshal(centerModelItems)
		job := releaseJob{center: center, modelItems: centerModelItems, encoded: string(encodedModelItems), versionsText: strings.Join(allVersions, "、")}
		jobs = append(jobs, job)
		// 告警控制中心不进入发布通知名单。
		if !center.TestMode {
			centerLines = append(centerLines, center.Name+"："+job.versionsText)
		}
	}
	// 发版开始/结束向飞书群发送通知，告知哪些中心正在发版（尽力而为，不影响发布）。
	var notifier *releaseNotifier
	if len(centerLines) > 0 {
		notifier = newReleaseNotifier(s.alerts, releaseOperator(c), centerLines)
	}
	created := make([]*domain.Task, 0, len(jobs))
	var notifyTasks []*domain.Task
	for _, job := range jobs {
		params := map[string]string{
			"center_id":   job.center.ID,
			"center_name": job.center.Name,
			"versions":    job.versionsText,
			"items":       string(encodedItems),
			"model_items": job.encoded,
		}
		centerID, centerName, modelItems := job.center.ID, job.center.Name, job.modelItems
		notify := notifier != nil && !job.center.TestMode
		created = append(created, s.tasks.Submit("RELEASE_VERSION", params, func(ctx context.Context, h *task.Handle) error {
			err := s.runRelease(ctx, centerID, items, modelItems, h)
			if notify {
				notifier.record(centerName, err)
			}
			return err
		}))
		if notify {
			notifyTasks = append(notifyTasks, created[len(created)-1])
		}
	}
	if notifier != nil {
		s.watchReleaseNotificationsAsync(notifier, notifyTasks)
	}
	ok(c, created)
}

// releaseOperator 从会话上下文取当前操作人，用于发布通知。
func releaseOperator(c *gin.Context) string {
	if value, exists := c.Get("auth_username"); exists {
		if name, ok := value.(string); ok {
			return name
		}
	}
	return ""
}

func checkReleaseCentersConcurrently(ctx context.Context, centers []domain.Center, check func(context.Context, string) releaseCenterPlan) []releaseCenterPlan {
	plans := make([]releaseCenterPlan, len(centers))
	var wg sync.WaitGroup
	for index := range centers {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			plans[index] = check(ctx, centers[index].ID)
		}()
	}
	wg.Wait()
	return plans
}

func readyReleaseCenters(centers []domain.Center, plans []releaseCenterPlan) ([]domain.Center, map[string][]modelasset.ReleaseItem, []string) {
	ready := make([]domain.Center, 0, len(centers))
	resolvedModels := make(map[string][]modelasset.ReleaseItem, len(centers))
	blocked := make([]string, 0)
	for index, center := range centers {
		plan := plans[index]
		if !plan.Ready {
			reason := plan.Error
			if reason == "" {
				reason = "未满足发布条件"
			}
			blocked = append(blocked, center.Name+"："+reason)
			continue
		}
		ready = append(ready, center)
		resolvedModels[center.ID] = plan.ModelPlan.Targets
	}
	return ready, resolvedModels, blocked
}

func (s *Server) runRelease(ctx context.Context, centerID string, items []deploy.Item, modelItems []modelasset.ReleaseItem, h *task.Handle) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error { return s.runReleaseLocked(ctx, centerID, items, modelItems, h) })
}
func (s *Server) runReleaseLocked(ctx context.Context, centerID string, items []deploy.Item, modelItems []modelasset.ReleaseItem, h *task.Handle) error {
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		return err
	}
	if err = validateReselection(center, len(items), len(modelItems)); err != nil {
		return err
	}
	if len(items) == 0 && len(modelItems) == 0 {
		return fmt.Errorf("发布任务没有软件或模型版本快照")
	}
	if len(modelItems) == 0 {
		if current, err := s.models.CurrentVersions(ctx, centerID); err != nil {
			return fmt.Errorf("发布前读取中心当前模型失败: %w", err)
		} else if err = currentModelsReady(current); err != nil {
			return err
		}
		return s.runSoftwareRelease(ctx, centerID, items, h)
	}
	if len(items) == 0 {
		return s.runModelRelease(ctx, centerID, modelItems, true, h)
	}

	if err := s.prepareSoftwareRelease(ctx, centerID, items, h); err != nil {
		return err
	}
	before, err := s.centerActions.RuntimeSnapshot(ctx, centerID)
	if err != nil {
		return err
	}
	h.Message("阶段 1/3：发布模型版本")
	if err := s.models.ReleaseCenter(ctx, centerID, modelItems, phaseReporter{reporter: h, prefix: "阶段 1/3：模型发布", start: 0, end: 25}); err != nil {
		return err
	}
	h.Message("阶段 2/3：发布软件版本")
	scope, err := s.deployments.Run(ctx, centerID, items, phaseReporter{reporter: h, prefix: "阶段 2/3：软件发布", start: 25, end: 90})
	if err != nil {
		return err
	}
	if err := s.centerActions.RestartModels(ctx, centerID, before, h); err != nil {
		return err
	}
	if err := s.provisioning.RecordBusinessPublication(ctx, centerID, scope...); err != nil {
		return err
	}
	if center.Deployment != nil && center.Deployment.ReselectionRequired {
		// Fetch current module status after deployment; never overwrite it with
		// the pre-release deployment snapshot.
		current, err := s.centers.GetPublic(centerID)
		if err != nil {
			return err
		}
		current.Deployment.ReselectionRequired = false
		if err = s.centers.UpdateDeployment(centerID, current.Deployment); err != nil {
			return err
		}
	}
	return s.finishModuleInitialization(ctx, centerID, scope, h)
}

func (s *Server) runSoftwareRelease(ctx context.Context, centerID string, items []deploy.Item, r releaseReporter) error {
	if err := s.prepareSoftwareRelease(ctx, centerID, items, r); err != nil {
		return err
	}
	scope, err := s.deployments.Run(ctx, centerID, items, r)
	if err != nil {
		return err
	}
	return s.finishModuleInitialization(ctx, centerID, scope, r)
}
func (s *Server) prepareSoftwareRelease(ctx context.Context, centerID string, items []deploy.Item, r releaseReporter) error {
	r.Message("阶段 1/4：正在检查发布条件")
	r.Progress(3)
	check := s.deployments.Check(ctx, centerID, items)
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		return err
	}
	if err = validateRebuildSoftware(center, check); err != nil {
		return err
	}

	if check.Error != "" || len(check.MissingServices) > 0 {
		return fmt.Errorf("发布预检失败: %s", releaseCheckSummary(check))
	}
	missing := make([]deploy.Item, 0, len(items))
	for _, item := range items {
		if check.Images[item.SourceID] == "" {
			missing = append(missing, item)
		}
	}
	if len(missing) == 0 {
		r.Log("阶段 2/4：目标中心已存在全部精确版本镜像，无需分发")
		r.Log("阶段 3/4：跳过镜像分发")
		r.Progress(65)
	} else {
		return s.prepareRegistrySoftwareRelease(ctx, centerID, missing, r)
	}
	r.Log("阶段 4/4：开始部署已锁定的镜像")
	return nil
}

type releaseReporter interface {
	Log(string)
	Logf(string, ...any)
	Progress(float64)
	Message(string)
}

type phaseReporter struct {
	reporter   releaseReporter
	prefix     string
	start, end float64
}

func (r phaseReporter) Log(message string) { r.reporter.Log(r.prefix + " · " + message) }
func (r phaseReporter) Logf(format string, args ...any) {
	r.Log(fmt.Sprintf(format, args...))
}
func (r phaseReporter) Progress(value float64) {
	r.reporter.Progress(r.start + (r.end-r.start)*value/100)
}
func (r phaseReporter) Message(message string) { r.reporter.Message(r.prefix + " · " + message) }

func releaseCheckSummary(check deploy.CheckResult) string {
	parts := make([]string, 0, 2)
	if check.Error != "" {
		parts = append(parts, check.Error)
	}
	if len(check.MissingServices) > 0 {
		parts = append(parts, strings.Join(check.MissingServices, "；"))
	}
	if len(parts) == 0 {
		return "未知错误"
	}
	return strings.Join(parts, "；")
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// softwareVersionText 以镜像版本口径展示软件版本（名称:ref-commit12），
// 与中心侧显示一致——Git ref/commit 不单独出现在面向中心的界面。
func softwareVersionText(name, ref, commit string) string {
	return name + ":" + build.ReleaseVersionBody(ref, commit)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (s *Server) listTasks(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	ok(c, s.tasks.List(limit))
}
func (s *Server) getTask(c *gin.Context) {
	v, found := s.tasks.Get(c.Param("id"))
	if !found {
		fail(c, 404, store.ErrNotFound)
		return
	}
	ok(c, v)
}
func (s *Server) taskLogs(c *gin.Context) { ok(c, s.tasks.Logs(c.Param("id"), 2000)) }
func (s *Server) cancelTask(c *gin.Context) {
	if e := s.tasks.Cancel(c.Param("id")); e != nil {
		fail(c, 400, e)
		return
	}
	ok(c, true)
}
func (s *Server) taskEvents(c *gin.Context) {
	ch, replay, cancel, e := s.tasks.Subscribe(c.Param("id"))
	if e != nil {
		fail(c, 409, e)
		return
	}
	defer cancel()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	send := func(e task.Event) { c.SSEvent(e.Name, e.Data); c.Writer.Flush() }
	for _, e := range replay {
		send(e)
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-ch:
			send(e)
			if e.Name == "status" {
				if current, found := s.tasks.Get(c.Param("id")); found && current.Status != task.Pending && current.Status != task.Running {
					return
				}
			}
		case <-ticker.C:
			c.SSEvent("ping", map[string]any{"time": time.Now()})
			c.Writer.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}
func (s *Server) storeError(c *gin.Context, e error) {
	if errors.Is(e, store.ErrNotFound) {
		fail(c, 404, e)
	} else {
		fail(c, 500, e)
	}
}

func (s *Server) finishModuleInitialization(ctx context.Context, id string, scope []string, r releaseReporter) error {
	r.Message("正在核验业务 Compose 整体发布状态")
	if err := s.provisioning.RecordBusinessPublication(ctx, id, scope...); err != nil {
		return err
	}
	center, err := s.centers.GetPublic(id)
	if err != nil {
		return err
	}
	if center.Deployment != nil && center.InitializedAt == nil {
		return s.provisioning.ResumeModules(ctx, id, r)
	}
	return nil
}

func validateReselection(c domain.Center, softwareCount, modelCount int) error {
	if c.Deployment == nil || !c.Deployment.ReselectionRequired {
		return nil
	}
	if c.Deployment.Status == "reset" {
		return fmt.Errorf("请先重新初始化中心，再选择软件与模型版本")
	}
	if softwareCount == 0 || modelCount == 0 {
		return fmt.Errorf("中心已重置，请同时重新选择软件和模型版本进行联合发布；原模型文件保留但不会自动沿用")
	}
	return nil
}

func validateRebuildSoftware(c domain.Center, check deploy.CheckResult) error {
	if c.Deployment == nil || !c.Deployment.ReselectionRequired {
		return nil
	}
	business, ok := check.Modules["business"]
	if !ok || len(business.BaseServices) == 0 {
		return fmt.Errorf("重建中心缺少已初始化的业务 Compose")
	}
	selected := map[string]bool{}
	for _, names := range business.ServiceMappings {
		for _, name := range names {
			selected[name] = true
		}
	}
	missing := []string{}
	for _, name := range business.BaseServices {
		if !selected[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("重建时请为全部业务服务选择软件版本，尚未选择：%s", strings.Join(missing, "、"))
	}
	return nil
}
