package centeraction

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/sshx"
	dockertypes "github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

const (
	RestartContainer    = "restart_container"
	RestartCompose      = "restart_compose"
	RedeployCompose     = "redeploy_compose"
	StopCompose         = "stop_compose"
	RemoveAllContainers = "remove_all_containers"
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

type Service struct {
	centers CenterResolver
}

func New(centers CenterResolver) *Service { return &Service{centers: centers} }

func (s *Service) Run(ctx context.Context, centerID, action, containerID string, r Reporter) error {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return cancelErr
	}
	defer release()
	if !validAction(action) {
		return fmt.Errorf("不支持的中心操作: %s", action)
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
	r.Progress(10)

	switch action {
	case RestartContainer:
		return restartContainer(ctx, remote, containerID, r)
	case RemoveAllContainers:
		return removeAllContainers(ctx, remote, r)
	default:
		if center.Deployment != nil {
			return fmt.Errorf("模块化中心请在模块列表中选择目标模块操作")
		}
		return runComposeAction(ctx, remote, cfg.Password, center.ComposeFile, action, r)
	}
}

func restartContainer(ctx context.Context, remote *sshx.DockerClient, containerID string, r Reporter) error {
	if strings.TrimSpace(containerID) == "" {
		return fmt.Errorf("缺少容器 ID")
	}
	info, err := remote.Client.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("读取目标容器失败: %w", err)
	}
	name := strings.TrimPrefix(info.Name, "/")
	r.Message("正在重启容器 " + name)
	r.Logf("重启容器：%s（%s）", name, shortID(info.ID))
	timeout := 30
	if err = remote.Client.ContainerRestart(ctx, info.ID, containertypes.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("重启容器 %s 失败: %w", name, err)
	}
	r.Progress(100)
	r.Message("容器重启完成")
	return nil
}

func removeAllContainers(ctx context.Context, remote *sshx.DockerClient, r Reporter) error {
	containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("读取中心全部容器失败: %w", err)
	}
	if len(containers) == 0 {
		r.Log("中心没有可删除的容器")
		r.Progress(100)
		r.Message("中心没有容器")
		return nil
	}
	r.Message(fmt.Sprintf("正在删除全部 %d 个容器", len(containers)))
	r.Logf("将强制删除该中心全部 %d 个 Docker 容器；镜像和数据卷保留", len(containers))
	failed := make([]string, 0)
	for index, item := range containers {
		if err = ctx.Err(); err != nil {
			return err
		}
		name := shortID(item.ID)
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		if err = remote.Client.ContainerRemove(ctx, item.ID, containertypes.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
			failed = append(failed, name+": "+err.Error())
			r.Logf("删除失败：%s", name)
		} else {
			r.Logf("已删除：%s", name)
		}
		r.Progress(10 + float64(index+1)*90/float64(len(containers)))
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d 个容器删除失败: %s", len(failed), strings.Join(failed, "；"))
	}
	r.Message("中心全部容器已删除")
	return nil
}

