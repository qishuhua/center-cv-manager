package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

type Reporter interface {
	Log(string)
	Logf(string, ...any)
	Progress(float64)
	Message(string)
}
type CenterResolver interface {
	Connection(string) (sshx.Config, string, domain.Center, error)
	RememberHostKey(domain.Center, string) error
}
type Service struct{ centers CenterResolver }

func New(centers CenterResolver) *Service { return &Service{centers: centers} }

var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,255}$`)
var invalidImagePart = regexp.MustCompile(`[^a-z0-9._-]+`)

const (
	LabelSourceID   = "com.center-manager.source.id"
	LabelSourceName = "com.center-manager.source.name"
	LabelSourceRef  = "com.center-manager.source.ref"
	LabelCommit     = "com.center-manager.source.commit"
)

func ValidateImageTag(value string) error {
	if !tagPattern.MatchString(value) {
		return fmt.Errorf("镜像标签无效")
	}
	return nil
}

func DefaultImageRepository(src domain.Source) string {
	name := sanitizeImagePart(src.Name)
	if name == "" {
		repository := strings.TrimSuffix(strings.TrimRight(src.Repository, "/"), ".git")
		if index := strings.LastIndexAny(repository, "/:"); index >= 0 {
			repository = repository[index+1:]
		}
		name = sanitizeImagePart(repository)
	}
	if name == "" {
		name = "software"
	}
	return name
}

func DefaultImageTag(src domain.Source, ref string) string {
	version := sanitizeImagePart(ref)
	if version == "" {
		version = "latest"
	}
	return DefaultImageRepository(src) + ":" + version
}

// ReleaseImageTag returns an immutable, human-readable tag for a source
// snapshot. A ref alone is not an artifact identity because branches (and even
// tags in some repositories) can move to another commit.
func ReleaseImageTag(src domain.Source, ref, commit string) string {
	version := sanitizeImagePart(ref)
	if version == "" {
		version = "release"
	}
	commit = sanitizeImagePart(commit)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		return DefaultImageTag(src, ref)
	}
	return DefaultImageRepository(src) + ":" + version + "-" + commit
}

// ReleaseVersionBody 返回镜像版本主体（sanitize(ref)-commit12）。对中心与
// 发布界面而言这就是"镜像版本"本身——Git ref 与 commit 只是构建输入，只在
// 软件仓库页（构建发布）出现。
func ReleaseVersionBody(ref, commit string) string {
	version := sanitizeImagePart(ref)
	if version == "" {
		version = "latest"
	}
	commit = sanitizeImagePart(commit)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		return version
	}
	return version + "-" + commit
}

// ReleaseRegistryTag 返回镜像仓库内的标签部分（版本主体 + 平台后缀，如
// master-2b6d1a220210-nvidia）：不含仓库名与主机名，拼完整引用用
// registry.Reference(host, repository, tag)。同一 commit 的 nvidia/denglin
// 构建产物不同，共享仓库内必须以后缀隔离，否则互相覆盖。
func ReleaseRegistryTag(ref, commit, accelerator string) string {
	body := ReleaseVersionBody(ref, commit)
	if accelerator == "" || accelerator == "auto" {
		return body
	}
	return body + "-" + sanitizeImagePart(accelerator)
}

func sanitizeImagePart(value string) string {
	value = invalidImagePart.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	return strings.Trim(value, ".-_")
}

// streamDockerMessages 消费 dockerd 的 JSON 输出流（构建/推送通用），逐行转写
// 到 Reporter，在 error 字段出现时终止；返回流中出现的 aux digest（构建流为
// 空）。action 用于错误文案（构建/推送）。
func streamDockerMessages(body io.Reader, r Reporter, action string) (string, error) {
	digest := ""
	decoder := json.NewDecoder(body)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Status      string `json:"status"`
			Progress    string `json:"progress"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Aux *struct {
				Digest string `json:"digest"`
			} `json:"aux"`
		}
		if err := decoder.Decode(&msg); err == io.EOF {
			return digest, nil
		} else if err != nil {
			return digest, fmt.Errorf("解析 Docker 输出: %w", err)
		}
		if msg.Aux != nil && msg.Aux.Digest != "" {
			digest = msg.Aux.Digest
			r.Log("digest: " + msg.Aux.Digest)
			continue
		}
		line := strings.TrimSpace(msg.Stream)
		if line == "" {
			line = strings.TrimSpace(strings.TrimSpace(msg.Status) + " " + strings.TrimSpace(msg.Progress))
		}
		if line != "" {
			r.Log(line)
		}
		if msg.Error != "" {
			return digest, fmt.Errorf("Docker %s失败: %s", action, msg.Error)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return digest, fmt.Errorf("Docker %s失败: %s", action, msg.ErrorDetail.Message)
		}
	}
}

