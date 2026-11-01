package main

import (
	"bytes"
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/deploy"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/provision"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	oss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"path"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "recover" {
		return recoverCenter()
	}
	db, e := sql.Open("sqlite", "file:/app/data/manager.db?mode=ro")
	if e != nil {
		return e
	}
	defer db.Close()
	if len(os.Args) > 1 && os.Args[1] == "status" {
		rows, err := db.Query("SELECT id,type,status FROM tasks WHERE status IN ('PENDING','RUNNING')")
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var id, kind, status string
			if err = rows.Scan(&id, &kind, &status); err != nil {
				return err
			}
			fmt.Println(id, kind, status)
			count++
		}
		fmt.Println("ACTIVE_TASKS", count)
		var state string
		if err = db.QueryRow("SELECT deployment_json FROM centers WHERE id=?", centerID).Scan(&state); err != nil {
			return err
		}
		fmt.Println(state)
		return rows.Err()
	}
	var cfg sshx.Config
	e = db.QueryRow("SELECT host,port,username,password,private_key,passphrase,host_key FROM centers WHERE id=?", "c929fe88-a5b7-4e7e-9ec7-115c2536bc2e").Scan(&cfg.Host, &cfg.Port, &cfg.Username, &cfg.Password, &cfg.PrivateKey, &cfg.Passphrase, &cfg.HostKey)
	if e != nil {
		return e
	}
	cipher, e := secret.LoadOrCreate("/app/data")
	if e != nil {
		return e
	}
	for _, p := range []*string{&cfg.Password, &cfg.PrivateKey, &cfg.Passphrase} {
		*p, e = cipher.Decrypt(*p)
		if e != nil {
			return e
		}
	}
	c, _, e := sshx.Dial(cfg)
	if e != nil {
		return e
	}
	defer c.Close()
	if len(os.Args) > 1 && (os.Args[1] == "download-onnx" || os.Args[1] == "publish-models") {
		var conf domain.OSSCredential
		e = db.QueryRow("SELECT endpoint,bucket,access_key_id,access_key_secret FROM oss_credentials WHERE id=1").Scan(&conf.Endpoint, &conf.Bucket, &conf.AccessKeyID, &conf.AccessKeySecret)
		if e != nil {
			return e
		}
		sec, e := cipher.Decrypt(conf.AccessKeySecret)
		if e != nil {
			return e
		}
		oc, e := oss.New(conf.Endpoint, conf.AccessKeyID, sec)
		if e != nil {
			return e
		}
		bucket, e := oc.Bucket(conf.Bucket)
		if e != nil {
			return e
		}
		var paramsText string
		e = db.QueryRow("SELECT params FROM tasks WHERE id=?", "task-d5218b58-2a81-48ef-9cf0-69a986b04b15").Scan(&paramsText)
		if e != nil {
			return e
		}
		var params map[string]string
		if e = json.Unmarshal([]byte(paramsText), &params); e != nil {
			return e
		}
		var items []modelasset.ReleaseItem
		if e = json.Unmarshal([]byte(params["models"]), &items); e != nil {
			return e
		}
		if os.Args[1] == "publish-models" {
			svc := modelasset.New(readRepo{db}, cipher, nil)
			for _, item := range items {
				if item.Model != "center_person_car" && item.Model != "long_hair" {
					continue
				}
				input := modelasset.ModelUpload{Model: item.Model, Version: item.Version + "-v100-fix-20261014"}
				contents := map[string][]byte{}
				for _, format := range item.Formats {
					uf := modelasset.ModelUploadFormat{ID: format.ID, Platform: format.Platform, HardwareProfile: format.HardwareProfile}
					for _, a := range format.Artifacts {
						var data []byte
						if format.Platform == "nvidia" {
							sess, err := c.NewSession()
							if err != nil {
								return err
							}
							data, err = sess.Output("cat /home/jt/.local/share/center-manager/data/model-repair-20261014/" + item.Model + ".plan")
							sess.Close()
							if err != nil {
								return err
							}
							if len(data) < 1024 {
								return fmt.Errorf("candidate engine empty")
							}
						} else {
							body, err := bucket.GetObject(path.Join(path.Dir(item.ManifestKey), a.Path))
							if err != nil {
								return err
							}
							data, err = io.ReadAll(io.LimitReader(body, a.Size+1))
							body.Close()
							if err != nil {
								return err
							}
							if int64(len(data)) != a.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != a.SHA256 {
								return fmt.Errorf("source artifact hash mismatch")
							}
						}
						name := strings.TrimPrefix(a.Path, "artifact/"+format.ID+"/")
						contents[format.ID+"/"+name] = data
						uf.Files = append(uf.Files, modelasset.ModelUploadFile{Path: name, Size: int64(len(data))})
					}
					input.Formats = append(input.Formats, uf)
				}
				upload, err := svc.CreateModelUpload(input)
				if err != nil {
					return err
				}
				for _, f := range upload.Formats {
					for _, a := range f.Files {
						if err = svc.UploadModelFile(upload.UploadID, f.ID, a.Path, a.Size, bytes.NewReader(contents[f.ID+"/"+a.Path])); err != nil {
							return err
						}
					}
				}
				version, err := svc.FinalizeModelUpload(upload)
				if err != nil {
					return err
				}
				fmt.Println("Published repaired model:", version.Model, version.Version, version.ManifestDigest)
			}
			return nil
		}
		for _, item := range items {
			if item.Model != "center_person_car" && item.Model != "long_hair" {
				continue
			}
			for _, f := range item.Formats {
				if f.Platform != "onnx" {
					continue
				}
				for _, a := range f.Artifacts {
					body, e := bucket.GetObject(path.Join(path.Dir(item.ManifestKey), a.Path))
					if e != nil {
						return e
					}
					data, e := io.ReadAll(io.LimitReader(body, a.Size+1))
					body.Close()
					if e != nil {
						return e
					}
					if int64(len(data)) != a.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != a.SHA256 {
						return fmt.Errorf("ONNX hash mismatch: %s", item.Model)
					}
					s, e := c.NewSession()
					if e != nil {
						return e
					}
					s.Stdin = bytes.NewReader(data)
					dest := "/home/jt/.local/share/center-manager/data/model-repair-20261014/" + item.Model + ".onnx"
					e = s.Run("umask 022; mkdir -p /home/jt/.local/share/center-manager/data/model-repair-20261014 && test ! -e " + dest + " && cat > " + dest)
					s.Close()
					if e != nil {
						return e
					}
					fmt.Println("Verified ONNX:", item.Model, item.Version, a.SHA256)
				}
			}
		}
		return nil
	}
	command, e := io.ReadAll(os.Stdin)
	if e != nil {
		return e
	}
	s, e := c.NewSession()
	if e != nil {
		return e
	}
	defer s.Close()
	s.Stdout = os.Stdout
	s.Stderr = os.Stderr
	timer := time.AfterFunc(55*time.Second, func() { c.Close() })
	defer timer.Stop()
	return s.Run(strings.ReplaceAll(string(command), "\r", ""))
}