func runComposeAction(ctx context.Context, remote *sshx.DockerClient, password, composeFile, action string, r Reporter) error {
	if strings.TrimSpace(composeFile) == "" || path.Clean(composeFile) == "." {
		return fmt.Errorf("中心尚未配置 Compose 文件路径")
	}
	command, message := composeCommand(action)
	if command == "" {
		return fmt.Errorf("不支持的 Compose 操作: %s", action)
	}
	r.Message(message)
	r.Logf("Compose 文件：%s", composeFile)
	client := remote.SSH
	checkCommand := "if [ ! -e " + shellQuote(composeFile) + " ]; then printf '%s\\n' " + shellQuote("Compose 文件不存在："+composeFile) + " >&2; exit 10; fi; " +
		"if [ ! -f " + shellQuote(composeFile) + " ]; then printf '%s\\n' " + shellQuote("Compose 路径不是普通文件："+composeFile) + " >&2; exit 11; fi; " +
		"if [ ! -r " + shellQuote(composeFile) + " ]; then printf '%s\\n' " + shellQuote("当前 SSH 用户无权读取 Compose 文件："+composeFile) + " >&2; exit 12; fi"
	if output, err := runSSH(ctx, client, checkCommand); err != nil {
		return composeStepError("Compose 文件检查", output, err)
	}
	composeBase := "docker compose -f " + shellQuote(composeFile)
	r.Progress(20)
	var restartServices []string
	if action == RedeployCompose {
		r.Message("正在校验 Compose 配置")
		configCommand := composeBase + " config"
		r.Log("$ " + configCommand)
		output, err := runSSH(ctx, client, configCommand)
		if err != nil {
			return composeStepError("Compose 配置校验", output, err)
		}
		r.Log("Compose 配置校验通过")
		r.Message("正在检查 Compose 必需的本地镜像")
		if err = validateRequiredLocalImages(ctx, remote, output, r); err != nil {
			return err
		}
		r.Log("本地镜像预检通过，开始更新服务")
	} else {
		ids, output, err := composeContainerIDs(ctx, client, composeBase)
		if err != nil {
			return composeStepError("读取 Compose 容器", output, err)
		}
		if len(ids) == 0 {
			verb := "重启"
			if action == StopCompose {
				verb = "停止"
			}
			return fmt.Errorf("当前 Compose 没有任何容器，无法%s；请先执行“重新部署 Compose”", verb)
		}
		r.Logf("操作前实际发现 %d 个 Compose 容器", len(ids))
		if action == RestartCompose {
			restartServices, err = composeRestartServices(ctx, remote, ids, r)
			if err != nil {
				return err
			}
		}
	}
	if repairsRuntimePaths(action) {
		r.Message("正在校验并修复全部已发布模型的运行时路径")
		modelsRoot := centerpath.ModelsRoot(composeFile)
		r.Logf("模型目录：%s", modelsRoot)
		if repairErr := modelasset.RepairRuntimePathsOnClient(ctx, client, password, modelsRoot, r); repairErr != nil {
			return fmt.Errorf("Compose 操作前模型路径检查失败: %w", repairErr)
		}
	}
	r.Progress(35)
	actionCommand := composeBase + " " + command
	if action == RestartCompose {
		for _, service := range restartServices {
			actionCommand += " " + shellQuote(service)
		}
	}
	r.Log("$ " + actionCommand)
	output, err := runSSHStreaming(ctx, client, actionCommand, r)
	if err != nil {
		return composeStepError(strings.TrimPrefix(message, "正在"), output, err)
	}
	r.Progress(80)
	r.Message("正在核对 Compose 实际运行状态")
	psCommand := composeBase + " ps --all"
	r.Log("$ " + psCommand)
	if psOutput, psErr := runSSHStreaming(ctx, client, psCommand, r); psErr != nil {
		return composeStepError("读取 Compose 实际状态", psOutput, psErr)
	}
	ids, idsOutput, idsErr := composeContainerIDs(ctx, client, composeBase)
	if idsErr != nil {
		return composeStepError("读取 Compose 容器", idsOutput, idsErr)
	}
	if len(ids) == 0 {
		return fmt.Errorf("命令执行后 Compose 仍没有任何容器，服务未启动")
	}
	if err = validateComposeState(ctx, remote, ids, action, r); err != nil {
		return err
	}
	r.Progress(100)
	r.Message(strings.TrimPrefix(message, "正在") + "完成，实际状态已核验")
	return nil
}

func repairsRuntimePaths(action string) bool {
	return action == RedeployCompose || action == RestartCompose
}

type composeImageConfig struct {
	Services map[string]struct {
		Image      string `yaml:"image"`
		PullPolicy string `yaml:"pull_policy"`
	} `yaml:"services"`
}

func requiredLocalImages(config string) (map[string][]string, error) {
	var parsed composeImageConfig
	if err := yaml.Unmarshal([]byte(config), &parsed); err != nil {
		return nil, fmt.Errorf("解析 Compose 镜像配置失败: %w", err)
	}
	result := map[string][]string{}
	for service, item := range parsed.Services {
		if strings.EqualFold(strings.TrimSpace(item.PullPolicy), "never") && strings.TrimSpace(item.Image) != "" {
			result[item.Image] = append(result[item.Image], service)
		}
	}
	for imageName := range result {
		sort.Strings(result[imageName])
	}
	return result, nil
}

