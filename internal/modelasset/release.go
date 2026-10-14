package modelasset

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"golang.org/x/crypto/ssh"
)

const manifestSchemaVersion = 1

var manifestNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type ManifestArtifact struct {
	Path       string `json:"path"`
	TargetPath string `json:"target_path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

type ManifestFormat struct {
	ID              string             `json:"id"`
	Platform        string             `json:"platform"`
	HardwareProfile string             `json:"hardware_profile,omitempty"`
	RepositoryPath  string             `json:"repository_path"`
	Artifacts       []ManifestArtifact `json:"artifacts"`
}

type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	Model         string           `json:"model"`
	Version       string           `json:"version"`
	CreatedAt     time.Time        `json:"created_at"`
	Formats       []ManifestFormat `json:"formats"`
}

type Version struct {
	SchemaVersion  int              `json:"schema_version"`
	Formats        []ManifestFormat `json:"formats"`
	ManifestKey    string           `json:"manifest_key"`
	ManifestDigest string           `json:"manifest_digest"`
	Model          string           `json:"model"`
	Version        string           `json:"version"`
	CreatedAt      time.Time        `json:"created_at"`
}

type Selection struct {
	ManifestKey    string `json:"manifest_key"`
	ManifestDigest string `json:"manifest_digest"`
}

type ReleaseItem struct {
	SchemaVersion   int                `json:"schema_version,omitempty"`
	RepositoryPath  string             `json:"repository_path,omitempty"`
	Artifacts       []ManifestArtifact `json:"artifacts,omitempty"`
	Formats         []ManifestFormat   `json:"formats,omitempty"`
	FormatID        string             `json:"format_id,omitempty"`
	ManifestKey     string             `json:"manifest_key"`
	ManifestDigest  string             `json:"manifest_digest"`
	Model           string             `json:"model"`
	Version         string             `json:"version"`
	Platform        string             `json:"platform,omitempty"`
	HardwareProfile string             `json:"hardware_profile,omitempty"`
}

type CurrentVersion struct {
	SchemaVersion   int                `json:"schema_version"`
	RepositoryPath  string             `json:"repository_path,omitempty"`
	StoragePath     string             `json:"storage_path,omitempty"`
	Artifacts       []ManifestArtifact `json:"artifacts,omitempty"`
	FormatID        string             `json:"format_id"`
	Model           string             `json:"model"`
	Version         string             `json:"version"`
	Platform        string             `json:"platform"`
	HardwareProfile string             `json:"hardware_profile,omitempty"`
	ManifestDigest  string             `json:"manifest_digest"`
	TargetPath      string             `json:"target_path"`
	ArtifactSize    int64              `json:"artifact_size,omitempty"`
	Pinned          bool               `json:"pinned"`
	ActivatedAt     string             `json:"activated_at"`
	Healthy         bool               `json:"healthy"`
	HealthError     string             `json:"health_error,omitempty"`
}

type ModelSetPin struct {
	Pinned          bool              `json:"pinned"`
	PinnedAt        string            `json:"pinned_at,omitempty"`
	ManifestDigests map[string]string `json:"manifest_digests,omitempty"`
}

type ReleasePlan struct {
	Ready         bool                      `json:"ready"`
	Error         string                    `json:"error,omitempty"`
	Action        string                    `json:"action"`
	DownloadBytes int64                     `json:"download_bytes"`
	Current       map[string]CurrentVersion `json:"current"`
	Targets       []ReleaseItem             `json:"targets"`
}

func (s *Service) Versions() ([]Version, error) {
	objects, err := s.Catalog()
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Object, len(objects))
	for _, object := range objects {
		byKey[object.Key] = object
	}
	_, bucket, _, err := s.client()
	if err != nil {
		return nil, err
	}
	versions := make([]Version, 0)
	for _, object := range objects {
		if path.Base(object.Key) != "manifest.json" {
			continue
		}
		body, getErr := bucket.GetObject(object.Key)
		if getErr != nil {
			return nil, fmt.Errorf("读取模型 Manifest %s: %w", object.Key, getErr)
		}
		data, readErr := io.ReadAll(io.LimitReader(body, 1024*1024+1))
		closeErr := body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取模型 Manifest %s: %w", object.Key, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > 1024*1024 {
			return nil, fmt.Errorf("模型 Manifest 不能超过 1 MiB: %s", object.Key)
		}
		version, versionErr := parseVersion(data, object.Key, byKey)
		if versionErr != nil {
			return nil, versionErr
		}
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Model != versions[j].Model {
			return versions[i].Model < versions[j].Model
		}
		return versions[i].CreatedAt.After(versions[j].CreatedAt)
	})
	return versions, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("不支持 schema_version=%d", manifest.SchemaVersion)
	}
	if !manifestNamePattern.MatchString(manifest.Model) {
		return fmt.Errorf("model 只能包含字母、数字、点、下划线和短横线")
	}
	if !manifestNamePattern.MatchString(manifest.Version) {
		return fmt.Errorf("version 只能包含字母、数字、点、下划线和短横线")
	}
	if len(manifest.Formats) == 0 {
		return fmt.Errorf("formats 不能为空")
	}
	ids := map[string]bool{}
	profiles := map[string]bool{}
	for _, format := range manifest.Formats {
		if !manifestNamePattern.MatchString(format.ID) || ids[format.ID] {
			return fmt.Errorf("format.id 无效或重复: %s", format.ID)
		}
		ids[format.ID] = true
		if err := validateFormat(format); err != nil {
			return fmt.Errorf("模型格式 %s 无效: %w", format.ID, err)
		}
		key := format.Platform + ":" + normalizeHardwareName(format.HardwareProfile)
		if profiles[key] {
			return fmt.Errorf("平台和硬件配置重复: %s", format.ID)
		}
		profiles[key] = true
	}
	return nil
}

func (s *Service) PrepareSelections(selections []Selection) ([]ReleaseItem, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("请至少选择一个模型版本")
	}
	versions, err := s.Versions()
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Version, len(versions))
	for _, version := range versions {
		byKey[version.ManifestKey] = version
	}
	items := make([]ReleaseItem, 0, len(selections))
	seenModels := map[string]bool{}
	for _, selection := range selections {
		version, found := byKey[selection.ManifestKey]
		if !found {
			return nil, fmt.Errorf("模型版本不存在或尚未就绪: %s", selection.ManifestKey)
		}
		if selection.ManifestDigest == "" || selection.ManifestDigest != version.ManifestDigest {
			return nil, fmt.Errorf("模型 %s:%s 的 Manifest 已变化，请刷新后重新发布", version.Model, version.Version)
		}
		if seenModels[version.Model] {
			return nil, fmt.Errorf("同一个模型一次只能选择一个版本: %s", version.Model)
		}
		seenModels[version.Model] = true
		items = append(items, ReleaseItem{
			SchemaVersion: version.SchemaVersion, Formats: version.Formats,
			ManifestKey: version.ManifestKey, ManifestDigest: version.ManifestDigest,
			Model: version.Model, Version: version.Version,
		})
	}
	if err := validateReleaseTargets(items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) CheckRelease(ctx context.Context, centerID string, items []ReleaseItem) ReleasePlan {
	plan := ReleasePlan{Ready: false, Action: "blocked", Current: map[string]CurrentVersion{}, Targets: items}
	_, accelerator, err := s.releaseCenter(ctx, centerID)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	cfg, _, stored, err := s.centers.Connection(centerID)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	client, _, err := sshx.Dial(cfg)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	defer client.Close()
	targetRoot := modular.ModelsRoot(stored)
	items, err = resolveReleaseItems(ctx, client, accelerator, items)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	plan.Targets = items
	for _, item := range items {
		if err = checkHardwareProfile(ctx, client, item); err != nil {
			plan.Error = err.Error()
			return plan
		}
	}
	modelSetPin, err := readModelSetPin(ctx, client, targetRoot)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	for _, item := range items {
		if modelSetPinConflict(modelSetPin, item) {
			plan.Error = "中心当前模型版本组已固定，请先整体解绑后再调整模型"
			return plan
		}
	}
	checkFiles := remoteFiles{client: client, root: targetRoot}
	allCurrent := true
	for _, item := range items {
		statePath := releaseStatePath(targetRoot, item.Model)
		output, readErr := runSSH(ctx, client, "cat "+shellQuote(statePath)+" 2>/dev/null", nil)
		var current CurrentVersion
		if readErr == nil && json.Unmarshal(output, &current) == nil {
			current.Pinned = modelSetPin.Pinned
			plan.Current[item.Model] = current
		}
		modelFilesReady := current.ManifestDigest == item.ManifestDigest && currentVersionFilesReady(ctx, checkFiles, current, item, false)
		if !modelFilesReady {
			allCurrent = false
		}
		if !modelFilesReady {
			plan.DownloadBytes += artifactBytes(item.Artifacts)
		}
	}
	plan.Ready = true
	if allCurrent {
		plan.Action = "skip"
	} else {
		plan.Action = "download_and_activate"
	}
	return plan
}

func modelSetPinConflict(pin ModelSetPin, target ReleaseItem) bool {
	return pin.Pinned && pin.ManifestDigests[target.Model] != target.ManifestDigest
}

func platformCompatible(accelerator, platform string) bool {
	return platform == "onnx" || accelerator == platform
}

func resolveReleaseItems(ctx context.Context, client *ssh.Client, accelerator string, items []ReleaseItem) ([]ReleaseItem, error) {
	resolved := make([]ReleaseItem, 0, len(items))
	var gpuNames []string
	var gpuErr error
	for _, item := range items {
		if len(item.Formats) == 0 {
			if !platformCompatible(accelerator, item.Platform) {
				return nil, fmt.Errorf("中心平台为 %s，不能发布 %s 模型 %s:%s", accelerator, item.Platform, item.Model, item.Version)
			}
			resolved = append(resolved, item)
			continue
		}
		if accelerator == "nvidia" && gpuNames == nil && gpuErr == nil {
			gpuNames, gpuErr = readNVIDIAGPUNames(ctx, client)
		}
		selected, selectErr := selectReleaseFormat(item, accelerator, gpuNames, gpuErr)
		if selectErr != nil {
			return nil, selectErr
		}
		item.Platform = selected.Platform
		item.FormatID = selected.ID
		item.HardwareProfile = selected.HardwareProfile
		item.RepositoryPath = selected.RepositoryPath
		item.Artifacts = selected.Artifacts
		item.Formats = nil
		resolved = append(resolved, item)
	}
	return resolved, validateReleaseTargets(resolved)
}

func selectReleaseFormat(item ReleaseItem, accelerator string, gpuNames []string, gpuErr error) (ManifestFormat, error) {
	native := make([]ManifestFormat, 0, 1)
	var fallback *ManifestFormat
	for index := range item.Formats {
		format := item.Formats[index]
		if format.Platform == "onnx" {
			candidate := format
			fallback = &candidate
			continue
		}
		if format.Platform != accelerator {
			continue
		}
		if format.Platform == "nvidia" && (gpuErr != nil || !hardwareProfileMatches(gpuNames, format.HardwareProfile)) {
			continue
		}
		native = append(native, format)
	}
	switch len(native) {
	case 1:
		return native[0], nil
	case 0:
		if fallback != nil {
			return *fallback, nil
		}
		if accelerator == "nvidia" && gpuErr != nil {
			return ManifestFormat{}, fmt.Errorf("无法读取中心 NVIDIA GPU 型号，不能为模型 %s:%s 选择 plan: %w", item.Model, item.Version, gpuErr)
		}
		return ManifestFormat{}, fmt.Errorf("模型 %s:%s 没有适用于中心平台 %s 的格式", item.Model, item.Version, accelerator)
	default:
		return ManifestFormat{}, fmt.Errorf("模型 %s:%s 有多个格式匹配中心硬件，请收紧 hardware_profile", item.Model, item.Version)
	}
}

func readNVIDIAGPUNames(ctx context.Context, client *ssh.Client) ([]string, error) {
	output, err := runSSH(ctx, client, "nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null", nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("中心未返回 NVIDIA GPU 型号")
	}
	return names, nil
}

func hardwareProfileMatches(names []string, profile string) bool {
	wanted := normalizeHardwareName(profile)
	if wanted == "" || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !strings.Contains(normalizeHardwareName(name), wanted) {
			return false
		}
	}
	return true
}

func checkHardwareProfile(ctx context.Context, client *ssh.Client, item ReleaseItem) error {
	if item.Platform != "nvidia" || item.HardwareProfile == "" {
		return nil
	}
	names, err := readNVIDIAGPUNames(ctx, client)
	if err != nil {
		return fmt.Errorf("无法读取中心 NVIDIA GPU 型号，不能校验模型 %s:%s 的硬件型号 %s", item.Model, item.Version, item.HardwareProfile)
	}
	if !hardwareProfileMatches(names, item.HardwareProfile) {
		return fmt.Errorf("中心 GPU 型号为 %s，不能发布需要 %s 的模型 %s:%s", strings.Join(names, ", "), item.HardwareProfile, item.Model, item.Version)
	}
	return nil
}

func normalizeHardwareName(value string) string {
	var normalized strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}

func releaseStatePath(root, model string) string {
	return path.Join(root, ".center-manager", "releases", model+".json")
}

func modelSetPinPath(root string) string {
	return path.Join(root, ".center-manager", "model-set.json")
}

func readModelSetPin(ctx context.Context, client *ssh.Client, root string) (ModelSetPin, error) {
	statePath := modelSetPinPath(root)
	command := "if [ -e " + shellQuote(statePath) + " ]; then cat " + shellQuote(statePath) + "; else printf '%s' '{\"pinned\":false}'; fi"
	output, err := runSSH(ctx, client, command, nil)
	if err != nil {
		return ModelSetPin{}, fmt.Errorf("读取中心模型版本组固定状态: %w", err)
	}
	var pin ModelSetPin
	if err = json.Unmarshal(output, &pin); err != nil {
		return ModelSetPin{}, fmt.Errorf("中心模型版本组固定状态无效")
	}
	if pin.ManifestDigests == nil {
		pin.ManifestDigests = map[string]string{}
	}
	if pin.Pinned && len(pin.ManifestDigests) == 0 {
		return ModelSetPin{}, fmt.Errorf("中心模型版本组固定快照为空")
	}
	for model, digest := range pin.ManifestDigests {
		if !manifestNamePattern.MatchString(model) || !sha256Pattern.MatchString(digest) {
			return ModelSetPin{}, fmt.Errorf("中心模型版本组固定快照无效")
		}
	}
	return pin, nil
}

// CurrentVersions reads the authoritative model release state stored on a
// center. The manager database is intentionally not used so pinning survives
// manager reinstalls and cannot drift from the center being managed.
func (s *Service) CurrentVersions(ctx context.Context, centerID string) ([]CurrentVersion, error) {
	cfg, _, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return nil, err
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)
	return CurrentVersionsOnClient(ctx, client, modular.ModelsRoot(stored))
}

// CurrentVersionsOnClient reuses an existing center SSH connection, which is
// useful for the center overview that already queried Docker on that center.
func CurrentVersionsOnClient(ctx context.Context, client *ssh.Client, root string) ([]CurrentVersion, error) {
	stateDir := path.Join(root, ".center-manager", "releases")
	command := "state_dir=" + shellQuote(stateDir) + "; if [ -d \"$state_dir\" ]; then for state in \"$state_dir\"/*.json; do [ -f \"$state\" ] || continue; cat \"$state\"; printf '\\n'; done; fi"
	output, err := runSSH(ctx, client, command, nil)
	if err != nil {
		return nil, fmt.Errorf("读取中心模型版本: %w", err)
	}
	versions := make([]CurrentVersion, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	files := remoteFiles{client: client, root: root}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var current CurrentVersion
		if err = json.Unmarshal(line, &current); err != nil {
			return nil, fmt.Errorf("解析中心模型版本状态: %w", err)
		}
		if err = validateCurrentVersion(current); err != nil {
			return nil, fmt.Errorf("中心模型版本状态无效: %w", err)
		}
		item := releaseItemFromCurrent(current)
		checkErr := currentVersionHealth(ctx, files, current, item)
		if checkErr != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("模型健康检查未完成: %w", ctx.Err())
			}
		}
		setCurrentVersionHealth(&current, checkErr)
		versions = append(versions, current)
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	pin, err := readModelSetPin(ctx, client, root)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(versions))
	for index := range versions {
		versions[index].Pinned = pin.Pinned
		seen[versions[index].Model] = true
		if pin.Pinned && pin.ManifestDigests[versions[index].Model] != versions[index].ManifestDigest {
			versions[index].Healthy = false
			versions[index].HealthError = "当前状态已偏离固定的模型版本组快照"
		}
	}
	if pin.Pinned {
		for model, digest := range pin.ManifestDigests {
			if !seen[model] {
				versions = append(versions, CurrentVersion{Model: model, ManifestDigest: digest, Pinned: true, Healthy: false, HealthError: "固定快照中的模型版本状态缺失"})
			}
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Model < versions[j].Model })
	return versions, nil
}

// RepairRuntimePathsOnClient reconciles the stable model paths consumed by
// inference services from the authoritative release states on the center. It
// is intentionally safe to run before every whole-Compose start/restart so
// releases created before the flat runtime aliases were introduced are
// migrated without downloading or changing model versions.
func RepairRuntimePathsOnClient(ctx context.Context, client *ssh.Client, password, root string, r Reporter) error {
	versions, err := CurrentVersionsOnClient(ctx, client, root)
	if err != nil {
		return fmt.Errorf("读取已发布模型状态: %w", err)
	}
	if len(versions) == 0 {
		r.Log("中心没有已发布模型状态，跳过运行时模型路径修复")
		return nil
	}
	files, err := prepareRemoteFiles(ctx, client, password, root, r)
	if err != nil {
		return err
	}
	for _, current := range versions {
		if err = ctx.Err(); err != nil {
			return err
		}
		item := releaseItemFromCurrent(current)
		if current.StoragePath == "" {
			current, err = migrateLegacyCurrentVersion(ctx, files, current, item, r)
		} else {
			current, err = repairStoredCurrentVersion(ctx, files, current, item, r)
		}
		if err != nil {
			return fmt.Errorf("修复模型 %s 的扁平运行时路径: %w", current.Model, err)
		}
		if !currentVersionFilesReady(ctx, files, current, item, true) {
			return fmt.Errorf("模型 %s 的扁平运行时路径修复后校验失败", current.Model)
		}
		r.Logf("运行时模型路径校验通过：%s", current.Model)
	}
	r.Logf("全部 %d 个已发布模型的运行时路径已校验", len(versions))
	return nil
}

func validateCurrentVersion(current CurrentVersion) error {
	if current.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("不支持 schema_version=%d", current.SchemaVersion)
	}
	if !manifestNamePattern.MatchString(current.Model) || !manifestNamePattern.MatchString(current.Version) || !manifestNamePattern.MatchString(current.FormatID) {
		return fmt.Errorf("模型、版本或格式标识无效")
	}
	if !sha256Pattern.MatchString(current.ManifestDigest) {
		return fmt.Errorf("Manifest 摘要无效")
	}
	item := releaseItemFromCurrent(current)
	if err := validateFormat(ManifestFormat{ID: "resolved", Platform: current.Platform, HardwareProfile: current.HardwareProfile, RepositoryPath: current.RepositoryPath, Artifacts: current.Artifacts}); err != nil {
		return err
	}
	alias, _, _, err := runtimeAlias(item)
	if err != nil {
		return err
	}
	if current.StoragePath == "" {
		if current.TargetPath != current.RepositoryPath {
			return fmt.Errorf("旧模型状态的 target_path 与 repository_path 不一致")
		}
		return nil
	}
	legacyNVIDIAAlias := current.Platform == "nvidia" && current.TargetPath == path.Join("plan", current.HardwareProfile, current.Model+".plan")
	if current.TargetPath != alias && !legacyNVIDIAAlias {
		return fmt.Errorf("target_path 必须是扁平运行时路径 %s", alias)
	}
	if !safeStoragePath(current.StoragePath) {
		return fmt.Errorf("storage_path 无效")
	}
	return nil
}

// SetModelSetPinned atomically fixes or unbinds the center's complete model
// version snapshot. It shares the center mutation lock with releases.
func (s *Service) SetModelSetPinned(ctx context.Context, centerID string, pinned bool) (ModelSetPin, error) {
	release, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return ModelSetPin{}, err
	}
	defer release()
	cfg, _, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return ModelSetPin{}, err
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		return ModelSetPin{}, err
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)
	targetRoot := modular.ModelsRoot(stored)
	files, err := prepareRemoteFiles(ctx, client, cfg.Password, targetRoot, discardReporter{})
	if err != nil {
		return ModelSetPin{}, err
	}
	pin := ModelSetPin{Pinned: pinned, ManifestDigests: map[string]string{}}
	if pinned {
		versions, versionErr := CurrentVersionsOnClient(ctx, client, targetRoot)
		if versionErr != nil {
			return ModelSetPin{}, versionErr
		}
		if len(versions) == 0 {
			return ModelSetPin{}, fmt.Errorf("中心尚未发布任何受管模型，无法固定模型版本组")
		}
		pin.PinnedAt = time.Now().UTC().Format(time.RFC3339)
		for _, current := range versions {
			if !sha256Pattern.MatchString(current.ManifestDigest) {
				return ModelSetPin{}, fmt.Errorf("中心模型 %s 的版本状态无效", current.Model)
			}
			pin.ManifestDigests[current.Model] = current.ManifestDigest
		}
	}
	statePath := modelSetPinPath(targetRoot)
	if _, err = files.run(ctx, releasePathGuard(targetRoot, statePath, statePath+".tmp")); err != nil {
		return ModelSetPin{}, fmt.Errorf("模型版本组固定状态路径包含不安全符号链接: %w", err)
	}
	stateJSON, _ := json.Marshal(pin)
	tempPath := statePath + ".tmp"
	command := "umask 022; printf %s " + shellQuote(string(stateJSON)) + " > " + shellQuote(tempPath) + "; mv -Tf " + shellQuote(tempPath) + " " + shellQuote(statePath)
	if _, err = files.run(ctx, command); err != nil {
		return ModelSetPin{}, fmt.Errorf("更新模型版本组固定状态: %w", err)
	}
	return pin, nil
}

type discardReporter struct{}

func (discardReporter) Log(string)          {}
func (discardReporter) Logf(string, ...any) {}
func (discardReporter) Progress(float64)    {}
func (discardReporter) Message(string)      {}

func (s *Service) releaseCenter(ctx context.Context, centerID string) (string, string, error) {
	center, err := s.centers.GetPublic(centerID)
	if err != nil {
		return "", "", err
	}
	accelerator := center.DetectedAccelerator
	if accelerator == "" {
		accelerator = center.Accelerator
	}
	if accelerator == "" || accelerator == "auto" {
		return center.Name, accelerator, fmt.Errorf("中心 %s 尚未识别加速平台，请先执行硬件检测", center.Name)
	}
	return center.Name, accelerator, nil
}

func (s *Service) ReleaseCenter(ctx context.Context, centerID string, items []ReleaseItem, r Reporter) error {
	if err := validateReleaseTargets(items); err != nil {
		return err
	}
	release, err := centerlock.AcquireContext(ctx, centerID)
	if err != nil {
		return err
	}
	defer release()
	centerName, accelerator, err := s.releaseCenter(ctx, centerID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !sha256Pattern.MatchString(item.ManifestDigest) {
			return fmt.Errorf("模型 %s 的 Manifest 摘要无效", item.Model)
		}
		if !platformCompatible(accelerator, item.Platform) {
			return fmt.Errorf("中心平台为 %s，不能发布 %s 模型 %s:%s", accelerator, item.Platform, item.Model, item.Version)
		}
	}
	_, bucket, _, err := s.client()
	if err != nil {
		return err
	}
	cfg, _, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return err
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)
	for _, item := range items {
		if err = checkHardwareProfile(ctx, client, item); err != nil {
			return err
		}
	}
	targetRoot := modular.ModelsRoot(stored)
	files, err := prepareRemoteFiles(ctx, client, cfg.Password, targetRoot, r)
	if err != nil {
		return err
	}
	modelSetPin, err := readModelSetPin(ctx, client, targetRoot)
	if err != nil {
		return err
	}
	for _, item := range items {
		if modelSetPinConflict(modelSetPin, item) {
			return fmt.Errorf("中心当前模型版本组已固定，请先整体解绑后再调整模型")
		}
	}
	currentByModel := make(map[string]CurrentVersion, len(items))
	for _, item := range items {
		output, stateErr := files.run(ctx, "cat "+shellQuote(releaseStatePath(targetRoot, item.Model))+" 2>/dev/null")
		if stateErr != nil {
			continue
		}
		var current CurrentVersion
		if json.Unmarshal(output, &current) != nil || current.Model != item.Model || validateCurrentVersion(current) != nil {
			return fmt.Errorf("中心模型 %s 的版本状态无效，请先修复状态文件", item.Model)
		}
		currentByModel[item.Model] = current
	}
	if _, err = files.run(ctx, "command -v sha256sum >/dev/null 2>&1 || command -v openssl >/dev/null 2>&1"); err != nil {
		return fmt.Errorf("中心缺少 sha256sum 或 openssl，无法校验模型文件")
	}
	var totalBytes int64
	for _, item := range items {
		totalBytes += artifactBytes(item.Artifacts)
	}
	var completed int64
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current := currentByModel[item.Model]
		if err = releaseRepository(ctx, files, bucket, item, current, completed, totalBytes, r); err != nil {
			return err
		}
		completed += artifactBytes(item.Artifacts)
		progress(r, completed, totalBytes)
	}
	r.Progress(100)
	r.Message("模型版本发布完成：" + centerName)
	return nil
}

func releasePathGuard(root string, paths ...string) string {
	seen := map[string]bool{}
	checks := make([]string, 0)
	for _, value := range paths {
		current := value
		// The stable target itself is expected to become a symlink. Its parents
		// still must be real directories so activation cannot escape TargetRoot.
		if !strings.HasPrefix(value, root+"/.center-manager/") {
			current = path.Dir(value)
		}
		for current != "/" && current != "." {
			if !seen[current] {
				checks = append(checks, "test ! -L "+shellQuote(current))
				seen[current] = true
			}
			if current == root {
				break
			}
			current = path.Dir(current)
		}
	}
	return strings.Join(checks, " && ")
}

func verifyRemoteArtifact(ctx context.Context, files remoteFiles, filePath string, artifact ManifestArtifact) bool {
	return verifyRemoteArtifactWithStat(ctx, files, filePath, artifact, "stat -c %s ")
}

func verifyRemoteArtifactFollowingSymlink(ctx context.Context, files remoteFiles, filePath string, artifact ManifestArtifact) bool {
	return verifyRemoteArtifactWithStat(ctx, files, filePath, artifact, "stat -L -c %s ")
}

func verifyRemoteArtifactWithStat(ctx context.Context, files remoteFiles, filePath string, artifact ManifestArtifact, statCommand string) bool {
	command := "test -f " + shellQuote(filePath) + " && [ \"$(" + statCommand + shellQuote(filePath) + ")\" = " + shellQuote(strconv.FormatInt(artifact.Size, 10)) + " ] && " +
		"actual=$(if command -v sha256sum >/dev/null 2>&1; then sha256sum " + shellQuote(filePath) + " | awk '{print $1}'; elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 " + shellQuote(filePath) + " | awk '{print $NF}'; else exit 127; fi) && [ \"$actual\" = " + shellQuote(artifact.SHA256) + " ]"
	_, err := files.run(ctx, command)
	return err == nil
}
