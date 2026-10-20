package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"center-cv-manager/internal/alerting"
	"center-cv-manager/internal/api"
	"center-cv-manager/internal/auth"
	"center-cv-manager/internal/baseimage"
	"center-cv-manager/internal/build"
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centeraction"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/monitor"
	"center-cv-manager/internal/provision"
	"center-cv-manager/internal/registry"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/source"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
	"center-cv-manager/internal/templates"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "监听地址")
	dataDir := flag.String("data", "./data", "数据目录")
	webDir := flag.String("web", "./web/dist", "前端静态文件目录")
	templatesDir := flag.String("templates", "./internal/templates", "部署模板目录")
	monitorDir := flag.String("monitor", "", "统一监控动态配置渲染目录（默认 <data>/monitor；compose 形态挂载专用卷）")
	flag.Parse()
	if err := templates.SetDirectory(*templatesDir); err != nil {
		log.Fatal(err)
	}
	abs, e := filepath.Abs(*dataDir)
	if e != nil {
		log.Fatal(e)
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		log.Fatal(e)
	}
	st, e := store.Open(filepath.Join(abs, "manager.db"))
	if e != nil {
		log.Fatal(e)
	}
	defer st.Close()
	cipher, e := secret.LoadOrCreate(abs)
	if e != nil {
		log.Fatal(e)
	}
	authManager, e := auth.NewManager(abs)
	if e != nil {
		log.Fatal(e)
	}
	centers := center.New(st, cipher)
	centerActions := centeraction.New(centers)
	sources := source.New(filepath.Join(abs, "sources"), st, cipher)
	tasks, e := task.New(st)
	if e != nil {
		log.Fatal(e)
	}
	builds := build.New(centers)
	deployments := deploy.New(centers)
	alerts := alerting.New(st, cipher)
	provisioning := provision.New(centers, alerts)
	baseImages := baseimage.New(st, cipher, centers)
	models := modelasset.New(st, cipher, centers)
	registries := registry.New(st, cipher, filepath.Join(abs, "registry", "config.yml"))
	// 统一监控：启动时渲染一次补齐监控卷，此后由 center.Service 的变更钩子
	// 在中心记录任何写路径后自动重渲染（告警凭据保存在 API 层另行触发）。
	if *monitorDir == "" {
		*monitorDir = filepath.Join(abs, "monitor")
	}
	prometheusURL := os.Getenv("MONITOR_PROMETHEUS_URL")
	if prometheusURL == "" {
		prometheusURL = "http://127.0.0.1:9090"
	}
	monitors := monitor.New(st, cipher, *monitorDir, prometheusURL)
	centers.SetMonitorSync(monitors.Sync)
	if err := monitors.Sync(); err != nil {
		log.Printf("统一监控配置同步失败: %v", err)
	}
	// 前端由 web 容器（nginx）提供时 API 不携带静态文件；目录缺失仅提供 API，
	// make dev 直跑时仍可直接服务 web/dist。
	static := fs.FS(nil)
	if info, statErr := os.Stat(*webDir); statErr == nil && info.IsDir() {
		static = os.DirFS(*webDir)
	} else {
		log.Printf("前端静态目录 %s 不存在，仅提供 API（compose 形态前端由 web 容器提供）", *webDir)
	}
	server := api.New(authManager, centers, centerActions, sources, builds, deployments, provisioning, baseImages, models, alerts, registries, monitors, st, tasks, filepath.Join(abs, "sources"), static)
	fmt.Printf("中心数字化管理平台 listening on http://%s\n", *listen)
	if e = server.Router().Run(*listen); e != nil {
		log.Fatal(e)
	}
}
