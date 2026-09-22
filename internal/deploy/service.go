package deploy

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
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
	UpdateRuntime(string, string, int, int, string, string) error
}

type Item struct {
	SourceID      string `json:"source_id"`
	SourceName    string `json:"source_name"`
	Ref           string `json:"ref"`
	Commit        string `json:"commit"`
	ExpectedImage string `json:"expected_image"`
}

type CheckResult struct {
	Ready           bool                       `json:"ready"`
	Error           string                     `json:"error,omitempty"`
	Images          map[string]string          `json:"images"`
	ServiceMappings map[string][]string        `json:"service_mappings"`
	MissingImages   []string                   `json:"missing_images"`
	MissingServices []string                   `json:"missing_services"`
	BaseServices    []string                   `json:"-"`
	CurrentServices map[string]deployedService `json:"-"`
	ComposeConfig   string                     `json:"-"`
}

type Service struct {
	centers CenterResolver
}

func New(centers CenterResolver) *Service {
	return &Service{centers: centers}
}

func (s *Service) Check(ctx context.Context, centerID string, items []Item) CheckResult {
	result := CheckResult{Images: map[string]string{}, ServiceMappings: map[string][]string{}, MissingImages: []string{}, MissingServices: []string{}, CurrentServices: map[string]deployedService{}}
	cfg, socket, center, err := s.centers.Connection(centerID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if center.ComposeFile == "" {
		result.Error = "中心尚未配置 Compose 文件路径"
		return result
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(center, fingerprint)
	images, err := remote.Client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		result.Error = "读取中心镜像失败: " + err.Error()
		return result
	}
	for _, item := range items {
		if tag := findImage(images, item); tag != "" {
			result.Images[item.SourceID] = tag
		} else {
			result.MissingImages = append(result.MissingImages, item.SourceName+":"+item.Ref)
		}
	}
	if _, err = runSSH(ctx, remote.SSH, "test -f "+shellQuote(center.ComposeFile)+" -a -r "+shellQuote(center.ComposeFile), ""); err != nil {
		result.Error = "Compose 文件不存在或不可读: " + center.ComposeFile
		return result
	}
	composeConfig, err := runSSH(ctx, remote.SSH, "docker compose -f "+shellQuote(center.ComposeFile)+" config", "")
	if err != nil {
		result.Error = "Compose 预检失败: " + err.Error()
		return result
	}
	result.ComposeConfig = composeConfig
	serviceImages, err := parseDeploymentServices(composeConfig)
	if err != nil {
		result.Error = "解析 Compose 配置失败: " + err.Error()
		return result
	}
	for serviceName := range serviceImages {
		result.BaseServices = append(result.BaseServices, serviceName)
	}
	sort.Strings(result.BaseServices)
	baseServiceSet := make(map[string]bool, len(result.BaseServices))
	for _, serviceName := range result.BaseServices {
		baseServiceSet[serviceName] = true
	}
	if containers, listErr := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true}); listErr == nil {
		running := map[string]bool{}
		for _, current := range containers {
			serviceName := current.Labels["com.docker.compose.service"]
			sourceID := current.Labels["com.center-manager.source.id"]
			sourceRef := current.Labels["com.center-manager.source.ref"]
			if !baseServiceSet[serviceName] || current.Image == "" || sourceID == "" || sourceRef == "" {
				continue
			}
			isRunning := current.State == "running"
			if _, exists := result.CurrentServices[serviceName]; exists && (running[serviceName] || !isRunning) {
				continue
			}
			result.CurrentServices[serviceName] = deployedService{Image: current.Image, Labels: map[string]string{
				"com.center-manager.source.id": sourceID, "com.center-manager.source.ref": sourceRef,
			}}
			running[serviceName] = isRunning
		}
	}
	for _, item := range items {
		reference := item.ExpectedImage
		repository := imageRepository(reference)
		matched := []string{}
		for serviceName, serviceImage := range serviceImages {
			if serviceImage == repository {
				matched = append(matched, serviceName)
			}
		}
		if len(matched) == 0 {
			result.MissingServices = append(result.MissingServices, fmt.Sprintf("%s（Compose 未配置镜像仓库 %s 的服务映射；请更新中心 Compose 模板）", item.SourceName, repository))
			continue
		}
		sort.Strings(matched)
		result.ServiceMappings[item.SourceID] = matched
	}
	serviceOwner := map[string]string{}
	for _, item := range items {
		for _, serviceName := range result.ServiceMappings[item.SourceID] {
			if owner, exists := serviceOwner[serviceName]; exists && owner != item.SourceName {
				result.Error = fmt.Sprintf("Compose service %s 同时映射到 %s 和 %s", serviceName, owner, item.SourceName)
				return result
			}
			serviceOwner[serviceName] = item.SourceName
		}
	}
	result.MissingImages = uniqueSorted(result.MissingImages)
	result.MissingServices = uniqueSorted(result.MissingServices)
	result.Ready = result.Error == "" && len(result.MissingImages) == 0 && len(result.MissingServices) == 0
	return result
}

