package domain

import "time"

type Center struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	Host                string      `json:"host"`
	Port                int         `json:"port"`
	ConsolePort         int         `json:"console_port"`
	Username            string      `json:"username"`
	AuthType            string      `json:"auth_type"`
	Password            string      `json:"password,omitempty"`
	PrivateKey          string      `json:"private_key,omitempty"`
	Passphrase          string      `json:"passphrase,omitempty"`
	HostKey             string      `json:"host_key,omitempty"`
	DockerSocket        string      `json:"docker_socket"`
	Accelerator         string      `json:"accelerator"`
	DetectedAccelerator string      `json:"detected_accelerator,omitempty"`
	CPUCount            int         `json:"cpu_count"`
	GPUCount            int         `json:"gpu_count"`
	ComposeFile         string      `json:"compose_file"`
	ComposeContent      string      `json:"-"`
	Deployment          *Deployment `json:"deployment,omitempty"`
	ComposeProject      string      `json:"-"`
	InitializedAt       *time.Time  `json:"initialized_at,omitempty"`
	Description         string      `json:"description"`
	WMSURL              string      `json:"wms_url,omitempty"`
	MisAuth             string      `json:"mis_auth,omitempty"`
	// MisSortEnabled 是错分检测的启用开关；关闭时 WMSURL/MisAuth 原值保留，
	// 重新开启后直接恢复使用。启用判定 = WMSURL 非空且本开关为 true。
	MisSortEnabled bool `json:"mis_sort_enabled,omitempty"`
	// MisSortIntervalSeconds 是错分检测开启时 ingress 摄像机配置轮询周期
	//（秒，module.env 托管 CV_CAMERA_POLL_INTERVAL_SECONDS）；0 视为未配置，
	// 由保存/渲染层回填默认 1 小时。关闭开关时值同样保留在中心记录。
	MisSortIntervalSeconds int `json:"mis_sort_interval_seconds,omitempty"`
	// DebugLogEnabled 开启后业务 Worker（center-cv-service）日志等级改为
	// debug（module.env 托管 LOG_LEVEL）；关闭移除托管值，回落 Compose
	// 插值默认 info。仅模块化中心支持。
	DebugLogEnabled bool `json:"debug_log_enabled,omitempty"`
	// SaveImageEnabled 开启后业务 Worker（center-cv-service）每轮推理保存
	// 调试图片（module.env 托管 CV_SAVE_DEBUG_IMAGE）；关闭移除托管值，
	// 回落 Compose 插值默认 0（不存图）。仅模块化中心支持。
	SaveImageEnabled bool `json:"save_image_enabled,omitempty"`
	// DecodeOnlyEnabled 开启后业务 Worker（center-cv-service）进入仅解码
	// 模式（module.env 托管 CV_DECODE_ONLY_MODE）：只拉流、解码、按任务
	// 既有采样策略抽帧，不加载模型、不推理，也不产生事件/录片/告警；关闭
	// 移除托管值，回落 Compose 插值默认 0。仅模块化中心支持；该环境变量
	// 仅进程启动时读取，修改后需重启 Worker 生效。
	DecodeOnlyEnabled bool `json:"decode_only_enabled,omitempty"`
	// SingleInstanceEnabled 开启后 business 模块只部署一个 center-cv-service
	// 实例：可见全部 GPU（nvidia NVIDIA_VISIBLE_DEVICES=all、登临全部分配
	// 逻辑卡）、不绑定 cpuset（由调度器使用全部 CPU）。与 Worker/CPU 拓扑
	// 一样只在初始化渲染时生效，已初始化中心切换需先重置。
	SingleInstanceEnabled bool `json:"single_instance_enabled,omitempty"`
	// 飞书告警测试由 video-transcode 使用，机器人地址明文入库并明文返回
	//（不按凭据处理，编辑页直接回显）。
	TestAlarmEnabled bool      `json:"test_alarm_enabled,omitempty"`
	FeishuWebhookURL string    `json:"feishu_webhook_url,omitempty"`
	TestMode         bool      `json:"test_mode,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Deployment keeps center paths independent of any individual Compose entry.
// A nil Deployment is a legacy center and is never implicitly migrated.
type Deployment struct {
	ReselectionRequired bool             `json:"reselection_required,omitempty"`
	Migration           *ModuleMigration `json:"migration,omitempty"`
	Root                string           `json:"root"`
	ModelsRoot          string           `json:"models_root"`
	LogsRoot            string           `json:"logs_root"`
	MediaRoot           string           `json:"media_root"`
	Status              string           `json:"status"`
	Modules             []ComposeModule  `json:"modules"`
}

type ModuleMigration struct {
	Projects       []string `json:"projects"`
	SourceRevision string   `json:"source_revision"`
	Project        string   `json:"project"`
	Containers     []string `json:"containers"`
	Completed      bool     `json:"completed"`
}

type ComposeModule struct {
	FilesRevision string   `json:"files_revision,omitempty"`
	ConfigChanged bool     `json:"config_changed,omitempty"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Project       string   `json:"project"`
	ComposeFile   string   `json:"compose_file"`
	Enabled       bool     `json:"enabled"`
	DependsOn     []string `json:"depends_on"`
	InitServices  []string `json:"init_services"`
	ModelServices []string `json:"model_services"`
	ReadyURLs     []string `json:"ready_urls"`
	Timeout       int      `json:"timeout"`
	Status        string   `json:"status"`
	// RuntimeStatus 是模块运行状态（healthy/partial/starting/stopped/failed），
	// 仅由 Compose 列表接口从 deployment 记录回填（该接口的 Status 语义是
	// 发布状态 published/unpublished/unknown），持久化时恒为空、不入库。
	RuntimeStatus   string    `json:"runtime_status,omitempty"`
	Error           string    `json:"error,omitempty"`
	Revision        string    `json:"revision"`
	AppliedRevision string    `json:"applied_revision"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AlertCredential struct {
	FeishuWebhook string    `json:"feishu_webhook,omitempty"`
	Configured    bool      `json:"configured"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type OSSCredential struct {
	Endpoint        string    `json:"endpoint"`
	Bucket          string    `json:"bucket"`
	AccessKeyID     string    `json:"access_key_id"`
	AccessKeySecret string    `json:"access_key_secret,omitempty"`
	ImagesPrefix    string    `json:"images_prefix"`
	ModelsPrefix    string    `json:"models_prefix"`
	Configured      bool      `json:"configured"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

// RegistrySettings 是共享镜像仓库（registry:3 + OSS S3 兼容后端）的接入地址。
// 访问凭据不入库：registry 容器自身的 config.yml 由管理端渲染到数据目录。
type RegistrySettings struct {
	Endpoint   string    `json:"endpoint"`
	Configured bool      `json:"configured"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// ModelConvertPlatform 是一个平台（nvidia/denglin）的模型转换配置：指定已登记中心
// 作为构建机，经 SSH 在其上 docker run 专用镜像完成 ONNX → TensorRT/登临 engine 转换。
// 约定：源模型落构建机 /work/source.onnx，产物必须写 /work/output.plan（nvidia）
// 或 /work/output.engine（denglin）；Command 为容器内命令，路径按容器内视角书写。
type ModelConvertPlatform struct {
	CenterID   string `json:"center_id"`
	DockerArgs string `json:"docker_args"`
	Image      string `json:"image"`
	Command    string `json:"command"`
}

type ModelConvertSettings struct {
	Nvidia    ModelConvertPlatform `json:"nvidia"`
	Denglin   ModelConvertPlatform `json:"denglin"`
	UpdatedAt time.Time            `json:"updated_at,omitempty"`
}

// Configured 报告该平台是否完整配置（构建机、镜像、命令齐备），未配置的平台拒绝转换。
func (p ModelConvertPlatform) Configured() bool {
	return p.CenterID != "" && p.Image != "" && p.Command != ""
}

// RegistryStorage 是镜像仓库存储侧的唯一可配置项：OSS 凭据、Bucket 与
// Endpoint 全部复用模型仓库的全局 OSS 配置（oss_credentials），镜像仓库只在
// 共享 Bucket 内占一个独立前缀，不持有任何凭据。
type RegistryStorage struct {
	RootDirectory string    `json:"root_directory"`
	Exists        bool      `json:"exists"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type Source struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Repository  string    `json:"repository"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GitCredential struct {
	BaseURL    string    `json:"base_url"`
	Username   string    `json:"username"`
	Token      string    `json:"token,omitempty"`
	Configured bool      `json:"configured"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

type Checkout struct {
	SourceID  string    `json:"source_id"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

type Task struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Progress   float64           `json:"progress"`
	Message    string            `json:"message"`
	Error      string            `json:"error,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	LogCount   int               `json:"log_count"`
	CreatedAt  time.Time         `json:"created_at"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
}
