package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
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
	"center-cv-manager/internal/provision"
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
	auth          *auth.Manager
	centers       *center.Service
	centerActions *centeraction.Service
	sources       *source.Service
	builds        *build.Service
	deployments   *deploy.Service
	provisioning  *provision.Service
	baseImages    *baseimage.Service
	models        *modelasset.Service
	alerts        *alerting.Service
	store         *store.Store
	tasks         *task.Manager
	sourceRoot    string
	static        fs.FS
}

func New(authManager *auth.Manager, centers *center.Service, centerActions *centeraction.Service, sources *source.Service, builds *build.Service, deployments *deploy.Service, provisioning *provision.Service, baseImages *baseimage.Service, models *modelasset.Service, alerts *alerting.Service, st *store.Store, tasks *task.Manager, sourceRoot string, static fs.FS) *Server {
	return &Server{auth: authManager, centers: centers, centerActions: centerActions, sources: sources, builds: builds, deployments: deployments, provisioning: provisioning, baseImages: baseImages, models: models, alerts: alerts, store: st, tasks: tasks, sourceRoot: sourceRoot, static: static}
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
	api := root.Group("")
	api.Use(s.requireAuth())
	api.GET("/centers", s.listCenters)
	api.POST("/centers", s.saveCenter)
	api.PUT("/centers/:id", s.saveCenter)
	api.DELETE("/centers/:id", s.deleteCenter)
	api.POST("/centers/:id/test", s.testCenter)
	api.POST("/centers/:id/detect", s.detectCenter)
	api.POST("/centers/:id/initialize", s.initializeCenter)
	api.POST("/centers/:id/actions", s.runCenterAction)
	api.GET("/centers/:id/modules", s.listModules)
	api.POST("/centers/:id/migration/plan", s.migrationPlan)
	api.POST("/centers/:id/migration", s.migrateCenter)
	api.POST("/centers/:id/modules", s.saveModuleMetadata)
	api.PUT("/centers/:id/modules/:module", s.saveModuleMetadata)
	api.DELETE("/centers/:id/modules/:module", s.deleteModule)
	api.GET("/centers/:id/modules/:module/revisions", s.moduleHistory)
	api.GET("/centers/:id/modules/:module/compose", s.readModule)
	api.PUT("/centers/:id/modules/:module/compose", s.saveModule)
	api.POST("/centers/:id/modules/:module/actions", s.moduleAction)
	api.POST("/centers/:id/modules/:module/plan", s.planModule)
	api.GET("/centers/:id/compose", s.getCenterCompose)
	api.PUT("/centers/:id/compose", s.saveCenterCompose)
	api.GET("/centers/:id/images", s.centerImages)
	api.GET("/centers/:id/overview", s.centerOverview)
	api.GET("/centers/:id/models", s.centerModels)
	api.PUT("/centers/:id/models/pin", s.setCenterModelPin)
	api.GET("/centers/:id/terminal", func(c *gin.Context) {
		centerID := c.Param("id")
		websocket.Handler(func(ws *websocket.Conn) { s.terminalSocket(centerID, ws) }).ServeHTTP(c.Writer, c.Request)
	})
	api.GET("/overview", s.overview)
	api.GET("/sources", s.listSources)
	api.GET("/git-credential", s.getGitCredential)
	api.PUT("/git-credential", s.saveGitCredential)
	api.GET("/oss-credential", s.getOSSCredential)
	api.PUT("/oss-credential", s.saveOSSCredential)
	api.GET("/alert-credential", s.getAlertCredential)
	api.PUT("/alert-credential", s.saveAlertCredential)
	api.GET("/base-images", s.baseImageCatalog)
	api.POST("/base-images/upgrade", s.upgradeBaseImages)
	api.GET("/models", s.modelCatalog)
	api.DELETE("/models/:model", s.deleteModel)
	api.POST("/model-uploads", s.createModelUpload)
	api.PUT("/model-uploads/:id/file", s.uploadModelFile)
	api.POST("/model-uploads/:id/finalize", s.finalizeModelUpload)
	api.DELETE("/model-uploads/:id", s.abortModelUpload)
	api.POST("/model-releases/plan", s.modelReleasePlan)
	api.POST("/model-releases", s.releaseModels)
	api.POST("/sources", s.saveSource)
	api.PUT("/sources/:id", s.saveSource)
	api.DELETE("/sources/:id", s.deleteSource)
	api.GET("/sources/:id/versions", s.versions)
	api.GET("/sources/:id/checkouts", s.checkouts)
	api.POST("/sources/:id/download", s.download)
	api.POST("/releases/plan", s.releasePlan)
	api.POST("/releases", s.release)
	api.GET("/tasks", s.listTasks)
	api.GET("/tasks/:id", s.getTask)
	api.GET("/tasks/:id/logs", s.taskLogs)
	api.GET("/tasks/:id/events", s.taskEvents)
	api.POST("/tasks/:id/cancel", s.cancelTask)
	api.POST("/tasks/cancel-all", s.cancelAllTasks)
	api.POST("/tasks/:id/retry", s.retryTask)
	api.DELETE("/tasks/:id", s.deleteTask)
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
	isNew := c.Param("id") == "" && v.ID == ""
	if id := c.Param("id"); id != "" {
		v.ID = id
	}
	out, e := s.centers.Save(v)
	if e != nil {
		fail(c, 400, e)
		return
	}
	if isNew {
		initialization := s.submitCenterInitialization(out, false, true)
		ok(c, map[string]any{"center": out, "task": initialization})
		return
	}
	ok(c, out)
}

