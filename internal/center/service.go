package center

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/sshx"
	"github.com/google/uuid"
)

const Mask = "********"

// defaultMisAuth 是两服务共用的 misAuth 公开约定默认值（上游 Compose 同值）。
const defaultMisAuth = "20260606123a"

type Repository interface {
	ListCenters() ([]domain.Center, error)
	GetCenter(string) (domain.Center, error)
	SaveCenter(domain.Center) error
	DeleteCenter(string) error
}
type Service struct {
	repo   Repository
	cipher *secret.Cipher
	// monitorSync 在中心记录变更后触发，由装配层注入统一监控的目标重渲染。
	// 中心记录的全部写路径（保存、删除、硬件检测、运行时更新、重置）都经
	// 此处通知，调用方无需各自记得同步；失败仅记日志，不影响业务结果。
	monitorSync func() error
}

func New(repo Repository, c *secret.Cipher) *Service { return &Service{repo: repo, cipher: c} }

func (s *Service) SetMonitorSync(fn func() error) { s.monitorSync = fn }

func (s *Service) notifyMonitorSync() {
	if s.monitorSync == nil {
		return
	}
	if err := s.monitorSync(); err != nil {
		log.Printf("统一监控配置同步失败: %v", err)
	}
}

func validate(v domain.Center) error {
	if strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Host) == "" || strings.TrimSpace(v.Username) == "" {
		return fmt.Errorf("名称、主机和用户名不能为空")
	}
	if v.Port < 1 || v.Port > 65535 {
		return fmt.Errorf("SSH 端口无效")
	}
	if v.ConsolePort < 1 || v.ConsolePort > 65535 {
		return fmt.Errorf("对外访问端口无效")
	}
	if v.AuthType != "password" && v.AuthType != "private_key" {
		return fmt.Errorf("认证方式必须是 password 或 private_key")
	}
	if v.Accelerator != "auto" && v.Accelerator != "nvidia" && v.Accelerator != "denglin" && v.Accelerator != "cpu" {
		return fmt.Errorf("加速卡类型必须是 auto、nvidia、denglin 或 cpu")
	}
	if !centerpath.ValidRemoteFile(v.ComposeFile) {
		return fmt.Errorf("Compose 文件必须是中心服务器上的绝对路径或 ~/ 开头的家目录路径")
	}
	if v.WMSURL != "" {
		parsed, err := url.Parse(v.WMSURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("WMS 服务地址必须是 http://主机[:端口] 形式的基地址")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("WMS 服务地址仅支持 http 或 https")
		}
		if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("WMS 服务地址只能是基地址，不能包含路径、查询参数、fragment 或凭据")
		}
		if v.MisAuth == "" {
			return fmt.Errorf("启用错分检测（填写 WMS 服务地址）时必须填写 misAuth 鉴权值")
		}
	}
	if err := validatePlainToken("misAuth 鉴权值", v.MisAuth, 512); err != nil {
		return err
	}
	return nil
}

