package domain

import "time"

type Center struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Host                string     `json:"host"`
	Port                int        `json:"port"`
	Username            string     `json:"username"`
	AuthType            string     `json:"auth_type"`
	Password            string     `json:"password,omitempty"`
	PrivateKey          string     `json:"private_key,omitempty"`
	Passphrase          string     `json:"passphrase,omitempty"`
	HostKey             string     `json:"host_key,omitempty"`
	DockerSocket        string     `json:"docker_socket"`
	Accelerator         string     `json:"accelerator"`
	DetectedAccelerator string     `json:"detected_accelerator,omitempty"`
	CPUCount            int        `json:"cpu_count"`
	GPUCount            int        `json:"gpu_count"`
	ComposeFile         string     `json:"compose_file"`
	ComposeContent      string     `json:"-"`
	InitializedAt       *time.Time `json:"initialized_at,omitempty"`
	Description         string     `json:"description"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
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
