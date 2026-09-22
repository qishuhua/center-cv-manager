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

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	"github.com/docker/docker/api/types"
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

type Options struct {
	CenterID   string            `json:"center_id"`
	ContextDir string            `json:"-"`
	Dockerfile string            `json:"dockerfile"`
	ImageTag   string            `json:"image_tag"`
	NoCache    bool              `json:"no_cache"`
	BuildArgs  map[string]string `json:"build_args"`
	SourceID   string            `json:"source_id"`
	SourceName string            `json:"source_name"`
	SourceRef  string            `json:"source_ref"`
	Commit     string            `json:"commit"`
}

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

func sanitizeImagePart(value string) string {
	value = invalidImagePart.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	return strings.Trim(value, ".-_")
}

func (s *Service) Run(ctx context.Context, o Options, r Reporter) error {
	if err := ValidateImageTag(o.ImageTag); err != nil {
		return err
	}
	base, err := filepath.Abs(o.ContextDir)
	if err != nil {
		return err
	}
	cfg, socket, center, err := s.centers.Connection(o.CenterID)
	if err != nil {
		return err
	}
	r.Message("正在连接中心服务器")
	r.Logf("连接中心 %s (%s:%d)", center.Name, center.Host, center.Port)
	remote, fp, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	if err = s.centers.RememberHostKey(center, fp); err != nil {
		return err
	}
	accelerator, err := ResolveAccelerator(ctx, remote, center)
	if err != nil {
		return err
	}
	dockerfileName, err := resolveDockerfile(base, o.Dockerfile, accelerator)
	if err != nil {
		return err
	}
	dockerfile := filepath.Clean(filepath.Join(base, dockerfileName))
	rel, err := filepath.Rel(base, dockerfile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Dockerfile 必须位于源码目录内")
	}
	r.Progress(10)
	r.Message("正在流式传输源码并构建")
	r.Log("SSH 主机指纹: " + fp)
	r.Logf("加速卡类型: %s，使用 %s", AcceleratorName(accelerator), filepath.ToSlash(rel))
	r.Log("开始构建镜像 " + o.ImageTag)
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
	labels := map[string]string{LabelSourceID: o.SourceID, LabelSourceName: o.SourceName, LabelSourceRef: o.SourceRef, LabelCommit: o.Commit, "com.center-manager.accelerator": accelerator}
	resp, err := remote.Client.ImageBuild(ctx, stream, types.ImageBuildOptions{Dockerfile: filepath.ToSlash(rel), Tags: []string{o.ImageTag}, Remove: true, NoCache: o.NoCache, BuildArgs: args, Labels: labels})
	if err != nil {
		return fmt.Errorf("远端 docker build: %w", err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Status      string `json:"status"`
			Progress    string `json:"progress"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err = decoder.Decode(&msg); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("解析 Docker 输出: %w", err)
		}
		line := strings.TrimSpace(msg.Stream)
		if line == "" {
			line = strings.TrimSpace(strings.TrimSpace(msg.Status) + " " + strings.TrimSpace(msg.Progress))
		}
		if line != "" {
			r.Log(line)
		}
		if msg.Error != "" {
			return fmt.Errorf("Docker 构建失败: %s", msg.Error)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return fmt.Errorf("Docker 构建失败: %s", msg.ErrorDetail.Message)
		}
	}
	r.Progress(100)
	r.Message("构建完成")
	r.Log("镜像构建完成: " + o.ImageTag)
	return nil
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
		"nvidia":  {"Dockerfile.nvidia", "Dockerfile"},
		"denglin": {"Dockerfile.denglin", "Dockerfile"},
		"cpu":     {"Dockerfile", "Dockerfile.cpu"},
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
	default:
		return "自动"
	}
}