func (s *Server) submitCenterInitialization(v domain.Center, forceTemplate, downloadImages bool) *domain.Task {
	params := map[string]string{"center_id": v.ID, "center_name": v.Name, "force_template": strconv.FormatBool(forceTemplate), "download_images": strconv.FormatBool(downloadImages)}
	return s.tasks.Submit("INITIALIZE_CENTER", params, func(ctx context.Context, h *task.Handle) error {
		return centerlock.Within(ctx, v.ID, func(ctx context.Context) error {
			if err := s.provisioning.Initialize(ctx, v.ID, forceTemplate, h); err != nil {
				return err
			}
			if downloadImages {
				credential, err := s.baseImages.Credential()
				if err != nil {
					return err
				}
				if credential.Configured {
					if err = s.baseImages.UpgradeCenter(ctx, v.ID, false, h); err != nil {
						return err
					}
				} else {
					h.Log("尚未配置 OSS，使用中心已有镜像或 Compose 镜像拉取策略")
				}
			}
			return s.provisioning.ResumeModules(ctx, v.ID, h)
		})
	})
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
		ForceTemplate  bool `json:"force_template"`
		DownloadImages bool `json:"download_images"`
	}
	if err = c.ShouldBindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		fail(c, 400, err)
		return
	}
	ok(c, s.submitCenterInitialization(v, in.ForceTemplate, in.DownloadImages))
}

func (s *Server) getCenterCompose(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	document, err := s.provisioning.ReadCompose(ctx, c.Param("id"))
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, document)
}

