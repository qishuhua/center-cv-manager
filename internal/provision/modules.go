package provision

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modelasset"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/templates"
)

// deploymentWorkers 返回 business 模块应登记的 Worker 实例数：单实例模式
// 只登记一个，其余按（物理显卡）数量。
func deploymentWorkers(c domain.Center, gpuTotal int) int {
	if c.SingleInstanceEnabled {
		return 1
	}
	return gpuTotal
}

func (s *Service) prepareModules(ctx context.Context, remote *sshx.DockerClient, c domain.Center, composeFile, accelerator string, cpus, gpus int, opts templates.RenderOptions, r Reporter) error {
	d := c.Deployment
	if d.Status == "reset" {
		fresh := modular.NewDeployment(composeFile, deploymentWorkers(c, opts.Workers))
		fresh.ModelsRoot, fresh.LogsRoot, fresh.MediaRoot = d.ModelsRoot, d.LogsRoot, d.MediaRoot
		fresh.Status = "configured"
		fresh.ReselectionRequired = d.ReselectionRequired
		d = fresh
	}
	if d.Status == "preparing" && allPending(d.Modules) {
		d = modular.NewDeployment(composeFile, deploymentWorkers(c, opts.Workers))
	}
	files := map[string]string{}
	var err error
	if d.Status == "preparing" || d.Status == "configured" {
		files, err = templates.RenderModules(accelerator, opts)
	}
	if err != nil {
		return err
	}
	// 渲染的 Worker 数（单实例=1）必须与登记表一致，否则 ValidateConfig 以
	// 「登记的服务不存在」拒绝；无论是否重建 Deployment 都统一对齐，顺带
	// 自愈旧登记表与实际 Compose 不一致的记录。
	modular.SetBusinessWorkers(d, deploymentWorkers(c, opts.Workers))
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		target := path.Join(d.Root, rel)
		// Preserve every existing center file, including manually edited configs.
		state, err := runSSH(ctx, remote.SSH, "if [ -e "+shellQuote(target)+" ]; then printf exists; else printf missing; fi", "")
		if err != nil {
			return err
		}
		if strings.TrimSpace(state) == "exists" {
			continue
		}
		mode := "0644"
		if strings.HasSuffix(rel, ".sh") {
			mode = "0755"
		}
		if strings.HasSuffix(rel, "module.env") || strings.Contains(rel, "secrets/") {
			mode = "0600"
		}
		// Alertmanager runs as 65534 and needs to read its bind-mounted URL file.
		if err = uploadRemoteFile(ctx, remote.SSH, target, files[rel], mode); err != nil {
			return err
		}
		r.Logf("已准备模块文件：%s", rel)
	}
	for i := range d.Modules {
		m := &d.Modules[i]
		if !m.Enabled {
			continue
		}
		content, err := readRemoteFile(ctx, remote.SSH, m.ComposeFile)
		if err != nil {
			return err
		}
		if err = modular.ValidateConfig(*m, content); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if _, err = runSSH(ctx, remote.SSH, modular.Command(*m)+" config --quiet", ""); err != nil {
			return fmt.Errorf("模块 %s 配置校验失败: %w", m.Name, err)
		}
		m.Revision = modular.Hash(content)
		if m.Status == "pending" {
			m.Status = "prepared"
		}
		m.UpdatedAt = time.Now()
	}
	if err = s.centers.UpdateRuntime(c.ID, accelerator, cpus, gpus, composeFile, ""); err != nil {
		return err
	}
	if d.Status == "preparing" || d.Status == "configured" {
		d.Status = "prepared"
	}
	if err = s.centers.UpdateDeployment(c.ID, d); err != nil {
		return err
	}
	r.Message("五类模块配置已准备；已有配置保持不变")
	return nil
}

func allPending(ms []domain.ComposeModule) bool {
	for _, m := range ms {
		if m.Status != "pending" {
			return false
		}
	}
	return true
}

