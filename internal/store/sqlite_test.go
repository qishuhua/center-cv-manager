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