func (s *Server) saveCenterCompose(c *gin.Context) {
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	document, err := s.provisioning.SaveCompose(ctx, c.Param("id"), in.Content)
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
		Action        string `json:"action" binding:"required"`
		ContainerID   string `json:"container_id"`
		AdminPassword string `json:"admin_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, fmt.Errorf("请选择操作并输入管理员密码"))
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
	token, _ := c.Cookie(authCookieName)
	if err := s.auth.VerifyPassword(token, in.AdminPassword); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, auth.ErrInvalidSession) {
			status = http.StatusUnauthorized
		}
		fail(c, status, err)
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
			remote, fingerprint, connectionErr := sshx.NewDocker(cfg, socket)
			if connectionErr != nil {
				item.Error = connectionErr.Error()
				result[index] = item
				return
			}
			defer remote.Close()
			_ = s.centers.RememberHostKey(storedCenter, fingerprint)
			ctx, cancel := context.WithTimeout(requestContext, 15*time.Second)
			defer cancel()
			if _, pingErr := remote.Client.Ping(ctx); pingErr != nil {
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	current, err := s.models.SetModelSetPinned(ctx, c.Param("id"), in.Pinned)
	if err != nil {
		fail(c, http.StatusConflict, err)
		return
	}
	ok(c, current)
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
		params := map[string]string{
			"center_id": centerID, "center_name": center.Name,
			"model_count": strconv.Itoa(len(resolvedItems)), "versions": strings.Join(versions, "、"),
			"items": string(encodedItems), "restart_stack": strconv.FormatBool(in.RestartStack),
		}
		created = append(created, s.tasks.Submit("MODEL_RELEASE", params, func(ctx context.Context, h *task.Handle) error {
			return s.runModelRelease(ctx, centerID, resolvedItems, in.RestartStack, h)
		}))
	}
	ok(c, created)
}

func (s *Server) runModelRelease(ctx context.Context, centerID string, items []modelasset.ReleaseItem, restartStack bool, h *task.Handle) error {
	return centerlock.Within(ctx, centerID, func(ctx context.Context) error { return s.runModelReleaseLocked(ctx, centerID, items, restartStack, h) })
}
func (s *Server) runModelReleaseLocked(ctx context.Context, centerID string, items []modelasset.ReleaseItem, restartStack bool, h *task.Handle) error {
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
	return s.finishModuleInitialization(ctx, centerID, h)
}

func (s *Server) syncModels(c *gin.Context) {
	var in struct {
		CenterIDs []string `json:"center_ids"`
		Keys      []string `json:"keys"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	in.Keys = uniqueStrings(in.Keys)
	if len(in.CenterIDs) == 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("请至少选择一个中心服务器"))
		return
	}
	if len(in.Keys) == 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("请至少选择一个模型文件"))
		return
	}
	credential, err := s.baseImages.Credential()
	if err != nil || !credential.Configured {
		fail(c, http.StatusBadRequest, modelasset.ErrNotConfigured)
		return
	}
	created := make([]*domain.Task, 0, len(in.CenterIDs))
	for _, centerID := range in.CenterIDs {
		currentCenter, getErr := s.centers.GetPublic(centerID)
		if getErr != nil {
			s.storeError(c, getErr)
			return
		}
		centerID, centerName := centerID, currentCenter.Name
		keys := append([]string(nil), in.Keys...)
		params := map[string]string{
			"center_id":   centerID,
			"center_name": centerName,
			"model_count": strconv.Itoa(len(keys)),
		}
		encodedKeys, _ := json.Marshal(keys)
		params["keys"] = string(encodedKeys)
		created = append(created, s.tasks.Submit("MODEL_SYNC", params, func(ctx context.Context, h *task.Handle) error {
			return s.models.SyncCenter(ctx, centerID, keys, h)
		}))
	}
	ok(c, created)
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
func (s *Server) build(c *gin.Context) {
	var in struct {
		CenterIDs []string `json:"center_ids"`
		Items     []struct {
			SourceID   string            `json:"source_id"`
			Ref        string            `json:"ref"`
			Dockerfile string            `json:"dockerfile"`
			ImageTag   string            `json:"image_tag"`
			BuildArgs  map[string]string `json:"build_args"`
		} `json:"items"`
		NoCache bool `json:"no_cache"`
	}
	if e := c.ShouldBindJSON(&in); e != nil {
		fail(c, 400, e)
		return
	}
	in.CenterIDs = uniqueStrings(in.CenterIDs)
	if len(in.CenterIDs) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个中心服务器"))
		return
	}
	if len(in.Items) == 0 {
		fail(c, 400, fmt.Errorf("请至少选择一个需要构建的软件"))
		return
	}
	centerNames := map[string]string{}
	for _, centerID := range in.CenterIDs {
		center, e := s.store.GetCenter(centerID)
		if e != nil {
			s.storeError(c, e)
			return
		}
		centerNames[centerID] = center.Name
	}
	type preparedBuild struct {
		source     domain.Source
		checkout   domain.Checkout
		dockerfile string
		imageTag   string
		buildArgs  map[string]string
	}
	prepared := make([]preparedBuild, 0, len(in.Items))
	seenSources := map[string]bool{}
	for _, item := range in.Items {
		if seenSources[item.SourceID] {
			fail(c, 400, fmt.Errorf("同一个软件不能重复选择"))
			return
		}
		seenSources[item.SourceID] = true
		src, e := s.sources.Get(item.SourceID)
		if e != nil {
			s.storeError(c, e)
			return
		}
		if e = source.ValidateRef(item.Ref); e != nil {
			fail(c, 400, fmt.Errorf("%s 的版本无效", src.Name))
			return
		}
		checkout, e := s.store.GetCheckout(item.SourceID, item.Ref)
		if e != nil {
			fail(c, 400, fmt.Errorf("请先下载 %s 的 %s 版本", src.Name, item.Ref))
			return
		}
		if _, e = os.Stat(checkout.Path); e != nil {
			fail(c, 400, fmt.Errorf("%s 的本地源码缓存不存在，请重新下载", src.Name))
			return
		}
		if item.ImageTag == "" {
			item.ImageTag = build.DefaultImageTag(src, item.Ref)
		}
		if e = build.ValidateImageTag(item.ImageTag); e != nil {
			fail(c, 400, fmt.Errorf("%s: %w", src.Name, e))
			return
		}
		prepared = append(prepared, preparedBuild{source: src, checkout: checkout, dockerfile: item.Dockerfile, imageTag: item.ImageTag, buildArgs: item.BuildArgs})
	}
	tasks := make([]*domain.Task, 0, len(in.CenterIDs)*len(prepared))
	for _, centerID := range in.CenterIDs {
		for _, item := range prepared {
			centerID, item := centerID, item
			params := map[string]string{"center_id": centerID, "center_name": centerNames[centerID], "source_id": item.source.ID, "source_name": item.source.Name, "ref": item.checkout.Ref, "image_tag": item.imageTag}
			params["dockerfile"] = item.dockerfile
			params["no_cache"] = strconv.FormatBool(in.NoCache)
			encodedArgs, _ := json.Marshal(item.buildArgs)
			params["build_args"] = string(encodedArgs)
			t := s.tasks.Submit("BUILD_IMAGE", params, func(ctx context.Context, h *task.Handle) error {
				return s.builds.Run(ctx, build.Options{CenterID: centerID, ContextDir: item.checkout.Path, Dockerfile: item.dockerfile, ImageTag: item.imageTag, NoCache: in.NoCache, BuildArgs: item.buildArgs, SourceID: item.source.ID, SourceName: item.source.Name, SourceRef: item.checkout.Ref, Commit: item.checkout.Commit}, h)
			})
			tasks = append(tasks, t)
		}
	}
	ok(c, tasks)
}