// LocalOptions 描述一次面向镜像仓库的本地构建：在管理端本地 Docker 引擎上
// 构建、打上带 registry 主机的完整引用并推送。Accelerator 必须已解析为具体
// 平台（本地引擎不感知中心硬件，不再远端探测）。
type LocalOptions struct {
	ContextDir  string            `json:"-"`
	Dockerfile  string            `json:"dockerfile"`
	RegistryRef string            `json:"registry_ref"`
	NoCache     bool              `json:"no_cache"`
	BuildArgs   map[string]string `json:"build_args"`
	SourceID    string            `json:"source_id"`
	SourceName  string            `json:"source_name"`
	SourceRef   string            `json:"source_ref"`
	Commit      string            `json:"commit"`
	// Accelerator 取 nvidia/denglin/cpu/universal：决定 Dockerfile 变体与
	// 来源标签；universal 表示平台无关构建（RegistryRef 用无后缀通用标签）。
	Accelerator string            `json:"accelerator"`
}

// LocalDocker 返回管理端本地 Docker 引擎客户端：dev 模式（go run）经环境发现
// 本机 Docker Desktop，compose 模式经挂载的 /var/run/docker.sock 使用宿主引擎。
func LocalDocker(ctx context.Context) (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("初始化本地 Docker 客户端: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err = cli.Ping(pingCtx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("本地 Docker 引擎不可用（compose 部署需挂载 /var/run/docker.sock）: %w", err)
	}
	return cli, nil
}

// LocalDockerAvailable 报告本地 Docker 引擎是否可用（构建发布的预检）。
func LocalDockerAvailable() bool {
	cli, err := LocalDocker(context.Background())
	if err != nil {
		return false
	}
	cli.Close()
	return true
}

// BuildLocal 在本地 Docker 引擎构建镜像并打上仓库标签，不推送。
// 多平台原子发布由调用方组合 BuildLocal + PushRef：全部变体构建成功后
// 才开始推送。
func (s *Service) BuildLocal(ctx context.Context, o LocalOptions, r Reporter) error {
	if err := ValidateImageTag(o.RegistryRef); err != nil {
		return err
	}
	base, err := filepath.Abs(o.ContextDir)
	if err != nil {
		return err
	}
	r.Message("正在连接本地 Docker 引擎")
	docker, err := LocalDocker(ctx)
	if err != nil {
		return err
	}
	defer docker.Close()
	dockerfileName, err := resolveDockerfile(base, o.Dockerfile, o.Accelerator)
	if err != nil {
		return err
	}
	dockerfile := filepath.Clean(filepath.Join(base, dockerfileName))
	rel, err := filepath.Rel(base, dockerfile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Dockerfile 必须位于源码目录内")
	}
	r.Message("正在本地构建镜像")
	r.Logf("加速卡类型: %s，使用 %s", AcceleratorName(o.Accelerator), filepath.ToSlash(rel))
	r.Log("开始构建镜像 " + o.RegistryRef)
	stream, err := archive.TarWithOptions(base, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("创建构建上下文: %w", err)
	}
	defer stream.Close()
	args := map[string]*string{}
	for k, v := range o.BuildArgs {
		value := v
		args[k] = &value
	}
	labels := map[string]string{LabelSourceID: o.SourceID, LabelSourceName: o.SourceName, LabelSourceRef: o.SourceRef, LabelCommit: o.Commit, "com.center-manager.accelerator": o.Accelerator}
	resp, err := docker.ImageBuild(ctx, stream, types.ImageBuildOptions{Dockerfile: filepath.ToSlash(rel), Tags: []string{o.RegistryRef}, Remove: true, NoCache: o.NoCache, BuildArgs: args, Labels: labels})
	if err != nil {
		return fmt.Errorf("本地 docker build: %w", err)
	}
	defer resp.Body.Close()
	if _, err = streamDockerMessages(resp.Body, r, "构建"); err != nil {
		return err
	}
	return nil
}

// PushRef 推送本地已有的仓库标签，返回 manifest digest。
func (s *Service) PushRef(ctx context.Context, ref string, r Reporter) (string, error) {
	if err := ValidateImageTag(ref); err != nil {
		return "", err
	}
	docker, err := LocalDocker(ctx)
	if err != nil {
		return "", err
	}
	defer docker.Close()
	push, err := docker.ImagePush(ctx, ref, image.PushOptions{})
	if err != nil {
		return "", fmt.Errorf("推送镜像仓库: %w", err)
	}
	digest, err := streamDockerMessages(push, r, "推送")
	if err != nil {
		return "", err
	}
	if digest == "" {
		// 推送流未携带 aux digest 时回查镜像详情。
		inspect, _, inspectErr := docker.ImageInspectWithRaw(ctx, ref)
		if inspectErr != nil {
			return "", fmt.Errorf("读取推送结果失败: %w", inspectErr)
		}
		for _, value := range inspect.RepoDigests {
			if index := strings.Index(value, "@"); index >= 0 {
				digest = value[index+1:]
				break
			}
		}
	}
	if digest == "" {
		return "", fmt.Errorf("镜像 %s 推送后未获得 digest", ref)
	}
	r.Logf("镜像已推送到仓库: %s（%s）", ref, digest)
	return digest, nil
}