type ModuleDocument struct {
	Module   domain.ComposeModule `json:"module"`
	Path     string               `json:"path"`
	Content  string               `json:"content"`
	Revision string               `json:"revision"`
	Drift    bool                 `json:"drift"`
}

func (s *Service) ReadModule(ctx context.Context, centerID, moduleID, file string) (ModuleDocument, error) {
	remote, c, err := s.connect(centerID)
	if err != nil {
		return ModuleDocument{}, err
	}
	defer remote.Close()
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return ModuleDocument{}, err
	}
	p, err := moduleFile(m, file)
	if err != nil {
		return ModuleDocument{}, err
	}
	if err = guardModuleFile(ctx, remote, c, m, p); err != nil {
		return ModuleDocument{}, err
	}
	content, err := readRemoteFile(ctx, remote.SSH, p)
	if err != nil {
		return ModuleDocument{}, err
	}
	hash := modular.Hash(content)
	return ModuleDocument{Module: m, Path: p, Content: content, Revision: hash, Drift: file == "compose.yml" && m.Revision != "" && m.Revision != hash}, nil
}

// Restrict file editing to an explicitly named regular file inside the module.
func moduleFile(m domain.ComposeModule, file string) (string, error) {
	if file == "" {
		file = "compose.yml"
	}
	if path.IsAbs(file) || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") || strings.ContainsAny(file, "\x00\r\n\\") || strings.HasPrefix(file, ".history/") {
		return "", fmt.Errorf("无效的模块内文件路径")
	}
	return path.Join(path.Dir(m.ComposeFile), file), nil
}

func (s *Service) SaveModule(ctx context.Context, centerID, moduleID, file, content, revision string) (ModuleDocument, error) {
	unlock, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return ModuleDocument{}, err
	}
	defer unlock()
	if len(content) > 2<<20 {
		return ModuleDocument{}, fmt.Errorf("文件超过 2 MiB")
	}
	remote, c, err := s.connect(centerID)
	if err != nil {
		return ModuleDocument{}, err
	}
	defer remote.Close()
	if c.Deployment == nil {
		return ModuleDocument{}, fmt.Errorf("旧中心请使用原 Compose 编辑器")
	}
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return ModuleDocument{}, err
	}
	target, err := moduleFile(m, file)
	if err != nil {
		return ModuleDocument{}, err
	}
	if err = guardModuleFile(ctx, remote, c, m, target); err != nil {
		return ModuleDocument{}, err
	}
	old, readErr := readRemoteFile(ctx, remote.SSH, target)
	if readErr != nil {
		state, e := runSSH(ctx, remote.SSH, "test ! -e "+shellQuote(target), "")
		_ = state
		if e != nil {
			return ModuleDocument{}, fmt.Errorf("无法读取原文件，停止覆盖")
		}
		if revision != "" {
			return ModuleDocument{}, fmt.Errorf("文件已删除，请重新读取")
		}
	} else if modular.Hash(old) != revision {
		return ModuleDocument{}, fmt.Errorf("配置已变更，请重新读取后保存")
	}
	isCompose := target == m.ComposeFile
	if isCompose {
		if err = modular.ValidateConfig(m, content); err != nil {
			return ModuleDocument{}, err
		}
	}
	if readErr == nil && isCompose {
		if err = s.captureComposeBaseline(ctx, remote, c, moduleID); err != nil {
			return ModuleDocument{}, err
		}
	}
	tmp := target + fmt.Sprintf(".validate.%d", time.Now().UnixNano())
	if err = uploadRemoteFile(ctx, remote.SSH, tmp, content, "0600"); err != nil {
		return ModuleDocument{}, err
	}
	defer func() { _, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(tmp), "") }()
	if isCompose {
		candidate := m
		candidate.ComposeFile = tmp
		if _, err = runSSH(ctx, remote.SSH, modular.Command(candidate)+" config --quiet", ""); err != nil {
			return ModuleDocument{}, fmt.Errorf("Compose 校验失败，未替换原配置: %w", err)
		}
	}
	if readErr == nil {
		backup := target + ".bak." + fmt.Sprint(time.Now().UnixNano())
		if _, err = runSSH(ctx, remote.SSH, "cp -p "+shellQuote(target)+" "+shellQuote(backup), ""); err != nil {
			return ModuleDocument{}, err
		}
	}
	mode := "0644"
	if file == "module.env" || strings.Contains(file, "secrets/") {
		mode = "0600"
	}
	if strings.HasSuffix(file, ".sh") {
		mode = "0755"
	}
	if _, err = runSSH(ctx, remote.SSH, "chmod "+mode+" "+shellQuote(tmp)+" && mv "+shellQuote(tmp)+" "+shellQuote(target), ""); err != nil {
		return ModuleDocument{}, err
	}
	for i := range c.Deployment.Modules {
		if c.Deployment.Modules[i].ID == moduleID {
			if isCompose {
				c.Deployment.Modules[i].Revision = modular.Hash(content)
			}
			if !isCompose {
				c.Deployment.Modules[i].ConfigChanged = true
				c.Deployment.Modules[i].FilesRevision = modular.Hash(c.Deployment.Modules[i].FilesRevision + file + content)
			}
			c.Deployment.Modules[i].Status = "prepared"
			c.Deployment.Modules[i].UpdatedAt = time.Now()
		}
	}
	if err = s.centers.UpdateDeployment(centerID, c.Deployment); err != nil {
		return ModuleDocument{}, err
	}
	return ModuleDocument{Module: m, Path: target, Content: content, Revision: modular.Hash(content)}, nil
}

