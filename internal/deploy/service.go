package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
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
	Modules         map[string]CheckResult     `json:"modules,omitempty"`
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
	if center.Deployment != nil {
		return s.checkModules(ctx, centerID, center, items)
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
	composeConfig, err := runSSH(ctx, remote.SSH, modular.CenterCommand(center)+" config", "")
	if err != nil {
		result.Error = "Compose 预检失败: " + err.Error()
		return result
	}
	result.ComposeConfig, err = runSSH(ctx, remote.SSH, "cat "+shellQuote(center.ComposeFile), "")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	serviceImages, err := parseDeploymentServices(composeConfig)
	if err != nil {
		result.Error = "解析 Compose 配置失败: " + err.Error()
		return result
	}
	// 模块化软件发布按 BaseServices 整体 up：未被本次发布分发覆盖的
	// pull_policy:never 镜像必须已在中心上，否则 up 会在中途失败并
	// 删掉旧容器（例如启用错分检测但错分审核镜像尚未分发）。
	if checkErr := requireLocalImages(ctx, remote, composeConfig, items); checkErr != nil {
		result.Error = checkErr.Error()
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
			if center.ComposeProject != "" && current.Labels["com.docker.compose.project"] != center.ComposeProject {
				continue
			}
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

// Run 应用本次发布并返回实际重建的服务名。legacy 中心只重建映射到所选
// 软件版本的服务（中间件、网关、监控既不修改也不重启）；模块化中心返回
// 实际执行发布的各模块的全部服务。
func (s *Service) Run(ctx context.Context, centerID string, items []Item, r Reporter) ([]string, error) {
	var restarted []string
	err := centerlock.Within(ctx, centerID, func(ctx context.Context) error {
		services, runErr := s.runLocked(ctx, centerID, items, r)
		restarted = services
		return runErr
	})
	if err != nil {
		return nil, err
	}
	return restarted, nil
}
func (s *Service) runLocked(ctx context.Context, centerID string, items []Item, r Reporter) ([]string, error) {
	_, _, currentCenter, resolveErr := s.centers.Connection(centerID)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if currentCenter.Deployment != nil {
		return s.runModules(ctx, centerID, currentCenter, items, r)
	}
	r.Message("正在检查中心部署条件")
	r.Progress(5)
	check := s.Check(ctx, centerID, items)
	if !check.Ready {
		return nil, fmt.Errorf("中心未满足部署条件: %s", checkSummary(check))
	}
	cfg, socket, center, err := s.centers.Connection(centerID)
	if err != nil {
		return nil, err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(center, fingerprint)

	temporaryCompose := fmt.Sprintf("%s.tmp.%d", center.ComposeFile, time.Now().UnixNano())
	content, err := renderComposeRelease(check.ComposeConfig, nil, items, check.Images, check.ServiceMappings)
	if err != nil {
		return nil, fmt.Errorf("生成 Compose 发布配置失败: %w", err)
	}
	r.Progress(25)
	r.Message("正在写入 Compose 镜像版本")
	r.Logf("Compose 文件: %s", center.ComposeFile)
	uploadCommand := "umask 022; cat > " + shellQuote(temporaryCompose)
	if output, commandErr := runSSH(ctx, remote.SSH, uploadCommand, content); commandErr != nil {
		return nil, fmt.Errorf("写入临时 Compose 失败: %s %w", output, commandErr)
	}
	r.Progress(45)
	r.Message("正在校验 Compose 配置")
	candidate := center
	candidate.ComposeFile = temporaryCompose
	temporaryComposeArgs := modular.CenterCommand(candidate)
	if output, commandErr := runSSH(ctx, remote.SSH, temporaryComposeArgs+" config >/dev/null", ""); commandErr != nil {
		_, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(temporaryCompose), "")
		return nil, fmt.Errorf("Compose 配置校验失败: %s %w", output, commandErr)
	}
	original, readErr := runSSH(ctx, remote.SSH, "cat "+shellQuote(center.ComposeFile), "")
	if readErr != nil {
		return nil, readErr
	}
	if modular.Hash(original) != modular.Hash(check.ComposeConfig) {
		return nil, fmt.Errorf("远端 Compose 在发布期间已修改，未覆盖配置")
	}
	if output, commandErr := runSSH(ctx, remote.SSH, "cp -p "+shellQuote(center.ComposeFile)+" "+shellQuote(center.ComposeFile+".bak."+fmt.Sprint(time.Now().UnixNano()))+" && mv -f "+shellQuote(temporaryCompose)+" "+shellQuote(center.ComposeFile), ""); commandErr != nil {
		return nil, fmt.Errorf("启用 Compose 发布配置失败: %s %w", output, commandErr)
	}
	accelerator := center.DetectedAccelerator
	if accelerator == "" {
		accelerator = center.Accelerator
	}
	if err = s.centers.UpdateRuntime(centerID, accelerator, center.CPUCount, center.GPUCount, center.ComposeFile, content); err != nil {
		return nil, fmt.Errorf("保存中心 Compose 状态失败: %w", err)
	}
	r.Progress(60)
	r.Message("正在更新目标服务")
	targets := releaseTargets(check, items)
	if center.ComposeProject != "" {
		// 模块化路径（moduleResolver 注入项目名）：整体发布该模块的服务。
		targets = uniqueSorted(check.BaseServices)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("未选择任何目标服务")
	}
	// --force-recreate 会连带重建依赖服务，必须配合 --no-deps 把重启范围
	// 限定在本次映射到的业务服务，中间件、网关、监控保持不动。
	composeArgs := modular.CenterCommand(center) + " up -d --force-recreate --no-deps"
	for _, name := range targets {
		composeArgs += " " + shellQuote(name)
	}
	output, commandErr := runSSH(ctx, remote.SSH, composeArgs, "")
	if strings.TrimSpace(output) != "" {
		r.Log(strings.TrimSpace(output))
	}
	if commandErr != nil {
		return nil, fmt.Errorf("启动 Compose 服务失败: %w", commandErr)
	}
	r.Progress(100)
	r.Message("目标服务部署完成")
	return targets, nil
}

// releaseTargets 汇总本次发布要重建的服务：仅映射到所选软件版本的服务。
func releaseTargets(check CheckResult, items []Item) []string {
	targets := []string{}
	for _, item := range items {
		targets = append(targets, check.ServiceMappings[item.SourceID]...)
	}
	return uniqueSorted(targets)
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
		// Decode the service to include labels inherited through YAML merge
		// anchors. Materialize only this service's labels, never modify the anchor.
		var effective struct {
			Labels any `yaml:"labels"`
		}
		if err := service.Decode(&effective); err != nil {
			return "", err
		}
		values := map[string]string{}
		switch raw := effective.Labels.(type) {
		case map[string]any:
			for k, v := range raw {
				values[k] = fmt.Sprint(v)
			}
		case []any:
			for _, v := range raw {
				parts := strings.SplitN(fmt.Sprint(v), "=", 2)
				value := ""
				if len(parts) == 2 {
					value = parts[1]
				}
				values[parts[0]] = value
			}
		case nil:
		default:
			return "", fmt.Errorf("Compose service %s 的 labels 无效", serviceName)
		}
		labels := &yaml.Node{}
		if err := labels.Encode(values); err != nil {
			return "", err
		}
		existing := yamlMappingValue(service, "labels")
		if existing != nil {
			*existing = *labels
			labels = existing
		} else {
			service.Content = append(service.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"}, labels)
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

// requireLocalImages 校验整体 up 会触碰的全部服务：非本次发布分发（按
// com.center-manager.image.repository 或镜像名匹配发布项）的 pull_policy:never
// 镜像必须已在中心上，否则 up 会在删除旧容器后因缺镜像中途失败。
func requireLocalImages(ctx context.Context, remote *sshx.DockerClient, composeConfig string, items []Item) error {
	var doc struct {
		Services map[string]struct {
			Image      string            `yaml:"image"`
			PullPolicy string            `yaml:"pull_policy"`
			Labels     map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeConfig), &doc); err != nil {
		return err
	}
	releasing := map[string]bool{}
	for _, item := range items {
		releasing[item.SourceName] = true
	}
	names := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := doc.Services[name]
		if !strings.EqualFold(strings.TrimSpace(svc.PullPolicy), "never") || strings.TrimSpace(svc.Image) == "" {
			continue
		}
		repository := svc.Labels["com.center-manager.image.repository"]
		if repository == "" {
			repository = imageRepository(svc.Image)
		}
		if releasing[repository] {
			continue
		}
		if _, _, err := remote.Client.ImageInspectWithRaw(ctx, svc.Image); err != nil {
			return fmt.Errorf("服务 %s 需要本地镜像 %s，但中心上不存在；请先发布该服务的业务版本，或预置镜像后再发布", name, svc.Image)
		}
	}
	return nil
}

func runSSH(ctx context.Context, client *ssh.Client, command, stdin string) (string, error) {
	return modular.Run(ctx, client, command, stdin)
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