func (s *Service) Run(ctx context.Context, centerID string, items []Item, r Reporter) error {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return cancelErr
	}
	defer release()
	r.Message("正在检查中心部署条件")
	r.Progress(5)
	check := s.Check(ctx, centerID, items)
	if !check.Ready {
		return fmt.Errorf("中心未满足部署条件: %s", checkSummary(check))
	}
	cfg, socket, center, err := s.centers.Connection(centerID)
	if err != nil {
		return err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(center, fingerprint)

	temporaryCompose := fmt.Sprintf("%s.tmp.%d", center.ComposeFile, time.Now().UnixNano())
	content, err := renderComposeRelease(check.ComposeConfig, check.CurrentServices, items, check.Images, check.ServiceMappings)
	if err != nil {
		return fmt.Errorf("生成 Compose 发布配置失败: %w", err)
	}
	r.Progress(25)
	r.Message("正在写入 Compose 镜像版本")
	r.Logf("Compose 文件: %s", center.ComposeFile)
	uploadCommand := "umask 022; cat > " + shellQuote(temporaryCompose)
	if output, commandErr := runSSH(ctx, remote.SSH, uploadCommand, content); commandErr != nil {
		return fmt.Errorf("写入临时 Compose 失败: %s %w", output, commandErr)
	}
	r.Progress(45)
	r.Message("正在校验 Compose 配置")
	temporaryComposeArgs := "docker compose -f " + shellQuote(temporaryCompose)
	if output, commandErr := runSSH(ctx, remote.SSH, temporaryComposeArgs+" config >/dev/null", ""); commandErr != nil {
		_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(temporaryCompose), "")
		return fmt.Errorf("Compose 配置校验失败: %s %w", output, commandErr)
	}
	if output, commandErr := runSSH(ctx, remote.SSH, "mv -f "+shellQuote(temporaryCompose)+" "+shellQuote(center.ComposeFile), ""); commandErr != nil {
		return fmt.Errorf("启用 Compose 发布配置失败: %s %w", output, commandErr)
	}
	accelerator := center.DetectedAccelerator
	if accelerator == "" {
		accelerator = center.Accelerator
	}
	if err = s.centers.UpdateRuntime(centerID, accelerator, center.CPUCount, center.GPUCount, center.ComposeFile, content); err != nil {
		return fmt.Errorf("保存中心 Compose 状态失败: %w", err)
	}
	r.Progress(60)
	r.Message("正在启动整套服务")
	composeArgs := "docker compose -f " + shellQuote(center.ComposeFile)
	output, commandErr := runSSH(ctx, remote.SSH, composeArgs+" up -d --remove-orphans", "")
	if strings.TrimSpace(output) != "" {
		r.Log(strings.TrimSpace(output))
	}
	if commandErr != nil {
		return fmt.Errorf("启动 Compose 服务失败: %w", commandErr)
	}
	r.Progress(100)
	r.Message("整套服务部署完成")
	return nil
}

func findImage(images []image.Summary, item Item) string {
	// Prefer the immutable tag selected by the release. Requiring its build
	// labels prevents an unrelated or stale image with the same tag from being
	// accepted silently.
	for _, summary := range images {
		if !matchesSourceSnapshot(summary, item) {
			continue
		}
		for _, tag := range summary.RepoTags {
			if tag == item.ExpectedImage {
				return tag
			}
		}
	}
	for _, summary := range images {
		if matchesSourceSnapshot(summary, item) {
			if tag := firstUsableTag(summary.RepoTags); tag != "" {
				return tag
			}
		}
	}
	// Backward compatibility is only allowed for legacy deployment requests
	// which did not lock a commit. New releases always carry Commit.
	if item.Commit != "" {
		return ""
	}
	for _, summary := range images {
		for _, tag := range summary.RepoTags {
			if tag == item.ExpectedImage {
				return tag
			}
		}
	}
	return ""
}

func matchesSourceSnapshot(summary image.Summary, item Item) bool {
	if summary.Labels[build.LabelSourceID] != item.SourceID || summary.Labels[build.LabelSourceRef] != item.Ref {
		return false
	}
	return item.Commit == "" || summary.Labels[build.LabelCommit] == item.Commit
}

func firstUsableTag(tags []string) string {
	for _, tag := range tags {
		if tag != "" && tag != "<none>:<none>" {
			return tag
		}
	}
	return ""
}