func (s *Service) SaveModuleMetadata(ctx context.Context, centerID string, m domain.ComposeModule) error {
	unlock, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return err
	}
	defer unlock()
	remote, c, err := s.connect(centerID)
	if err != nil {
		return err
	}
	defer remote.Close()
	if c.Deployment == nil {
		return fmt.Errorf("旧中心需先制定模块迁移计划")
	}
	d := c.Deployment
	m.ComposeFile = path.Join(d.Root, m.ID, "compose.yml")
	if m.Timeout == 0 {
		m.Timeout = 600
	}
	m.UpdatedAt = time.Now()
	found := false
	for i, old := range d.Modules {
		if old.ID == m.ID {
			if old.Project != m.Project {
				return fmt.Errorf("已登记项目名不可修改，请通过迁移处理")
			}
			m.Status, m.Revision, m.AppliedRevision = old.Status, old.Revision, old.AppliedRevision
			m.ConfigChanged = old.ConfigChanged
			m.FilesRevision = old.FilesRevision
			d.Modules[i] = m
			found = true
			break
		}
	}
	if !found {
		m.Status = "pending"
		d.Modules = append(d.Modules, m)
	}
	if err = modular.Validate(d); err != nil {
		return err
	}
	if !found {
		content := "name: " + m.Project + "\nservices: {}\n"
		for filename, text := range map[string]string{"compose.yml": content, "module.env": "# Module variables\n"} {
			p := path.Join(path.Dir(m.ComposeFile), filename)
			if _, err = runSSH(ctx, remote.SSH, "test ! -e "+shellQuote(p), ""); err != nil {
				return fmt.Errorf("目标文件已存在，请使用未占用的模块标识")
			}
			if err = uploadRemoteFile(ctx, remote.SSH, p, text, "0600"); err != nil {
				return err
			}
		}
	}
	return s.centers.UpdateDeployment(centerID, d)
}