const centerID = "c929fe88-a5b7-4e7e-9ec7-115c2536bc2e"

type reporter struct{ h *task.Handle }

func (r reporter) Log(s string)            { fmt.Println(s); r.h.Log(s) }
func (r reporter) Logf(s string, a ...any) { r.Log(fmt.Sprintf(s, a...)) }
func (r reporter) Message(s string)        { fmt.Println(s); r.h.Message(s) }
func (r reporter) Progress(v float64)      { r.h.Progress(v) }
func recoverCenter() error {
	st, e := store.Open("/app/data/manager.db")
	if e != nil {
		return e
	}
	defer st.Close()
	tasks, e := st.ListTasks(100)
	if e != nil {
		return e
	}
	for _, t := range tasks {
		if t.Status == task.Running || t.Status == task.Pending {
			return fmt.Errorf("active task exists: %s", t.ID)
		}
	}
	cipher, e := secret.LoadOrCreate("/app/data")
	if e != nil {
		return e
	}
	centers := center.New(st, cipher)
	models := modelasset.New(st, cipher, centers)
	prov := provision.New(centers, nil)
	deployments := deploy.New(centers)
	original, e := st.GetTask("task-d5218b58-2a81-48ef-9cf0-69a986b04b15")
	if e != nil {
		return e
	}
	var software []deploy.Item
	if e = json.Unmarshal([]byte(original.Params["items"]), &software); e != nil {
		return e
	}
	var items []modelasset.ReleaseItem
	if e = json.Unmarshal([]byte(original.Params["models"]), &items); e != nil {
		return e
	}
	versions, e := models.Versions()
	if e != nil {
		return e
	}
	for i := range items {
		if items[i].Model != "center_person_car" && items[i].Model != "long_hair" {
			continue
		}
		found := false
		for _, v := range versions {
			if v.Model == items[i].Model && v.Version == items[i].Version+"-v100-fix-20261014" {
				items[i] = modelasset.ReleaseItem{SchemaVersion: v.SchemaVersion, Model: v.Model, Version: v.Version, ManifestKey: v.ManifestKey, ManifestDigest: v.ManifestDigest, Formats: v.Formats}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("repaired version missing: %s", items[i].Model)
		}
	}
	plan := models.CheckRelease(context.Background(), centerID, items)
	if !plan.Ready {
		return fmt.Errorf("model plan: %s", plan.Error)
	}
	params := map[string]string{}
	for k, v := range original.Params {
		params[k] = v
	}
	data, _ := json.Marshal(items)
	params["models"] = string(data)
	params["repair_of"] = original.ID
	manager, e := task.New(st)
	if e != nil {
		return e
	}
	t := manager.Submit("INITIALIZE_CENTER", params, func(ctx context.Context, h *task.Handle) error {
		return centerlock.Within(ctx, centerID, func(ctx context.Context) error {
			r := reporter{h}
			r.Message("修复 NVIDIA 模型路径并恢复郑州初始化")
			if e := models.ReleaseCenter(ctx, centerID, plan.Targets, r); e != nil {
				return e
			}
			cfg, _, c, e := centers.Connection(centerID)
			if e != nil {
				return e
			}
			remote, _, e := sshx.Dial(cfg)
			if e != nil {
				return e
			}
			e = modelasset.RepairRuntimePathsOnClient(ctx, remote, cfg.Password, c.Deployment.ModelsRoot, r)
			remote.Close()
			if e != nil {
				return e
			}
			scope, e := deployments.Run(ctx, centerID, software, r)
			if e != nil {
				return e
			}
			if e = prov.RecordBusinessPublication(ctx, centerID, scope...); e != nil {
				return e
			}
			current, e := centers.GetPublic(centerID)
			if e != nil {
				return e
			}
			current.Deployment.ReselectionRequired = false
			if e = centers.UpdateDeployment(centerID, current.Deployment); e != nil {
				return e
			}
			if e = prov.ResumeModules(ctx, centerID, r); e != nil {
				return e
			}
			if e = prov.RecordBusinessPublication(ctx, centerID); e != nil {
				return e
			}
			done, e := centers.GetPublic(centerID)
			if e != nil {
				return e
			}
			if done.Deployment.Status != "ready" {
				return fmt.Errorf("not ready: %s", done.Deployment.Status)
			}
			r.Message("郑州初始化修复完成：模型、业务、网关和监控已就绪")
			return nil
		})
	})
	fmt.Println("RECOVERY_TASK", t.ID)
	for {
		time.Sleep(time.Second)
		current, _ := manager.Get(t.ID)
		if current.Status == task.Success {
			return nil
		}
		if current.Status == task.Failed || current.Status == task.Cancelled {
			return fmt.Errorf("recovery failed: %s", current.Error)
		}
	}
}

type readRepo struct{ db *sql.DB }

func (r readRepo) GetOSSCredential() (domain.OSSCredential, error) {
	var v domain.OSSCredential
	e := r.db.QueryRow("SELECT endpoint,bucket,access_key_id,access_key_secret,images_prefix,models_prefix FROM oss_credentials WHERE id=1").Scan(&v.Endpoint, &v.Bucket, &v.AccessKeyID, &v.AccessKeySecret, &v.ImagesPrefix, &v.ModelsPrefix)
	return v, e
}

func (readRepo) GetModelConvertSettings() (domain.ModelConvertSettings, error) {
	return domain.ModelConvertSettings{}, nil
}

func (readRepo) SaveModelConvertSettings(domain.ModelConvertSettings) error {
	return nil
}