type deploymentSelection struct {
	SourceID string `json:"source_id"`
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
}

type deploymentCenterPlan struct {
	Center          domain.Center     `json:"center"`
	Ready           bool              `json:"ready"`
	Error           string            `json:"error,omitempty"`
	Images          map[string]string `json:"images"`
	MissingImages   []string          `json:"missing_images"`
	MissingServices []string          `json:"missing_services"`
}

func (s *Server) prepareDeploymentItems(selections []deploymentSelection) ([]deploy.Item, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("请至少选择一个需要部署的软件")
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
		if commit == "" {
			// Keep historical build/deploy API callers working while new release
			// clients pin the commit returned by Git explicitly.
			checkout, checkoutErr := s.store.GetCheckout(src.ID, selection.Ref)
			if checkoutErr != nil {
				return nil, fmt.Errorf("%s 的 %s 未锁定 commit，请刷新远端版本后重试", src.Name, selection.Ref)
			}
			if _, checkoutErr = os.Stat(checkout.Path); checkoutErr != nil {
				return nil, fmt.Errorf("%s 的本地源码缓存不存在，请刷新远端版本后重试", src.Name)
			}
			commit = checkout.Commit
		}
		if err = source.ValidateCommit(commit); err != nil {
			return nil, fmt.Errorf("%s 的 commit 无效", src.Name)
		}
		items = append(items, deploy.Item{
			SourceID:      src.ID,
			SourceName:    src.Name,
			Ref:           selection.Ref,
			Commit:        commit,
			ExpectedImage: build.ReleaseImageTag(src, selection.Ref, commit),
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
		if check.Error != "" {
			errors = append(errors, check.Error)
		}
		if len(check.MissingServices) > 0 {
			errors = append(errors, strings.Join(check.MissingServices, "；"))
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
		items, err = s.prepareDeploymentItems(in.Items)
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
		items, err = s.prepareDeploymentItems(in.Items)
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
		versions = append(versions, fmt.Sprintf("%s:%s@%s", item.SourceName, item.Ref, shortCommit(item.Commit)))
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
	created := make([]*domain.Task, 0, len(releaseCenters))
	for _, center := range releaseCenters {
		centerID := center.ID
		centerModelItems := resolvedModels[centerID]
		allVersions := append([]string{}, versions...)
		for _, item := range centerModelItems {
			variant := item.Platform
			if item.HardwareProfile != "" {
				variant += "-" + item.HardwareProfile
			}
			allVersions = append(allVersions, item.Model+":"+item.Version+"["+variant+"]@"+item.ManifestDigest[:12])
		}
		encodedModelItems, _ := json.Marshal(centerModelItems)
		params := map[string]string{
			"center_id":   centerID,
			"center_name": center.Name,
			"versions":    strings.Join(allVersions, "、"),
			"items":       string(encodedItems),
			"model_items": string(encodedModelItems),
		}
		created = append(created, s.tasks.Submit("RELEASE_VERSION", params, func(ctx context.Context, h *task.Handle) error {
			return s.runRelease(ctx, centerID, items, centerModelItems, h)
		}))
	}
	ok(c, created)
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
	if err := s.deployments.Run(ctx, centerID, items, phaseReporter{reporter: h, prefix: "阶段 2/3：软件发布", start: 25, end: 90}); err != nil {
		return err
	}
	if err := s.centerActions.RestartModels(ctx, centerID, before, h); err != nil {
		return err
	}
	return s.finishModuleInitialization(ctx, centerID, h)
}

func (s *Server) runSoftwareRelease(ctx context.Context, centerID string, items []deploy.Item, r releaseReporter) error {
	if err := s.prepareSoftwareRelease(ctx, centerID, items, r); err != nil {
		return err
	}
	if err := s.deployments.Run(ctx, centerID, items, r); err != nil {
		return err
	}
	return s.finishModuleInitialization(ctx, centerID, r)
}
func (s *Server) prepareSoftwareRelease(ctx context.Context, centerID string, items []deploy.Item, r releaseReporter) error {
	r.Message("阶段 1/4：正在检查发布条件")
	r.Progress(3)
	check := s.deployments.Check(ctx, centerID, items)
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
		r.Log("阶段 2/4：目标中心已存在全部精确版本镜像，无需准备源码")
		r.Log("阶段 3/4：跳过镜像构建")
		r.Progress(65)
	} else {
		for index, item := range missing {
			src, err := s.sources.Get(item.SourceID)
			if err != nil {
				return err
			}
			start := 5 + float64(index)*60/float64(len(missing))
			end := 5 + float64(index+1)*60/float64(len(missing))
			prepareEnd := start + (end-start)*0.25
			checkout, err := s.sources.EnsureCheckout(ctx, src, item.Ref, item.Commit, phaseReporter{reporter: r, prefix: fmt.Sprintf("阶段 2/4：准备 %s 源码", item.SourceName), start: start, end: prepareEnd})
			if err != nil {
				return fmt.Errorf("准备 %s:%s@%s 源码失败: %w", item.SourceName, item.Ref, shortCommit(item.Commit), err)
			}
			reporter := phaseReporter{reporter: r, prefix: fmt.Sprintf("阶段 3/4：构建 %s", item.SourceName), start: prepareEnd, end: end}
			err = s.builds.Run(ctx, build.Options{
				CenterID: centerID, ContextDir: checkout.Path, Dockerfile: "auto", ImageTag: item.ExpectedImage,
				BuildArgs: map[string]string{}, SourceID: src.ID, SourceName: src.Name, SourceRef: item.Ref, Commit: item.Commit,
			}, reporter)
			if err != nil {
				return fmt.Errorf("构建 %s 失败: %w", item.SourceName, err)
			}
		}
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

func (s *Server) deploymentPlan(c *gin.Context) {
	var in struct {
		Items []deploymentSelection `json:"items"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	items, err := s.prepareDeploymentItems(in.Items)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.storeError(c, err)
		} else {
			fail(c, 400, err)
		}
		return
	}
	centers, err := s.centers.List()
	if err != nil {
		fail(c, 500, err)
		return
	}
	plans := make([]deploymentCenterPlan, len(centers))
	var wg sync.WaitGroup
	for index, currentCenter := range centers {
		index, currentCenter := index, currentCenter
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			check := s.deployments.Check(ctx, currentCenter.ID, items)
			plans[index] = deploymentCenterPlan{
				Center:          currentCenter,
				Ready:           check.Ready,
				Error:           check.Error,
				Images:          check.Images,
				MissingImages:   check.MissingImages,
				MissingServices: check.MissingServices,
			}
		}()
	}
	wg.Wait()
	ok(c, plans)
}

func (s *Server) deploy(c *gin.Context) {
	var in struct {
		CenterIDs []string              `json:"center_ids"`
		Items     []deploymentSelection `json:"items"`
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
	items, err := s.prepareDeploymentItems(in.Items)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.storeError(c, err)
		} else {
			fail(c, 400, err)
		}
		return
	}
	centerNames := map[string]string{}
	for _, centerID := range in.CenterIDs {
		currentCenter, err := s.store.GetCenter(centerID)
		if err != nil {
			s.storeError(c, err)
			return
		}
		centerNames[centerID] = currentCenter.Name
	}
	versions := make([]string, 0, len(items))
	for _, item := range items {
		versions = append(versions, item.SourceName+":"+item.Ref)
	}
	created := make([]*domain.Task, 0, len(in.CenterIDs))
	for _, centerID := range in.CenterIDs {
		centerID := centerID
		params := map[string]string{
			"center_id":   centerID,
			"center_name": centerNames[centerID],
			"versions":    strings.Join(versions, "、"),
		}
		encodedItems, _ := json.Marshal(items)
		params["items"] = string(encodedItems)
		created = append(created, s.tasks.Submit("DEPLOY_STACK", params, func(ctx context.Context, h *task.Handle) error {
			return s.deployments.Run(ctx, centerID, items, h)
		}))
	}
	ok(c, created)
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

func (s *Server) finishModuleInitialization(ctx context.Context, id string, r releaseReporter) error {
	c, err := s.centers.GetPublic(id)
	if err != nil {
		return err
	}
	if c.Deployment != nil && c.Deployment.Status != "ready" {
		return s.provisioning.ResumeModules(ctx, id, r)
	}
	return nil
}