func (s *Service) ResumeModules(ctx context.Context, centerID string, r Reporter) (result error) {
	unlock, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return err
	}
	defer unlock()
	remote, c, err := s.connect(centerID)
	if err != nil {
		return err
	}
	defer remote.Close()
	if c.Deployment == nil {
		return nil
	}
	d := c.Deployment
	defer func() {
		if result != nil {
			d.Status = "failed"
			_ = s.centers.UpdateDeployment(centerID, d)
		}
	}()
	order, err := modular.Order(d.Modules)
	if err != nil {
		return err
	}
	d.Status = "initializing"
	if err = s.centers.UpdateDeployment(centerID, d); err != nil {
		return err
	}
	for i, m := range order {
		if m.ID == "business" && d.ReselectionRequired {
			d.Status = "waiting_business"
			if err = s.centers.UpdateDeployment(centerID, d); err != nil {
				return err
			}
			r.Message("基础模块已就绪，准备发布本次选择的软件与模型版本")
			return nil
		}
		if m.ID == "business" {
			models, e := modelasset.CurrentVersionsOnClient(ctx, remote.SSH, d.ModelsRoot)
			if e != nil {
				return e
			}
			if len(models) == 0 && d.Migration == nil {
				d.Status = "waiting_business"
				if err = s.centers.UpdateDeployment(centerID, d); err != nil {
					return err
				}
				r.Message("基础模块已初始化，等待发布业务镜像和模型；监控尚未开启")
				return nil
			}
		}
		if m.ID == "business" {
			cfg, e := moduleConfig(ctx, remote, m)
			if e != nil {
				return e
			}
			for name, spec := range cfg.Services {
				if _, _, e = remote.Client.ImageInspectWithRaw(ctx, spec.Image); e != nil {
					d.Status = "waiting_business"
					if err = s.centers.UpdateDeployment(centerID, d); err != nil {
						return err
					}
					r.Message("等待业务镜像：" + name + "（" + spec.Image + "）")
					return nil
				}
			}
		}
		if err = s.applyModule(ctx, remote, &c, m, "deploy", nil, r); err != nil {
			d.Status = "failed"
			_ = s.centers.UpdateDeployment(centerID, d)
			return err
		}
		r.Progress(float64(i+1) * 95 / float64(len(order)))
	}
	d.Status = "ready"
	if err = s.centers.UpdateDeployment(centerID, d); err != nil {
		return err
	}
	r.Progress(100)
	r.Message("中心各模块已就绪，监控已开启")
	return nil
}

func (s *Service) ModuleAction(ctx context.Context, centerID, moduleID, action string, services []string, revision string, r Reporter) error {
	unlock, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return err
	}
	defer unlock()
	remote, c, err := s.connect(centerID)
	if err != nil {
		return err
	}
	defer remote.Close()
	if c.Deployment == nil {
		return fmt.Errorf("旧中心请使用原有维护操作")
	}
	m, err := modular.Find(c, moduleID)
	if err != nil {
		return err
	}
	if !m.Enabled {
		return fmt.Errorf("模块已禁用")
	}
	plan, err := s.PlanModule(ctx, centerID, moduleID, action, services)
	if err != nil {
		return err
	}
	if revision == "" || revision != plan.Revision {
		return fmt.Errorf("模块配置已变化，请重新生成操作计划")
	}
	// 停止不再硬性拦截依赖方（网关只是反代业务、停业务让它 502 属可接受的
	// 调试场景）；运行中依赖方清单由 PlanModule 返回，前端确认框提示后果。
	if moduleID == "monitoring" && c.InitializedAt == nil && action != "stop" {
		for _, other := range c.Deployment.Modules {
			if other.Enabled && other.ID != "monitoring" {
				if other.Status != "healthy" {
					return fmt.Errorf("首次开启监控前需完成模块 %s", other.Name)
				}
				cfg, e := moduleConfig(ctx, remote, other)
				if e != nil {
					return e
				}
				if e = modular.Inspect(ctx, remote, other, modular.ServiceNames(cfg)); e != nil {
					return e
				}
			}
		}
	}
	if action == "deploy" || action == "restart" {
		for _, id := range m.DependsOn {
			dep, e := modular.Find(c, id)
			if e != nil {
				return e
			}
			cfg, e := moduleConfig(ctx, remote, dep)
			if e != nil {
				return e
			}
			if e = modular.Inspect(ctx, remote, dep, modular.ServiceNames(cfg)); e != nil {
				return fmt.Errorf("依赖 %s 未就绪: %w", id, e)
			}
		}
	}
	return s.applyModule(ctx, remote, &c, m, action, services, r)
}

