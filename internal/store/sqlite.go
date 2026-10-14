package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("记录不存在")

type Store struct{ db *sql.DB }

// sqliteTime accepts both the legacy time.Time.String representation used by
// modernc/sqlite and RFC3339 values. The schema intentionally remains TEXT so
// existing databases require no migration.
type sqliteTime struct{ time.Time }

func (t *sqliteTime) Scan(value any) error {
	var raw string
	switch v := value.(type) {
	case time.Time:
		t.Time = v
		return nil
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("不支持的时间类型 %T", value)
	}
	if index := strings.Index(raw, " m="); index >= 0 {
		raw = strings.TrimSpace(raw[:index])
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("无法解析时间 %q", raw)
}

type nullableSQLiteTime struct {
	time.Time
	Valid bool
}

func (t *nullableSQLiteTime) Scan(value any) error {
	if value == nil {
		t.Valid = false
		return nil
	}
	var parsed sqliteTime
	if err := parsed.Scan(value); err != nil {
		return err
	}
	t.Time, t.Valid = parsed.Time, true
	return nil
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// synchronous=NORMAL：WAL 模式下提交不再逐条 fsync（仅 checkpoint 时落盘），
	// 大幅提高并发任务写日志的吞吐；进程崩溃不丢数据，仅操作系统崩溃可能丢
	// 最后几条已提交日志，对本地管理工具可接受。
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=NORMAL`, `PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS centers (id TEXT PRIMARY KEY,name TEXT NOT NULL,host TEXT NOT NULL,port INTEGER NOT NULL,console_port INTEGER NOT NULL DEFAULT 8080,username TEXT NOT NULL,auth_type TEXT NOT NULL,password TEXT NOT NULL DEFAULT '',private_key TEXT NOT NULL DEFAULT '',passphrase TEXT NOT NULL DEFAULT '',host_key TEXT NOT NULL DEFAULT '',docker_socket TEXT NOT NULL,accelerator TEXT NOT NULL DEFAULT 'auto',detected_accelerator TEXT NOT NULL DEFAULT '',cpu_count INTEGER NOT NULL DEFAULT 0,gpu_count INTEGER NOT NULL DEFAULT 0,compose_file TEXT NOT NULL DEFAULT '~/.local/share/center-manager/deploy/docker-compose.yml',compose_content TEXT NOT NULL DEFAULT '',initialized_at TEXT,description TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sources (id TEXT PRIMARY KEY,name TEXT NOT NULL,repository TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS git_credentials (id INTEGER PRIMARY KEY CHECK(id=1),base_url TEXT NOT NULL,username TEXT NOT NULL,token TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS oss_credentials (id INTEGER PRIMARY KEY CHECK(id=1),endpoint TEXT NOT NULL,bucket TEXT NOT NULL,access_key_id TEXT NOT NULL,access_key_secret TEXT NOT NULL,images_prefix TEXT NOT NULL DEFAULT 'center-images/',models_prefix TEXT NOT NULL DEFAULT 'center-models/',updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS alert_credentials (id INTEGER PRIMARY KEY CHECK(id=1),feishu_webhook TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS checkouts (source_id TEXT NOT NULL,ref TEXT NOT NULL,commit_hash TEXT NOT NULL,path TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(source_id,ref),FOREIGN KEY(source_id) REFERENCES sources(id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY,type TEXT NOT NULL,status TEXT NOT NULL,progress REAL NOT NULL,message TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',params TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT)`,
		`UPDATE tasks SET message='源码下载完成' WHERE type='DOWNLOAD_SOURCE' AND status='SUCCESS' AND progress>=100 AND message='正在下载源码'`,
		`CREATE TABLE IF NOT EXISTS task_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL,line TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_task_logs_task_id ON task_logs(task_id)`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("初始化数据库: %w", err)
		}
	}
	if err := ensureColumn(db, "centers", "accelerator", `TEXT NOT NULL DEFAULT 'auto'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("升级数据库: %w", err)
	}
	if err := ensureColumn(db, "centers", "compose_file", `TEXT NOT NULL DEFAULT '~/.local/share/center-manager/deploy/docker-compose.yml'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("升级数据库: %w", err)
	}
	for _, migration := range []struct{ column, definition string }{
		{"console_port", `INTEGER NOT NULL DEFAULT 8080`},
		{"detected_accelerator", `TEXT NOT NULL DEFAULT ''`},
		{"cpu_count", `INTEGER NOT NULL DEFAULT 0`},
		{"gpu_count", `INTEGER NOT NULL DEFAULT 0`},
		{"compose_content", `TEXT NOT NULL DEFAULT ''`},
		{"initialized_at", `TEXT`},
		{"deployment_json", `TEXT NOT NULL DEFAULT 'null'`},
	} {
		if err := ensureColumn(db, "centers", migration.column, migration.definition); err != nil {
			db.Close()
			return nil, fmt.Errorf("升级数据库: %w", err)
		}
	}
	if err := ensureColumn(db, "oss_credentials", "models_prefix", `TEXT NOT NULL DEFAULT 'center-models/'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("升级数据库: %w", err)
	}
	if err := migrateIncorrectModelsPrefix(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("升级数据库: %w", err)
	}
	return &Store{db: db}, nil
}

func migrateIncorrectModelsPrefix(db *sql.DB) error {
	const migration = "fix_models_prefix_center_models_v1"
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, migration).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE oss_credentials SET models_prefix='center-models/' WHERE models_prefix='models/'`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(name,applied_at) VALUES(?,?)`, migration, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ListCenters() ([]domain.Center, error) {
	rows, err := s.db.Query(`SELECT id,name,host,port,console_port,username,auth_type,password,private_key,passphrase,host_key,docker_socket,accelerator,detected_accelerator,cpu_count,gpu_count,compose_file,compose_content,initialized_at,description,created_at,updated_at,deployment_json FROM centers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Center, 0)
	for rows.Next() {
		var v domain.Center
		var createdAt, updatedAt sqliteTime
		var initializedAt nullableSQLiteTime
		var deploymentJSON string
		if err = rows.Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.ConsolePort, &v.Username, &v.AuthType, &v.Password, &v.PrivateKey, &v.Passphrase, &v.HostKey, &v.DockerSocket, &v.Accelerator, &v.DetectedAccelerator, &v.CPUCount, &v.GPUCount, &v.ComposeFile, &v.ComposeContent, &initializedAt, &v.Description, &createdAt, &updatedAt, &deploymentJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(deploymentJSON), &v.Deployment); err != nil {
			return nil, err
		}
		v.CreatedAt, v.UpdatedAt = createdAt.Time, updatedAt.Time
		if initializedAt.Valid {
			v.InitializedAt = &initializedAt.Time
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetCenter(id string) (domain.Center, error) {
	var v domain.Center
	var createdAt, updatedAt sqliteTime
	var initializedAt nullableSQLiteTime
	var deploymentJSON string
	err := s.db.QueryRow(`SELECT id,name,host,port,console_port,username,auth_type,password,private_key,passphrase,host_key,docker_socket,accelerator,detected_accelerator,cpu_count,gpu_count,compose_file,compose_content,initialized_at,description,created_at,updated_at,deployment_json FROM centers WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.ConsolePort, &v.Username, &v.AuthType, &v.Password, &v.PrivateKey, &v.Passphrase, &v.HostKey, &v.DockerSocket, &v.Accelerator, &v.DetectedAccelerator, &v.CPUCount, &v.GPUCount, &v.ComposeFile, &v.ComposeContent, &initializedAt, &v.Description, &createdAt, &updatedAt, &deploymentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		if decodeErr := json.Unmarshal([]byte(deploymentJSON), &v.Deployment); decodeErr != nil {
			return v, decodeErr
		}
		v.CreatedAt, v.UpdatedAt = createdAt.Time, updatedAt.Time
		if initializedAt.Valid {
			v.InitializedAt = &initializedAt.Time
		}
	}
	return v, err
}

func (s *Store) SaveCenter(v domain.Center) error {
	deploymentJSON, marshalErr := json.Marshal(v.Deployment)
	if marshalErr != nil {
		return marshalErr
	}
	_, err := s.db.Exec(`INSERT INTO centers(id,name,host,port,console_port,username,auth_type,password,private_key,passphrase,host_key,docker_socket,accelerator,detected_accelerator,cpu_count,gpu_count,compose_file,compose_content,initialized_at,description,created_at,updated_at,deployment_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,host=excluded.host,port=excluded.port,console_port=excluded.console_port,username=excluded.username,auth_type=excluded.auth_type,password=excluded.password,private_key=excluded.private_key,passphrase=excluded.passphrase,host_key=excluded.host_key,docker_socket=excluded.docker_socket,accelerator=excluded.accelerator,detected_accelerator=excluded.detected_accelerator,cpu_count=excluded.cpu_count,gpu_count=excluded.gpu_count,compose_file=excluded.compose_file,compose_content=excluded.compose_content,initialized_at=excluded.initialized_at,description=excluded.description,updated_at=excluded.updated_at,deployment_json=excluded.deployment_json`, v.ID, v.Name, v.Host, v.Port, v.ConsolePort, v.Username, v.AuthType, v.Password, v.PrivateKey, v.Passphrase, v.HostKey, v.DockerSocket, v.Accelerator, v.DetectedAccelerator, v.CPUCount, v.GPUCount, v.ComposeFile, v.ComposeContent, v.InitializedAt, v.Description, v.CreatedAt, v.UpdatedAt, string(deploymentJSON))
	return err
}

func (s *Store) DeleteCenter(id string) error {
	r, e := s.db.Exec(`DELETE FROM centers WHERE id=?`, id)
	if e == nil {
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return e
}

func (s *Store) ListSources() ([]domain.Source, error) {
	rows, e := s.db.Query(`SELECT id,name,repository,description,created_at,updated_at FROM sources ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]domain.Source, 0)
	for rows.Next() {
		var v domain.Source
		var createdAt, updatedAt sqliteTime
		if e = rows.Scan(&v.ID, &v.Name, &v.Repository, &v.Description, &createdAt, &updatedAt); e != nil {
			return nil, e
		}
		v.CreatedAt, v.UpdatedAt = createdAt.Time, updatedAt.Time
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) GetSource(id string) (domain.Source, error) {
	var v domain.Source
	var createdAt, updatedAt sqliteTime
	e := s.db.QueryRow(`SELECT id,name,repository,description,created_at,updated_at FROM sources WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.Repository, &v.Description, &createdAt, &updatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	if e == nil {
		v.CreatedAt, v.UpdatedAt = createdAt.Time, updatedAt.Time
	}
	return v, e
}
func (s *Store) SaveSource(v domain.Source) error {
	_, e := s.db.Exec(`INSERT INTO sources(id,name,repository,description,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,repository=excluded.repository,description=excluded.description,updated_at=excluded.updated_at`, v.ID, v.Name, v.Repository, v.Description, v.CreatedAt, v.UpdatedAt)
	return e
}
func (s *Store) DeleteSource(id string) error {
	r, e := s.db.Exec(`DELETE FROM sources WHERE id=?`, id)
	if e == nil {
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return e
}

func (s *Store) GetGitCredential() (domain.GitCredential, error) {
	var v domain.GitCredential
	var updatedAt sqliteTime
	e := s.db.QueryRow(`SELECT base_url,username,token,updated_at FROM git_credentials WHERE id=1`).Scan(&v.BaseURL, &v.Username, &v.Token, &updatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return domain.GitCredential{}, nil
	}
	if e == nil {
		v.Configured = v.Token != ""
		v.UpdatedAt = updatedAt.Time
	}
	return v, e
}

func (s *Store) SaveGitCredential(v domain.GitCredential) error {
	_, e := s.db.Exec(`INSERT INTO git_credentials(id,base_url,username,token,updated_at) VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET base_url=excluded.base_url,username=excluded.username,token=excluded.token,updated_at=excluded.updated_at`, v.BaseURL, v.Username, v.Token, v.UpdatedAt)
	return e
}

func (s *Store) GetOSSCredential() (domain.OSSCredential, error) {
	var v domain.OSSCredential
	var updatedAt sqliteTime
	err := s.db.QueryRow(`SELECT endpoint,bucket,access_key_id,access_key_secret,images_prefix,models_prefix,updated_at FROM oss_credentials WHERE id=1`).Scan(&v.Endpoint, &v.Bucket, &v.AccessKeyID, &v.AccessKeySecret, &v.ImagesPrefix, &v.ModelsPrefix, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OSSCredential{ImagesPrefix: "center-images/", ModelsPrefix: "center-models/"}, nil
	}
	if err == nil {
		v.Configured = v.Endpoint != "" && v.Bucket != "" && v.AccessKeyID != "" && v.AccessKeySecret != ""
		v.UpdatedAt = updatedAt.Time
	}
	return v, err
}

func (s *Store) SaveOSSCredential(v domain.OSSCredential) error {
	_, err := s.db.Exec(`INSERT INTO oss_credentials(id,endpoint,bucket,access_key_id,access_key_secret,images_prefix,models_prefix,updated_at) VALUES(1,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET endpoint=excluded.endpoint,bucket=excluded.bucket,access_key_id=excluded.access_key_id,access_key_secret=excluded.access_key_secret,images_prefix=excluded.images_prefix,models_prefix=excluded.models_prefix,updated_at=excluded.updated_at`, v.Endpoint, v.Bucket, v.AccessKeyID, v.AccessKeySecret, v.ImagesPrefix, v.ModelsPrefix, v.UpdatedAt)
	return err
}

func (s *Store) GetAlertCredential() (domain.AlertCredential, error) {
	var value domain.AlertCredential
	var updatedAt sqliteTime
	err := s.db.QueryRow(`SELECT feishu_webhook,updated_at FROM alert_credentials WHERE id=1`).Scan(&value.FeishuWebhook, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AlertCredential{}, nil
	}
	if err == nil {
		value.Configured = value.FeishuWebhook != ""
		value.UpdatedAt = updatedAt.Time
	}
	return value, err
}

func (s *Store) SaveAlertCredential(value domain.AlertCredential) error {
	_, err := s.db.Exec(`INSERT INTO alert_credentials(id,feishu_webhook,updated_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET feishu_webhook=excluded.feishu_webhook,updated_at=excluded.updated_at`, value.FeishuWebhook, value.UpdatedAt)
	return err
}

func (s *Store) SaveCheckout(v domain.Checkout) error {
	_, e := s.db.Exec(`INSERT INTO checkouts(source_id,ref,commit_hash,path,created_at) VALUES(?,?,?,?,?) ON CONFLICT(source_id,ref) DO UPDATE SET commit_hash=excluded.commit_hash,path=excluded.path,created_at=excluded.created_at`, v.SourceID, v.Ref, v.Commit, v.Path, v.CreatedAt)
	return e
}
func (s *Store) GetCheckout(sourceID, ref string) (domain.Checkout, error) {
	var v domain.Checkout
	var createdAt sqliteTime
	e := s.db.QueryRow(`SELECT source_id,ref,commit_hash,path,created_at FROM checkouts WHERE source_id=? AND ref=?`, sourceID, ref).Scan(&v.SourceID, &v.Ref, &v.Commit, &v.Path, &createdAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	if e == nil {
		v.CreatedAt = createdAt.Time
	}
	return v, e
}
func (s *Store) ListCheckouts(sourceID string) ([]domain.Checkout, error) {
	rows, e := s.db.Query(`SELECT source_id,ref,commit_hash,path,created_at FROM checkouts WHERE source_id=? ORDER BY created_at DESC`, sourceID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]domain.Checkout, 0)
	for rows.Next() {
		var v domain.Checkout
		var createdAt sqliteTime
		if e = rows.Scan(&v.SourceID, &v.Ref, &v.Commit, &v.Path, &createdAt); e != nil {
			return nil, e
		}
		v.CreatedAt = createdAt.Time
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveTask(t *domain.Task) error {
	params, _ := json.Marshal(t.Params)
	_, e := s.db.Exec(`INSERT INTO tasks(id,type,status,progress,message,error,params,created_at,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,progress=excluded.progress,message=excluded.message,error=excluded.error,params=excluded.params,started_at=excluded.started_at,finished_at=excluded.finished_at`, t.ID, t.Type, t.Status, t.Progress, t.Message, t.Error, string(params), t.CreatedAt, t.StartedAt, t.FinishedAt)
	return e
}
func (s *Store) AppendLog(id, line string) error {
	_, e := s.db.Exec(`INSERT INTO task_logs(task_id,line,created_at) VALUES(?,?,?)`, id, line, time.Now())
	return e
}
func (s *Store) FailInterruptedTasks(finishedAt time.Time, message string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM tasks WHERE status IN ('PENDING','RUNNING')`)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, tx.Commit()
	}
	logLine := finishedAt.Format("15:04:05") + " 任务失败: " + message
	for _, id := range ids {
		if _, err = tx.Exec(`INSERT INTO task_logs(task_id,line,created_at) VALUES(?,?,?)`, id, logLine, finishedAt); err != nil {
			return 0, err
		}
	}
	result, err := tx.Exec(`UPDATE tasks SET status='FAILED',message=?,error=?,finished_at=? WHERE status IN ('PENDING','RUNNING')`, message, message, finishedAt)
	if err != nil {
		return 0, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if updated != int64(len(ids)) {
		return 0, fmt.Errorf("中断任务数量发生变化: 日志=%d, 状态=%d", len(ids), updated)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}
func (s *Store) DeleteTask(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM task_logs WHERE task_id=?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
func (s *Store) ListTasks(limit int) ([]*domain.Task, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, e := s.db.Query(`SELECT t.id,t.type,t.status,t.progress,t.message,t.error,t.params,t.created_at,t.started_at,t.finished_at,COUNT(l.id) FROM tasks t LEFT JOIN task_logs l ON l.task_id=t.id GROUP BY t.id ORDER BY t.created_at DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]*domain.Task, 0)
	for rows.Next() {
		t := new(domain.Task)
		var p string
		var createdAt sqliteTime
		var startedAt, finishedAt nullableSQLiteTime
		if e = rows.Scan(&t.ID, &t.Type, &t.Status, &t.Progress, &t.Message, &t.Error, &p, &createdAt, &startedAt, &finishedAt, &t.LogCount); e != nil {
			return nil, e
		}
		t.CreatedAt = createdAt.Time
		if startedAt.Valid {
			value := startedAt.Time
			t.StartedAt = &value
		}
		if finishedAt.Valid {
			value := finishedAt.Time
			t.FinishedAt = &value
		}
		_ = json.Unmarshal([]byte(p), &t.Params)
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) GetTask(id string) (*domain.Task, error) {
	t := new(domain.Task)
	var p string
	var createdAt sqliteTime
	var startedAt, finishedAt nullableSQLiteTime
	e := s.db.QueryRow(`SELECT t.id,t.type,t.status,t.progress,t.message,t.error,t.params,t.created_at,t.started_at,t.finished_at,COUNT(l.id) FROM tasks t LEFT JOIN task_logs l ON l.task_id=t.id WHERE t.id=? GROUP BY t.id`, id).Scan(&t.ID, &t.Type, &t.Status, &t.Progress, &t.Message, &t.Error, &p, &createdAt, &startedAt, &finishedAt, &t.LogCount)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e == nil {
		t.CreatedAt = createdAt.Time
		if startedAt.Valid {
			value := startedAt.Time
			t.StartedAt = &value
		}
		if finishedAt.Valid {
			value := finishedAt.Time
			t.FinishedAt = &value
		}
	}
	_ = json.Unmarshal([]byte(p), &t.Params)
	return t, e
}
func (s *Store) GetLogs(id string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, e := s.db.Query(`SELECT line FROM task_logs WHERE task_id=? ORDER BY id DESC LIMIT ?`, id, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	rev := make([]string, 0)
	for rows.Next() {
		var line string
		if e = rows.Scan(&line); e != nil {
			return nil, e
		}
		rev = append(rev, line)
	}
	out := make([]string, len(rev))
	for i := range rev {
		out[len(rev)-1-i] = rev[i]
	}
	return out, rows.Err()
}

func (s *Store) RememberCenterHostKey(id, fingerprint string) error {
	_, err := s.db.Exec(`UPDATE centers SET host_key=? WHERE id=? AND host_key=''`, fingerprint, id)
	return err
}