func validateRequiredLocalImages(ctx context.Context, remote *sshx.DockerClient, config string, r Reporter) error {
	required, err := requiredLocalImages(config)
	if err != nil {
		return err
	}
	images := make([]string, 0, len(required))
	for imageName := range required {
		images = append(images, imageName)
	}
	sort.Strings(images)
	missing := make([]string, 0)
	for _, imageName := range images {
		_, _, inspectErr := remote.Client.ImageInspectWithRaw(ctx, imageName)
		if inspectErr == nil {
			r.Logf("本地镜像存在：%s（服务：%s）", imageName, strings.Join(required[imageName], ", "))
			continue
		}
		if !errdefs.IsNotFound(inspectErr) {
			return fmt.Errorf("检查本地镜像 %s 失败: %w", imageName, inspectErr)
		}
		missing = append(missing, fmt.Sprintf("%s（服务：%s）", imageName, strings.Join(required[imageName], ", ")))
	}
	if len(missing) > 0 {
		return fmt.Errorf("Compose 本地镜像预检失败，缺少：%s。请先在“软件与版本”配置对应仓库并构建该镜像，或从“基础镜像”导入后再重新部署", strings.Join(missing, "；"))
	}
	r.Logf("本地镜像预检通过：%d 个 pull_policy=never 镜像均已存在", len(images))
	return nil
}

func composeContainerIDs(ctx context.Context, client *ssh.Client, composeBase string) ([]string, string, error) {
	output, err := runSSH(ctx, client, composeBase+" ps --all --quiet")
	if err != nil {
		return nil, output, err
	}
	return strings.Fields(output), output, nil
}

// composeRestartServices excludes successful one-shot jobs from `compose restart`.
// Docker Compose's restart command does not re-evaluate depends_on conditions, so
// restarting an init service together with its dependency can make the init script
// fail while that dependency is still starting.
func composeRestartServices(ctx context.Context, remote *sshx.DockerClient, ids []string, r Reporter) ([]string, error) {
	services := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		info, err := remote.Client.ContainerInspect(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("读取 Compose 容器 %s 失败: %w", shortID(id), err)
		}
		name := strings.TrimPrefix(info.Name, "/")
		service := ""
		if info.Config != nil {
			service = strings.TrimSpace(info.Config.Labels["com.docker.compose.service"])
		}
		if service == "" {
			return nil, fmt.Errorf("Compose 容器 %s 缺少服务标签，无法安全确定重启范围", name)
		}
		if completedComposeJob(info) {
			r.Logf("跳过已成功完成的一次性任务：%s（服务=%s）", name, service)
			continue
		}
		services[service] = struct{}{}
	}
	result := make([]string, 0, len(services))
	for service := range services {
		result = append(result, service)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, fmt.Errorf("Compose 中没有需要重启的运行服务")
	}
	r.Logf("本次将重启 %d 个 Compose 服务；已完成的一次性任务保持退出状态", len(result))
	return result, nil
}

func completedComposeJob(info dockertypes.ContainerJSON) bool {
	return info.State != nil && info.State.Status == "exited" && info.State.ExitCode == 0 &&
		info.HostConfig != nil && info.HostConfig.RestartPolicy.Name == "no"
}