func moduleConfig(ctx context.Context, remote *sshx.DockerClient, m domain.ComposeModule) (modular.Config, error) {
	text, err := runSSH(ctx, remote.SSH, modular.Command(m)+" config", "")
	if err != nil {
		return modular.Config{}, err
	}
	if err = modular.ValidateConfig(m, text); err != nil {
		return modular.Config{}, err
	}
	return modular.Parse(text)
}

func (s *Service) applyModule(ctx context.Context, remote *sshx.DockerClient, c *domain.Center, m domain.ComposeModule, action string, services []string, r Reporter) (result error) {
	timeout := m.Timeout
	if timeout < 10 {
		timeout = 600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if action != "deploy" && action != "restart" && action != "stop" {
		return fmt.Errorf("不支持的模块操作")
	}
	// defer 执行时 services 可能已被展开为全量清单，整模块操作标记需提前捕获。
	wholeModule := len(services) == 0
	defer func() {
		for i := range c.Deployment.Modules {
			if c.Deployment.Modules[i].ID == m.ID {
				if result != nil {
					m.Status = "failed"
					m.Error = result.Error()
				} else {
					m.Error = ""
				}
				m.UpdatedAt = time.Now()
				c.Deployment.Modules[i] = m
			}
		}
		if result == nil && wholeModule {
			// 逐模块手动启停后对齐中心级状态：全部启停到位时与整体暂停/
			// 恢复同口径，避免中心状态停留在 ready/paused 失真（部分模块
			// 运行的中间状态不改动，仍由对应入口收尾）。
			if c.Deployment.Status == "ready" && allEnabledModules(c.Deployment, "stopped") {
				c.Deployment.Status = "paused"
			} else if c.Deployment.Status == "paused" && allEnabledModules(c.Deployment, "healthy") {
				c.Deployment.Status = "ready"
			}
		}
		if e := s.centers.UpdateDeployment(c.ID, c.Deployment); result == nil {
			result = e
		}
	}()
	cfg, err := moduleConfig(ctx, remote, m)
	if err != nil {
		return err
	}
	if err = modular.CheckOwnership(ctx, remote, m, cfg); err != nil {
		return err
	}
	for _, n := range services {
		if _, ok := cfg.Services[n]; !ok {
			return fmt.Errorf("服务 %s 不属于模块 %s", n, m.ID)
		}
	}
	all := len(services) == 0
	if all {
		services = modular.ServiceNames(cfg)
	}
	content, err := readRemoteFile(ctx, remote.SSH, m.ComposeFile)
	if err != nil {
		return err
	}
	m.Revision = modular.Hash(content)
	resolved, err := runSSH(ctx, remote.SSH, modular.Command(m)+" config", "")
	if err != nil {
		return err
	}
	filesRevision, err := configurationRevision(ctx, remote, *c, m.ComposeFile, resolved)
	if err != nil {
		return err
	}
	published, err := readPublishedState(ctx, remote, m.ComposeFile)
	if err != nil {
		return err
	}
	if published.Files == "" || published.Files != filesRevision {
		m.ConfigChanged = true
	}
	if action == "deploy" && all && m.AppliedRevision == m.Revision && m.Status == "healthy" && !m.ConfigChanged {
		if err = modular.Inspect(ctx, remote, m, services); err == nil {
			r.Logf("模块 %s 已部署，核对就绪状态", m.Name)
			return modular.Wait(ctx, remote, m, services, m.ReadyURLs)
		}
	}
	if len(m.ModelServices) > 0 && action != "stop" {
		cfg, _, _, e := s.centers.Connection(c.ID)
		if e != nil {
			return e
		}
		if e = modelasset.RepairRuntimePathsOnClient(ctx, remote.SSH, cfg.Password, c.Deployment.ModelsRoot, r); e != nil {
			return e
		}
	}
	command := modular.Command(m)
	switch action {
	case "deploy":
		command += " up -d"
		if m.ConfigChanged {
			command += " --force-recreate"
		}
		if !all {
			command += " --no-deps"
		}
	case "restart":
		command += " restart"
	case "stop":
		command += " stop"
	}
	selected := []string{}
	for _, n := range services {
		skip := false
		if action == "restart" {
			for _, init := range m.InitServices {
				if n == init {
					skip = true
				}
			}
		}
		if !skip {
			selected = append(selected, n)
			command += " " + shellQuote(n)
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("没有可操作的服务")
	}
	m.Status = "starting"
	for i := range c.Deployment.Modules {
		if c.Deployment.Modules[i].ID == m.ID {
			c.Deployment.Modules[i] = m
		}
	}
	if err = s.centers.UpdateDeployment(c.ID, c.Deployment); err != nil {
		return err
	}
	r.Message("正在" + action + "模块 " + m.Name)
	r.Log("$ " + command)
	output, err := runSSH(ctx, remote.SSH, command, "")
	if strings.TrimSpace(output) != "" {
		r.Log(strings.TrimSpace(output))
	}
	if err != nil {
		return fmt.Errorf("模块 %s 操作失败: %w", m.Name, err)
	}
	if action == "stop" {
		m.Status = "stopped"
		return nil
	}
	urls := modular.ReadyURLsForServices(m, selected)
	if all {
		urls = m.ReadyURLs
	}
	if err = modular.Wait(ctx, remote, m, selected, urls); err != nil {
		return err
	}
	m.Status = "healthy"
	if !all {
		if e := modular.Inspect(ctx, remote, m, modular.ServiceNames(cfg)); e != nil {
			m.Status = "partial"
		}
	}
	if all && action == "deploy" {
		latest, err := runSSH(ctx, remote.SSH, modular.Command(m)+" config", "")
		if err != nil {
			return err
		}
		latestFiles, err := configurationRevision(ctx, remote, *c, m.ComposeFile, latest)
		if err != nil {
			return err
		}
		if latest != resolved || latestFiles != filesRevision {
			return fmt.Errorf("部署期间配置发生变化，请刷新后重新发布")
		}
		if err = writePublishedState(ctx, remote, m.ComposeFile, composePublishedState{Content: m.Revision, Resolved: modular.Hash(resolved), Files: filesRevision}); err != nil {
			return err
		}
		m.AppliedRevision = m.Revision
		m.ConfigChanged = false
	}
	r.Message("模块 " + m.Name + " 已就绪")
	return nil
}

// allEnabledModules 报告启用模块是否全部处于指定状态（禁用模块不参与判断），
// 用于逐模块手动启停后与中心级 ready/paused 状态对齐。
func allEnabledModules(d *domain.Deployment, status string) bool {
	for _, m := range d.Modules {
		if m.Enabled && m.Status != status {
			return false
		}
	}
	return true
}

func guardModuleFile(ctx context.Context, remote *sshx.DockerClient, c domain.Center, m domain.ComposeModule, target string) error {
	if c.Deployment == nil {
		return fmt.Errorf("请使用原 Compose 编辑入口")
	}
	command := "deploy=$(realpath " + shellQuote(c.Deployment.Root) + ") && root=$(realpath -m " + shellQuote(path.Dir(m.ComposeFile)) + ") && target=$(realpath -m " + shellQuote(target) + ") && case \"$root\" in \"$deploy\"/*) ;; *) exit 1;; esac && case \"$target\" in \"$root\"/*) ;; *) exit 1;; esac"
	if _, err := runSSH(ctx, remote.SSH, command, ""); err != nil {
		return fmt.Errorf("文件超出模块目录，或路径无法解析")
	}
	return nil
}