type deployedService struct {
	Image  string
	Labels map[string]string
}

func renderComposeRelease(base string, current map[string]deployedService, items []Item, images map[string]string, serviceMappings map[string][]string) (string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(base), &document); err != nil {
		return "", err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("Compose 根节点必须是映射")
	}
	services := yamlMappingValue(document.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("Compose services 无效")
	}
	updates := make(map[string]deployedService, len(current)+len(items))
	for name, service := range current {
		updates[name] = service
	}
	for _, item := range items {
		imageName := images[item.SourceID]
		if imageName == "" {
			return "", fmt.Errorf("软件 %s 缺少已构建镜像", item.SourceName)
		}
		for _, serviceName := range serviceMappings[item.SourceID] {
			updates[serviceName] = deployedService{Image: imageName, Labels: map[string]string{
				"com.center-manager.source.id": item.SourceID, "com.center-manager.source.ref": item.Ref,
			}}
		}
	}
	for serviceName, update := range updates {
		service := yamlMappingValue(services, serviceName)
		if service == nil {
			// A running container from an obsolete service is deliberately ignored.
			continue
		}
		if service.Kind != yaml.MappingNode || update.Image == "" {
			return "", fmt.Errorf("Compose service %s 无效", serviceName)
		}
		yamlSetScalar(service, "image", update.Image)
		labels := yamlMappingValue(service, "labels")
		if labels == nil {
			labels = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			service.Content = append(service.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"}, labels)
		}
		if labels.Kind != yaml.MappingNode {
			return "", fmt.Errorf("Compose service %s 的 labels 必须是映射", serviceName)
		}
		for key, value := range update.Labels {
			yamlSetScalar(labels, key, value)
		}
	}
	output, err := yaml.Marshal(&document)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func yamlSetScalar(mapping *yaml.Node, key, value string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

// composeConfigServices 对应 docker compose config 输出中的 services 部分，
// 只关心每个 service 解析后的镜像引用。
type composeConfigServices struct {
	Services map[string]struct {
		Image string `yaml:"image"`
	} `yaml:"services"`
}

// parseComposeServices 解析 docker compose config 的 YAML 输出，
// 返回 service 名到镜像引用的映射；没有 image 的 service（纯 build）跳过。
func parseComposeServices(output string) (map[string]string, error) {
	var parsed composeConfigServices
	if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, fmt.Errorf("输出不是有效的 Compose 配置: %w", err)
	}
	services := map[string]string{}
	for name, service := range parsed.Services {
		if service.Image != "" {
			services[name] = service.Image
		}
	}
	return services, nil
}

// parseDeploymentServices 使用模板的显式仓库映射，旧配置仅接受精确仓库名。
func parseDeploymentServices(output string) (map[string]string, error) {
	var parsed struct {
		Services map[string]struct {
			Image  string            `yaml:"image"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for name, service := range parsed.Services {
		repository := service.Labels["com.center-manager.image.repository"]
		if repository == "" {
			repository = imageRepository(service.Image)
		}
		if repository != "" {
			result[name] = repository
		}
	}
	return result, nil
}

// imageRepository 提取镜像引用中的仓库部分，去掉 tag 和 digest。
func imageRepository(reference string) string {
	if i := strings.Index(reference, "@"); i >= 0 {
		reference = reference[:i]
	}
	if i := strings.LastIndex(reference, ":"); i >= 0 && !strings.Contains(reference[i:], "/") {
		reference = reference[:i]
	}
	return reference
}

func runSSH(ctx context.Context, client *ssh.Client, command, stdin string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}
	if err = session.Start(command); err != nil {
		return combineSSHOutput(stdout.String(), stderr.String()), err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		<-done
		return combineSSHOutput(stdout.String(), stderr.String()), ctx.Err()
	case err = <-done:
		output := combineSSHOutput(stdout.String(), stderr.String())
		if err != nil {
			return output, fmt.Errorf("%s: %w", strings.TrimSpace(output), err)
		}
		return output, nil
	}
}

func combineSSHOutput(stdout, stderr string) string {
	if stdout == "" {
		return stderr
	}
	if stderr == "" {
		return stdout
	}
	return strings.TrimRight(stdout, "\r\n") + "\n" + stderr
}

func checkSummary(result CheckResult) string {
	parts := []string{}
	if result.Error != "" {
		parts = append(parts, result.Error)
	}
	if len(result.MissingImages) > 0 {
		parts = append(parts, "缺少镜像 "+strings.Join(result.MissingImages, "、"))
	}
	if len(result.MissingServices) > 0 {
		parts = append(parts, strings.Join(result.MissingServices, "、"))
	}
	return strings.Join(parts, "；")
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