// validatePlainToken 校验与两服务约定一致的标识值：非空时限定长度且不含空白/控制字符。
func validatePlainToken(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s长度不能超过 %d 字节", name, maxBytes)
	}
	for _, ch := range value {
		if ch <= 0x20 || ch == 0x7f {
			return fmt.Errorf("%s不能包含空白或控制字符", name)
		}
	}
	return nil
}
func (s *Service) List() ([]domain.Center, error) {
	v, e := s.repo.ListCenters()
	if e != nil {
		return nil, e
	}
	for i := range v {
		s.mask(&v[i])
	}
	return v, nil
}
func (s *Service) GetPublic(id string) (domain.Center, error) {
	v, e := s.repo.GetCenter(id)
	if e == nil {
		s.mask(&v)
	}
	return v, e
}
func (s *Service) Save(input domain.Center) (domain.Center, error) {
	if input.ID != "" {
		release := centerlock.Acquire(input.ID)
		defer release()
	}
	if input.Port == 0 {
		input.Port = 22
	}
	if input.ConsolePort == 0 {
		input.ConsolePort = 8080
	}
	if input.DockerSocket == "" {
		input.DockerSocket = "/var/run/docker.sock"
	}
	if input.Accelerator == "" {
		input.Accelerator = "auto"
	}
	if input.ComposeFile == "" {
		input.ComposeFile = centerpath.DefaultComposeFile
	}
	// 与两服务 CV_WMS_URL 口径一致：去掉首尾空白与尾斜杠后入库。
	// 机构编码不再配置：两服务用 Compose 插值默认值（chute 637101）。
	input.WMSURL = strings.TrimRight(strings.TrimSpace(input.WMSURL), "/")
	input.MisAuth = strings.TrimSpace(input.MisAuth)
	// misAuth 留空时使用公开约定默认值（上游 Compose 同值），写入 module.env 时始终显式。
	if input.WMSURL != "" && input.MisAuth == "" {
		input.MisAuth = defaultMisAuth
	}
	now := time.Now()
	var old domain.Center
	var exists bool
	if input.ID != "" {
		old, exists = func() (domain.Center, bool) { v, e := s.repo.GetCenter(input.ID); return v, e == nil }()
	} else {
		input.ID = uuid.NewString()
	}
	if exists {
		if old.Deployment == nil && (input.WMSURL != "" || input.MisAuth != "") {
			return domain.Center{}, fmt.Errorf("旧中心（未模块化）不支持错分检测配置")
		}
		if old.Deployment == nil && input.DebugLogEnabled {
			return domain.Center{}, fmt.Errorf("旧中心（未模块化）不支持 Debug 模式")
		}
		if old.Deployment == nil && input.SaveImageEnabled {
			return domain.Center{}, fmt.Errorf("旧中心（未模块化）不支持存图")
		}
		input.CreatedAt = old.CreatedAt
		input.Deployment = old.Deployment
		if old.Deployment != nil && input.ComposeFile != old.ComposeFile {
			return domain.Center{}, fmt.Errorf("模块化中心不能通过连接设置移动部署目录")
		}
		input.DetectedAccelerator = old.DetectedAccelerator
		input.CPUCount = old.CPUCount
		input.GPUCount = old.GPUCount
		input.ComposeContent = old.ComposeContent
		input.InitializedAt = old.InitializedAt
		if input.Host != old.Host || input.Port != old.Port {
			input.HostKey = ""
		}
		if input.Password == "" || input.Password == Mask {
			input.Password = old.Password
		} else {
			input.Password = s.encrypt(input.Password)
		}
		if input.PrivateKey == "" || input.PrivateKey == Mask {
			input.PrivateKey = old.PrivateKey
		} else {
			input.PrivateKey = s.encrypt(input.PrivateKey)
		}
		if input.Passphrase == "" || input.Passphrase == Mask {
			input.Passphrase = old.Passphrase
		} else {
			input.Passphrase = s.encrypt(input.Passphrase)
		}
		if input.HostKey == "" && input.Host == old.Host && input.Port == old.Port {
			input.HostKey = old.HostKey
		}
	} else {
		input.CreatedAt = now
		input.Deployment = modular.NewDeployment(input.ComposeFile, 1)
		input.Password = s.encrypt(input.Password)
		input.PrivateKey = s.encrypt(input.PrivateKey)
		input.Passphrase = s.encrypt(input.Passphrase)
	}
	input.UpdatedAt = now
	if err := validate(input); err != nil {
		return domain.Center{}, err
	}
	if input.AuthType == "password" && input.Password == "" {
		return domain.Center{}, fmt.Errorf("密码认证需要填写密码")
	}
	if input.AuthType == "private_key" && input.PrivateKey == "" {
		return domain.Center{}, fmt.Errorf("私钥认证需要填写私钥")
	}
	if err := s.repo.SaveCenter(input); err != nil {
		return domain.Center{}, err
	}
	s.notifyMonitorSync()
	s.mask(&input)
	return input, nil
}

func (s *Service) UpdateRuntime(id, accelerator string, cpuCount, gpuCount int, composeFile, composeContent string) error {
	v, err := s.repo.GetCenter(id)
	if err != nil {
		return err
	}
	now := time.Now()
	v.DetectedAccelerator = accelerator
	v.CPUCount = cpuCount
	v.GPUCount = gpuCount
	v.ComposeFile = composeFile
	v.ComposeContent = composeContent
	if v.Deployment == nil {
		v.InitializedAt = &now
	}
	v.UpdatedAt = now
	if err = s.repo.SaveCenter(v); err != nil {
		return err
	}
	s.notifyMonitorSync()
	return nil
}

