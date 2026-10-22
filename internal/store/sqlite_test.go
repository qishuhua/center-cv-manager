package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
)

func TestEmptyListsAreNotNil(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	centers, err := st.ListCenters()
	if err != nil || centers == nil {
		t.Fatalf("centers must be an empty slice: %#v %v", centers, err)
	}
	sources, err := st.ListSources()
	if err != nil || sources == nil {
		t.Fatalf("sources must be an empty slice: %#v %v", sources, err)
	}
	tasks, err := st.ListTasks(10)
	if err != nil || tasks == nil {
		t.Fatalf("tasks must be an empty slice: %#v %v", tasks, err)
	}
}

func TestGitCredentialSchemaUpgradeAndTimeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sources (id TEXT PRIMARY KEY,name TEXT NOT NULL,repository TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sources(id,name,repository,description,created_at,updated_at) VALUES('legacy-source','Legacy','https://git.example.test/legacy.git','',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	credential := domain.GitCredential{BaseURL: "https://git.example.test", Username: "oauth2", Token: "encrypted", Configured: true, UpdatedAt: now}
	if err = st.SaveGitCredential(credential); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetGitCredential()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BaseURL != credential.BaseURL || loaded.Username != "oauth2" || loaded.Token != "encrypted" || !loaded.Configured {
		t.Fatalf("git credential mismatch: %#v", loaded)
	}
	if !loaded.UpdatedAt.Equal(now) {
		t.Fatalf("time mismatch: %v", loaded.UpdatedAt)
	}
	legacySource, err := st.GetSource("legacy-source")
	if err != nil || legacySource.Name != "Legacy" || legacySource.Repository != "https://git.example.test/legacy.git" {
		t.Fatalf("legacy source migration failed: %#v %v", legacySource, err)
	}
}

func TestGatewayIndependentMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	center := domain.Center{ID: "center-1", Name: "测试中心", Host: "192.0.2.1", Port: 22, Username: "root", AuthType: "password", Password: "encrypted", CreatedAt: now, UpdatedAt: now,
		Deployment: &domain.Deployment{Root: "/home/root/deploy", Status: "ready", Modules: []domain.ComposeModule{
			{ID: "infra", Name: "中间件", Project: "center-cv-infra", Enabled: true},
			{ID: "business", Name: "业务", Project: "center-cv-business", Enabled: true, DependsOn: []string{"infra"}},
			{ID: "gateway", Name: "网关", Project: "center-cv-gateway", Enabled: true, DependsOn: []string{"business"}},
		}}}
	if err = st.SaveCenter(center); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// 模拟升级前的库：摘掉迁移标记后重开，迁移应重写存量 deployment_json。
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM schema_migrations WHERE name='gateway_independent_v1'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	loaded, err := st2.GetCenter(center.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range loaded.Deployment.Modules {
		if m.ID == "gateway" && len(m.DependsOn) != 0 {
			t.Fatalf("网关应独立于业务，depends_on 应清空: %+v", m)
		}
		if m.ID == "business" && (len(m.DependsOn) != 1 || m.DependsOn[0] != "infra") {
			t.Fatalf("其它模块依赖不应被改动: %+v", m)
		}
	}
	// 幂等：再次重开不报错、数据不变。
	st2.Close()
	st3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	if again, err := st3.GetCenter(center.ID); err != nil {
		t.Fatal(err)
	} else if g, _ := modularFind(again, "gateway"); len(g.DependsOn) != 0 {
		t.Fatalf("重复迁移不应改动数据: %+v", g)
	}
}

func modularFind(c domain.Center, id string) (domain.ComposeModule, bool) {
	for _, m := range c.Deployment.Modules {
		if m.ID == id {
			return m, true
		}
	}
	return domain.ComposeModule{}, false
}

func TestCenterTimeRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	center := domain.Center{ID: "center-1", Name: "测试中心", Host: "192.0.2.1", Port: 22, Username: "root", AuthType: "password", Password: "encrypted", DockerSocket: "/var/run/docker.sock", CreatedAt: now, UpdatedAt: now}
	if err = st.SaveCenter(center); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetCenter(center.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.CreatedAt.Equal(now) || !loaded.UpdatedAt.Equal(now) {
		t.Fatalf("time mismatch: %v %v", loaded.CreatedAt, loaded.UpdatedAt)
	}
	list, err := st.ListCenters()
	if err != nil || len(list) != 1 {
		t.Fatalf("list failed: %#v %v", list, err)
	}
}

func TestCenterRuntimeSchemaUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-center.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE centers (id TEXT PRIMARY KEY,name TEXT NOT NULL,host TEXT NOT NULL,port INTEGER NOT NULL,username TEXT NOT NULL,auth_type TEXT NOT NULL,password TEXT NOT NULL DEFAULT '',private_key TEXT NOT NULL DEFAULT '',passphrase TEXT NOT NULL DEFAULT '',host_key TEXT NOT NULL DEFAULT '',docker_socket TEXT NOT NULL,accelerator TEXT NOT NULL DEFAULT 'auto',compose_file TEXT NOT NULL DEFAULT '/opt/center/compose.yml',description TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO centers(id,name,host,port,username,auth_type,password,docker_socket,accelerator,compose_file,created_at,updated_at) VALUES('legacy-center','旧中心','192.0.2.2',22,'root','password','encrypted','/var/run/docker.sock','auto','/opt/center/compose.yml',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	loaded, err := st.GetCenter("legacy-center")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CPUCount != 0 || loaded.GPUCount != 0 || loaded.DetectedAccelerator != "" || loaded.ComposeContent != "" || loaded.InitializedAt != nil {
		t.Fatalf("runtime migration defaults mismatch: %#v", loaded)
	}
	if loaded.WMSURL != "" || loaded.MisAuth != "" {
		t.Fatalf("错分检测字段迁移默认值不匹配: %#v", loaded)
	}
	if !loaded.MisSortEnabled {
		t.Fatal("错分检测开关迁移默认值应为启用（1），由 wms_url 是否非空决定实际生效")
	}
	if loaded.DebugLogEnabled {
		t.Fatal("Debug 模式开关迁移默认值应为关闭（0）")
	}
	loaded.WMSURL, loaded.MisAuth = "http://10.21.61.48:31995", "auth"
	loaded.TestMode = true
	if err = st.SaveCenter(loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := st.GetCenter("legacy-center")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WMSURL != "http://10.21.61.48:31995" || reloaded.MisAuth != "auth" {
		t.Fatalf("错分检测字段回读不匹配: %#v", reloaded)
	}
	// 关闭开关：配置值保留、开关位清零，重新打开恢复。
	reloaded.MisSortEnabled = false
	if err = st.SaveCenter(reloaded); err != nil {
		t.Fatal(err)
	}
	disabled, err := st.GetCenter("legacy-center")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.MisSortEnabled || disabled.WMSURL != "http://10.21.61.48:31995" || disabled.MisAuth != "auth" {
		t.Fatalf("关闭错分开关后配置值应保留: enabled=%v wms=%q mis=%q", disabled.MisSortEnabled, disabled.WMSURL, disabled.MisAuth)
	}
	reloaded.MisSortEnabled = true
	if !reloaded.TestMode {
		t.Fatal("告警控制字段回读应为 true")
	}
	reloaded.TestMode = false
	if err = st.SaveCenter(reloaded); err != nil {
		t.Fatal(err)
	}
	if again, err := st.GetCenter("legacy-center"); err != nil || again.TestMode {
		t.Fatal("告警控制字段应支持关闭回写")
	}
	reloaded.DebugLogEnabled = true
	if err = st.SaveCenter(reloaded); err != nil {
		t.Fatal(err)
	}
	if again, err := st.GetCenter("legacy-center"); err != nil || !again.DebugLogEnabled {
		t.Fatal("Debug 模式开关应支持开启回写")
	}
}

func TestAlertCredentialRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	credential := domain.AlertCredential{FeishuWebhook: "encrypted-webhook", Configured: true, UpdatedAt: now}
	if err = st.SaveAlertCredential(credential); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetAlertCredential()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FeishuWebhook != credential.FeishuWebhook || !loaded.Configured || !loaded.UpdatedAt.Equal(now) {
		t.Fatalf("alert credential mismatch: %#v", loaded)
	}
}

func TestOSSCredentialModelsPrefixSchemaUpgrade(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "legacy-oss.db")
	db, err := sql.Open("sqlite", filePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE oss_credentials (id INTEGER PRIMARY KEY CHECK(id=1),endpoint TEXT NOT NULL,bucket TEXT NOT NULL,access_key_id TEXT NOT NULL,access_key_secret TEXT NOT NULL,images_prefix TEXT NOT NULL DEFAULT 'center-images/',updated_at TEXT NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO oss_credentials(id,endpoint,bucket,access_key_id,access_key_secret,images_prefix,updated_at) VALUES(1,'https://oss.example','bucket','key','encrypted','center-images/',CURRENT_TIMESTAMP)`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	credential, err := st.GetOSSCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.ModelsPrefix != "center-models/" || !credential.Configured {
		t.Fatalf("OSS credential migration mismatch: %#v", credential)
	}
}

func TestIncorrectModelsPrefixIsMigratedOnlyOnce(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "incorrect-model-prefix.db")
	db, err := sql.Open("sqlite", filePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE oss_credentials (id INTEGER PRIMARY KEY CHECK(id=1),endpoint TEXT NOT NULL,bucket TEXT NOT NULL,access_key_id TEXT NOT NULL,access_key_secret TEXT NOT NULL,images_prefix TEXT NOT NULL DEFAULT 'center-images/',models_prefix TEXT NOT NULL DEFAULT 'models/',updated_at TEXT NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO oss_credentials(id,endpoint,bucket,access_key_id,access_key_secret,images_prefix,models_prefix,updated_at) VALUES(1,'https://oss.example','pro-alg-train-store','key','encrypted','center-images/','models/',CURRENT_TIMESTAMP)`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := st.GetOSSCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.ModelsPrefix != "center-models/" {
		t.Fatalf("incorrect prefix was not migrated: %#v", credential)
	}
	st.Close()

	st, err = Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	credential.ModelsPrefix = "models/"
	credential.UpdatedAt = time.Now()
	if err = st.SaveOSSCredential(credential); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	credential, err = st.GetOSSCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.ModelsPrefix != "models/" {
		t.Fatalf("completed migration must not overwrite a later explicit setting: %#v", credential)
	}
}

func TestRegistrySettingsAcceptsScriptTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SaveRegistrySettings(domain.RegistrySettings{Endpoint: "http://127.0.0.1:5000", Configured: true, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Simulate script/manual ISO timestamps (T-separated, no zone) - once caused scan errors.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE registry_settings SET updated_at='2026-10-15T18:10:44.044570' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	loaded, err := st.GetRegistrySettings()
	if err != nil {
		t.Fatalf("�����ű�д���ʱ��ʧ��: %v", err)
	}
	if !loaded.Configured || loaded.Endpoint != "http://127.0.0.1:5000" {
		t.Fatalf("loaded = %+v", loaded)
	}
}
