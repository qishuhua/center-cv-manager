package main

import (
	"embed"
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
	"center-cv-manager/internal/provision"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/source"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
)

//go:embed web_dist
var webFS embed.FS

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "监听地址")
	dataDir := flag.String("data", "./data", "数据目录")
	flag.Parse()
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
	var static fs.FS = webFS
	server := api.New(authManager, centers, centerActions, sources, builds, deployments, provisioning, baseImages, models, alerts, st, tasks, filepath.Join(abs, "sources"), static)
	fmt.Printf("中心数字化管理平台 listening on http://%s\n", *listen)
	if e = server.Router().Run(*listen); e != nil {
		log.Fatal(e)
	}
}