// UpdateHardware refreshes detected hardware without marking the center as
// initialized or changing its Compose configuration.
func (s *Service) UpdateHardware(id, accelerator string, cpuCount, gpuCount int) error {
	v, err := s.repo.GetCenter(id)
	if err != nil {
		return err
	}
	v.DetectedAccelerator = accelerator
	v.CPUCount = cpuCount
	v.GPUCount = gpuCount
	v.UpdatedAt = time.Now()
	if err = s.repo.SaveCenter(v); err != nil {
		return err
	}
	s.notifyMonitorSync()
	return nil
}

func (s *Service) Delete(id string) error {
	if err := s.repo.DeleteCenter(id); err != nil {
		return err
	}
	s.notifyMonitorSync()
	return nil
}
func (s *Service) Connection(id string) (sshx.Config, string, domain.Center, error) {
	v, e := s.repo.GetCenter(id)
	if e != nil {
		return sshx.Config{}, "", v, e
	}
	password, e := s.decrypt(v.Password)
	if e != nil {
		return sshx.Config{}, "", v, e
	}
	key, e := s.decrypt(v.PrivateKey)
	if e != nil {
		return sshx.Config{}, "", v, e
	}
	pass, e := s.decrypt(v.Passphrase)
	if e != nil {
		return sshx.Config{}, "", v, e
	}
	return sshx.Config{Host: v.Host, Port: v.Port, Username: v.Username, Password: password, PrivateKey: key, Passphrase: pass, HostKey: v.HostKey}, v.DockerSocket, v, nil
}

func (s *Service) RememberHostKey(v domain.Center, fp string) error {
	if v.HostKey == "" && fp != "" {
		if repo, ok := s.repo.(interface{ RememberCenterHostKey(string, string) error }); ok {
			return repo.RememberCenterHostKey(v.ID, fp)
		}
		v.HostKey = fp
		v.UpdatedAt = time.Now()
		return s.repo.SaveCenter(v)
	}
	return nil
}
func (s *Service) Test(id string) (string, error) {
	cfg, socket, v, e := s.Connection(id)
	if e != nil {
		return "", e
	}
	remote, fp, e := sshx.NewDocker(cfg, socket)
	if e != nil {
		return fp, e
	}
	defer remote.Close()
	if e = s.RememberHostKey(v, fp); e != nil {
		return fp, e
	}
	return fp, nil
}
func (s *Service) encrypt(v string) string {
	out, e := s.cipher.Encrypt(v)
	if e != nil {
		panic(e)
	}
	return out
}
func (s *Service) decrypt(v string) (string, error) { return s.cipher.Decrypt(v) }
func (s *Service) mask(v *domain.Center) {
	if v.Password != "" {
		v.Password = Mask
	}
	if v.PrivateKey != "" {
		v.PrivateKey = Mask
	}
	if v.Passphrase != "" {
		v.Passphrase = Mask
	}
}

// UpdateDeployment is called under the center operation lock.
// ResetRuntime clears publication state after remote reset, keeping connection
// settings and data paths for an explicit subsequent initialization.
func (s *Service) ResetRuntime(id string, deployment *domain.Deployment) error {
	v, err := s.repo.GetCenter(id)
	if err != nil {
		return err
	}
	deployment.ReselectionRequired = true
	v.Deployment = deployment
	v.InitializedAt = nil
	v.ComposeContent = ""
	v.ComposeProject = ""
	v.UpdatedAt = time.Now()
	if err = s.repo.SaveCenter(v); err != nil {
		return err
	}
	s.notifyMonitorSync()
	return nil
}

func (s *Service) UpdateDeployment(id string, deployment *domain.Deployment) error {
	v, err := s.repo.GetCenter(id)
	if err != nil {
		return err
	}
	wasReady := v.Deployment != nil && v.Deployment.Status == "ready"
	v.Deployment = deployment
	v.UpdatedAt = time.Now()
	if deployment != nil && deployment.Status == "ready" && (!wasReady || v.InitializedAt == nil) {
		now := time.Now()
		v.InitializedAt = &now
	}
	return s.repo.SaveCenter(v)
}