func validateComposeState(ctx context.Context, remote *sshx.DockerClient, ids []string, action string, r Reporter) error {
	failed := make([]string, 0)
	for _, id := range ids {
		info, err := remote.Client.ContainerInspect(ctx, id)
		if err != nil {
			failed = append(failed, shortID(id)+"（读取状态失败："+err.Error()+"）")
			continue
		}
		name := strings.TrimPrefix(info.Name, "/")
		service := info.Config.Labels["com.docker.compose.service"]
		state := "unknown"
		health := ""
		if info.State != nil {
			state = info.State.Status
			if info.State.Health != nil {
				health = info.State.Health.Status
			}
		}
		status := state
		if health != "" {
			status += ", health=" + health
		}
		r.Logf("容器状态：%s | 服务=%s | %s", name, service, status)
		if action == StopCompose {
			if info.State != nil && info.State.Running {
				failed = append(failed, name+"（仍在运行）")
			}
			continue
		}
		completedJob := completedComposeJob(info)
		if info.State == nil || (!info.State.Running && !completedJob) {
			failed = append(failed, name+"（"+status+"）")
		} else if info.State.Health != nil && info.State.Health.Status == "unhealthy" {
			failed = append(failed, name+"（health=unhealthy）")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("Compose 命令已返回，但实际容器状态未达到预期：%s", strings.Join(failed, "；"))
	}
	if action == StopCompose {
		r.Logf("实际状态校验通过：%d 个 Compose 容器均已停止", len(ids))
	} else {
		r.Logf("实际状态校验通过：%d 个 Compose 容器正在运行或已成功完成一次性任务", len(ids))
	}
	return nil
}

func composeStepError(step, output string, err error) error {
	detail := strings.TrimSpace(output)
	if detail == "" {
		return fmt.Errorf("%s失败：远端命令未返回错误详情（%w）", step, err)
	}
	if len(detail) > 4000 {
		detail = detail[len(detail)-4000:]
	}
	return fmt.Errorf("%s失败：%s（%w）", step, detail, err)
}

func composeCommand(action string) (string, string) {
	switch action {
	case RestartCompose:
		return "restart", "正在重启整套 Compose"
	case RedeployCompose:
		return "up -d", "正在重新部署整套 Compose"
	case StopCompose:
		return "stop", "正在停止整套 Compose"
	default:
		return "", ""
	}
}

// CommandPreview returns the exact center command shown in the live operation dialog.
func CommandPreview(composeFile, action, containerID string) string {
	if command, _ := composeCommand(action); command != "" {
		return "docker compose -f " + shellQuote(composeFile) + " " + command
	}
	if action == RestartContainer {
		return "docker restart " + shellQuote(containerID)
	}
	if action == RemoveAllContainers {
		return "Docker Engine API：强制删除中心全部容器（保留镜像和数据卷）"
	}
	return ""
}

func validAction(action string) bool {
	return action == RestartContainer || action == RestartCompose || action == RedeployCompose || action == StopCompose || action == RemoveAllContainers
}

func runSSH(ctx context.Context, client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if err = session.Start(command); err != nil {
		return combineSSHOutput(stdout.String(), stderr.String()), err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return combineSSHOutput(stdout.String(), stderr.String()), ctx.Err()
	case err = <-done:
		return combineSSHOutput(stdout.String(), stderr.String()), err
	}
}

type liveOutputWriter struct {
	mu       sync.Mutex
	all      strings.Builder
	pending  string
	reporter Reporter
	prefix   string
}

func (w *liveOutputWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.all.Write(data)
	w.pending += string(data)
	lines := make([]string, 0)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		lines = append(lines, strings.TrimSuffix(w.pending[:index], "\r"))
		w.pending = w.pending[index+1:]
	}
	w.mu.Unlock()
	for _, line := range lines {
		if line != "" {
			w.reporter.Log(w.prefix + line)
		}
	}
	return len(data), nil
}

func (w *liveOutputWriter) flush() {
	w.mu.Lock()
	line := strings.TrimRight(w.pending, "\r\n")
	w.pending = ""
	w.mu.Unlock()
	if line != "" {
		w.reporter.Log(w.prefix + line)
	}
}

func (w *liveOutputWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.all.String()
}

func runSSHStreaming(ctx context.Context, client *ssh.Client, command string, r Reporter) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	stdout := &liveOutputWriter{reporter: r}
	stderr := &liveOutputWriter{reporter: r}
	session.Stdout, session.Stderr = stdout, stderr
	if err = session.Start(command); err != nil {
		stdout.flush()
		stderr.flush()
		return combineSSHOutput(stdout.String(), stderr.String()), err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		err = ctx.Err()
	case err = <-done:
	}
	stdout.flush()
	stderr.flush()
	return combineSSHOutput(stdout.String(), stderr.String()), err
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

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func shortID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