// DetectBuildVariants 返回源码需要构建的平台变体：存在 Dockerfile.nvidia /
// Dockerfile.denglin 时各自成变体（全部构建成功才允许推送，保证版本完整）；
// 只有通用 Dockerfile（或 Dockerfile.cpu）时构建一次（universal），以不带
// 平台后缀的通用标签发布，各类显卡中心都能拉取。
func DetectBuildVariants(dir string) ([]string, error) {
	variants := []string{}
	if existsRegularFile(filepath.Join(dir, "Dockerfile.nvidia")) {
		variants = append(variants, "nvidia")
	}
	if existsRegularFile(filepath.Join(dir, "Dockerfile.denglin")) {
		variants = append(variants, "denglin")
	}
	if len(variants) > 0 {
		return variants, nil
	}
	if existsRegularFile(filepath.Join(dir, "Dockerfile")) || existsRegularFile(filepath.Join(dir, "Dockerfile.cpu")) {
		return []string{"universal"}, nil
	}
	return nil, fmt.Errorf("源码中没有 Dockerfile、Dockerfile.nvidia 或 Dockerfile.denglin")
}

// DetectCenterAccelerator 打开到中心的临时连接探测加速卡类型，供镜像仓库
// 模式在本地构建前解析平台（中心配置为 auto 时使用）。
func (s *Service) DetectCenterAccelerator(ctx context.Context, centerID string) (string, error) {
	cfg, socket, center, err := s.centers.Connection(centerID)
	if err != nil {
		return "", err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return "", err
	}
	defer remote.Close()
	if err = s.centers.RememberHostKey(center, fingerprint); err != nil {
		return "", err
	}
	return ResolveAccelerator(ctx, remote, center)
}

func ResolveAccelerator(ctx context.Context, remote *sshx.DockerClient, center domain.Center) (string, error) {
	if center.Accelerator != "" && center.Accelerator != "auto" {
		return center.Accelerator, nil
	}
	info, infoErr := remote.Client.Info(ctx)
	nvidia, denglin := false, false
	if infoErr == nil {
		for name := range info.Runtimes {
			lower := strings.ToLower(name)
			nvidia = nvidia || strings.Contains(lower, "nvidia")
			denglin = denglin || strings.Contains(lower, "dlrt") || strings.Contains(lower, "denglin")
		}
	}
	if nvidia != denglin {
		if nvidia {
			return "nvidia", nil
		}
		return "denglin", nil
	}
	var output bytes.Buffer
	session, err := remote.SSH.NewSession()
	if err != nil {
		return "", fmt.Errorf("自动识别加速卡失败: %w", err)
	}
	defer session.Close()
	session.Stdout = &output
	probe := `if command -v nvidia-smi >/dev/null 2>&1 || [ -e /dev/nvidiactl ]; then echo nvidia; elif command -v dl-smi >/dev/null 2>&1 || ls /dev/dl* >/dev/null 2>&1 || (command -v lspci >/dev/null 2>&1 && lspci 2>/dev/null | grep -Eiq 'Denglin|Goldwasser'); then echo denglin; else echo unknown; fi`
	if err = session.Start(probe); err != nil {
		return "", fmt.Errorf("自动识别加速卡失败: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case err = <-done:
		if err != nil {
			return "", fmt.Errorf("自动识别加速卡失败: %w", err)
		}
	}
	detected := strings.TrimSpace(output.String())
	if detected == "nvidia" || detected == "denglin" {
		return detected, nil
	}
	if nvidia && denglin {
		return "", fmt.Errorf("检测到多个 GPU 运行时，请在中心配置中明确选择 NVIDIA 或登临")
	}
	return "", fmt.Errorf("无法自动识别中心加速卡，请在中心配置中手动选择 NVIDIA、登临或 CPU")
}

func resolveDockerfile(base, requested, accelerator string) (string, error) {
	if requested != "" && requested != "auto" {
		if existsRegularFile(filepath.Join(base, requested)) {
			return requested, nil
		}
		return "", fmt.Errorf("指定的 Dockerfile 不存在: %s", requested)
	}
	candidates := map[string][]string{
		"nvidia":     {"Dockerfile.nvidia", "Dockerfile"},
		"denglin":    {"Dockerfile.denglin", "Dockerfile"},
		"cpu":        {"Dockerfile", "Dockerfile.cpu"},
		// universal：源码只有通用 Dockerfile，构建一次、三平台标签发布。
		"universal": {"Dockerfile", "Dockerfile.cpu"},
	}[accelerator]
	for _, candidate := range candidates {
		if existsRegularFile(filepath.Join(base, candidate)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("源码中没有适用于 %s 的 Dockerfile", AcceleratorName(accelerator))
}

func existsRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func AcceleratorName(value string) string {
	switch value {
	case "nvidia":
		return "NVIDIA"
	case "denglin":
		return "登临"
	case "cpu":
		return "CPU"
	case "universal":
		return "平台无关"
	default:
		return "自动"
	}
}
