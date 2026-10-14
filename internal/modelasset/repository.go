package modelasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func parseVersion(data []byte, key string, objects map[string]Object) (Version, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Version{}, fmt.Errorf("解析模型 Manifest %s: %w", key, err)
	}
	if err := validateManifest(m); err != nil {
		return Version{}, fmt.Errorf("模型 Manifest %s 无效: %w", key, err)
	}
	var artifacts []ManifestArtifact
	for _, format := range m.Formats {
		artifacts = append(artifacts, format.Artifacts...)
	}
	for _, artifact := range artifacts {
		artifactKey := path.Join(path.Dir(key), artifact.Path)
		object, found := objects[artifactKey]
		if !found {
			return Version{}, fmt.Errorf("模型 %s:%s 的文件不存在: %s", m.Model, m.Version, artifactKey)
		}
		if object.Size != artifact.Size {
			return Version{}, fmt.Errorf("文件 %s 的大小与 Manifest 不一致", artifactKey)
		}
	}
	digest := sha256.Sum256(data)
	return Version{SchemaVersion: m.SchemaVersion, ManifestKey: key, ManifestDigest: hex.EncodeToString(digest[:]), Model: m.Model,
		Version: m.Version, CreatedAt: m.CreatedAt, Formats: m.Formats}, nil
}

func validateFormat(v ManifestFormat) error {
	if v.Platform != "onnx" && v.Platform != "nvidia" && v.Platform != "denglin" {
		return fmt.Errorf("platform 必须是 onnx、nvidia 或 denglin")
	}
	if v.Platform == "nvidia" && v.HardwareProfile == "" {
		return fmt.Errorf("NVIDIA 格式必须声明 hardware_profile")
	}
	if v.Platform != "nvidia" && v.HardwareProfile != "" {
		return fmt.Errorf("hardware_profile 仅适用于 nvidia 模型")
	}
	if v.HardwareProfile != "" && !manifestNamePattern.MatchString(v.HardwareProfile) {
		return fmt.Errorf("hardware_profile 无效")
	}
	if !safeRelativePath(v.RepositoryPath) || v.RepositoryPath == "artifact" || len(v.Artifacts) == 0 {
		return fmt.Errorf("repository_path 或 artifacts 无效")
	}
	for _, segment := range strings.Split(v.RepositoryPath, "/") {
		if !manifestNamePattern.MatchString(segment) || segment == ".center-manager" {
			return fmt.Errorf("repository_path 无效")
		}
	}
	paths := map[string]bool{}
	var size int64
	for _, artifact := range v.Artifacts {
		if !safeRelativePath(artifact.Path) || !strings.HasPrefix(artifact.Path, "artifact/") || !safeRelativePath(artifact.TargetPath) {
			return fmt.Errorf("artifacts 路径无效: %s", artifact.Path)
		}
		if !strings.HasPrefix(artifact.TargetPath, v.RepositoryPath+"/") {
			return fmt.Errorf("文件 %s 不在 repository_path 下", artifact.Path)
		}
		if v.ID != "resolved" {
			prefix := "artifact/" + v.ID + "/"
			if !strings.HasPrefix(artifact.Path, prefix) || artifact.TargetPath != v.RepositoryPath+"/"+strings.TrimPrefix(artifact.Path, prefix) {
				return fmt.Errorf("文件 %s 的 target_path 必须对应模型格式 repository_path 下的相同相对路径", artifact.Path)
			}
		}
		if paths[artifact.Path] {
			return fmt.Errorf("重复的模型文件: %s", artifact.Path)
		}
		paths[artifact.Path] = true
		if artifact.Size < 0 || artifact.Size > math.MaxInt64-size {
			return fmt.Errorf("文件 %s 的 size 无效", artifact.Path)
		}
		size += artifact.Size
		if !sha256Pattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("文件 %s 的 sha256 无效", artifact.Path)
		}
	}
	return nil
}

func artifactBytes(artifacts []ManifestArtifact) int64 {
	var size int64
	for _, artifact := range artifacts {
		size += artifact.Size
	}
	return size
}

func validateReleaseTargets(items []ReleaseItem) error {
	targets := make([]string, 0, len(items))
	models := map[string]bool{}
	for _, item := range items {
		if item.SchemaVersion != manifestSchemaVersion {
			return fmt.Errorf("不支持 schema_version=%d", item.SchemaVersion)
		}
		if models[item.Model] {
			return fmt.Errorf("同一个模型一次只能选择一个版本: %s", item.Model)
		}
		models[item.Model] = true
		if len(item.Formats) > 0 {
			m := Manifest{SchemaVersion: manifestSchemaVersion, Model: item.Model, Version: item.Version, Formats: item.Formats}
			if err := validateManifest(m); err != nil {
				return err
			}
			for _, format := range item.Formats {
				if _, _, _, err := runtimeAlias(ReleaseItem{Model: item.Model, Platform: format.Platform, HardwareProfile: format.HardwareProfile, RepositoryPath: format.RepositoryPath, Artifacts: format.Artifacts}); err != nil {
					return err
				}
			}
			continue
		}
		if err := validateFormat(ManifestFormat{ID: "resolved", Platform: item.Platform, HardwareProfile: item.HardwareProfile, RepositoryPath: item.RepositoryPath, Artifacts: item.Artifacts}); err != nil {
			return err
		}
		if _, _, _, err := runtimeAlias(item); err != nil {
			return err
		}
		for _, previous := range targets {
			if item.RepositoryPath == previous || strings.HasPrefix(item.RepositoryPath, previous+"/") || strings.HasPrefix(previous, item.RepositoryPath+"/") {
				return fmt.Errorf("发布目标目录重叠: %s 和 %s", previous, item.RepositoryPath)
			}
		}
		targets = append(targets, item.RepositoryPath)
	}
	return nil
}

func verifyRepository(ctx context.Context, files remoteFiles, repository string, artifacts []ManifestArtifact, hashes bool) bool {
	if !safeRelativePath(repository) || len(artifacts) == 0 {
		return false
	}
	for _, artifact := range artifacts {
		if !safeRelativePath(artifact.TargetPath) || !strings.HasPrefix(artifact.TargetPath, repository+"/") {
			return false
		}
		name := path.Join(files.root, artifact.TargetPath)
		if hashes {
			if !verifyRemoteArtifact(ctx, files, name, artifact) {
				return false
			}
		} else {
			// Include a regular-file check: stat alone also returns a size for directories.
			if _, err := files.run(ctx, "test -f "+shellQuote(name)); err != nil || files.fileSize(ctx, name) != artifact.Size {
				return false
			}
		}
	}
	return true
}

// runtimeAlias describes the only public path consumed by inference services.
// Version files live below .center-manager/repositories and are never exposed
// as public per-model directories.
func runtimeAlias(item ReleaseItem) (aliasPath, linkTarget string, artifact ManifestArtifact, err error) {
	if len(item.Artifacts) != 1 {
		return "", "", ManifestArtifact{}, fmt.Errorf("模型 %s 的 %s 格式必须且只能包含一个运行时文件", item.Model, item.Platform)
	}
	artifact = item.Artifacts[0]
	var relativeAlias string
	switch item.Platform {
	case "onnx":
		relativeAlias = path.Join("onnx", item.Model+".onnx")
	case "denglin":
		relativeAlias = path.Join("denglin", item.Model+".engine")
	case "nvidia":
		if item.HardwareProfile == "" {
			return "", "", ManifestArtifact{}, fmt.Errorf("NVIDIA 模型 %s 缺少 hardware_profile", item.Model)
		}
		// TensorRT reads /data/models/plan/<model>.plan. Hardware profiles
		// select the stored artifact, not the path exposed to inference.
		relativeAlias = path.Join("plan", item.Model+".plan")
	default:
		return "", "", ManifestArtifact{}, fmt.Errorf("不支持的模型平台: %s", item.Platform)
	}
	prefix := path.Dir(relativeAlias) + "/"
	storagePrefix := prefix
	if item.Platform == "nvidia" {
		storagePrefix = path.Join("plan", item.HardwareProfile) + "/"
	}
	if !strings.HasPrefix(artifact.TargetPath, storagePrefix) {
		return "", "", ManifestArtifact{}, fmt.Errorf("模型 %s 的运行时文件不在 %s 下", item.Model, path.Dir(relativeAlias))
	}
	linkTarget = strings.TrimPrefix(artifact.TargetPath, prefix)
	if !safeRelativePath(linkTarget) {
		return "", "", ManifestArtifact{}, fmt.Errorf("模型 %s 的运行时链接目标无效", item.Model)
	}
	return relativeAlias, linkTarget, artifact, nil
}

func verifyRuntimeAlias(ctx context.Context, files remoteFiles, item ReleaseItem, hashes bool) bool {
	relativeAlias, _, artifact, err := runtimeAlias(item)
	if err != nil {
		return false
	}
	alias := path.Join(files.root, relativeAlias)
	if hashes {
		return verifyRemoteArtifactFollowingSymlink(ctx, files, alias, artifact)
	}
	command := "test -f " + shellQuote(alias) + " && [ \"$(stat -L -c %s " + shellQuote(alias) + ")\" = " + shellQuote(strconv.FormatInt(artifact.Size, 10)) + " ]"
	_, err = files.run(ctx, command)
	return err == nil
}

func releaseItemFromCurrent(current CurrentVersion) ReleaseItem {
	return ReleaseItem{
		SchemaVersion:   current.SchemaVersion,
		Model:           current.Model,
		Version:         current.Version,
		FormatID:        current.FormatID,
		ManifestDigest:  current.ManifestDigest,
		Platform:        current.Platform,
		HardwareProfile: current.HardwareProfile,
		RepositoryPath:  current.RepositoryPath,
		Artifacts:       current.Artifacts,
	}
}

func verifyStoredArtifact(ctx context.Context, files remoteFiles, storagePath string, artifact ManifestArtifact, hashes bool) bool {
	if !safeStoragePath(storagePath) {
		return false
	}
	stored := path.Join(files.root, storagePath)
	if hashes {
		return verifyRemoteArtifact(ctx, files, stored, artifact)
	}
	_, err := files.run(ctx, "test -f "+shellQuote(stored)+" && [ \"$(stat -c %s "+shellQuote(stored)+")\" = "+shellQuote(strconv.FormatInt(artifact.Size, 10))+" ]")
	return err == nil
}

func safeStoragePath(storagePath string) bool {
	const prefix = ".center-manager/repositories/"
	return strings.HasPrefix(storagePath, prefix) && safeRelativePath(strings.TrimPrefix(storagePath, prefix))
}

func currentVersionFilesReady(ctx context.Context, files remoteFiles, current CurrentVersion, item ReleaseItem, hashes bool) bool {
	if len(item.Artifacts) != 1 {
		return false
	}
	if current.StoragePath == "" {
		return current.TargetPath == current.RepositoryPath &&
			verifyRepository(ctx, files, current.RepositoryPath, current.Artifacts, hashes) &&
			repositoryLinkPortable(ctx, files, item) && verifyRuntimeAlias(ctx, files, item, hashes)
	}
	alias, _, _, err := runtimeAlias(item)
	return err == nil && current.TargetPath == alias &&
		verifyStoredArtifact(ctx, files, current.StoragePath, item.Artifacts[0], hashes) &&
		verifyRuntimeAlias(ctx, files, item, hashes)
}

func runtimeAliasCommand(alias, linkTarget, backup string) string {
	return "set -e; umask 022; alias_path=" + shellQuote(alias) + "; link_target=" + shellQuote(linkTarget) + "; backup=" + shellQuote(backup) + `; alias_tmp="$alias_path.center-manager.tmp";
mkdir -p "$(dirname "$alias_path")" "$(dirname "$backup")"
rm -f "$alias_tmp"
ln -s "$link_target" "$alias_tmp"
if [ -e "$alias_path" ] && [ ! -L "$alias_path" ]; then mv -T "$alias_path" "$backup"; fi
mv -Tf "$alias_tmp" "$alias_path"`
}

func ensureRuntimeAliasToStorage(ctx context.Context, files remoteFiles, item ReleaseItem, storagePath string, r Reporter) error {
	relativeAlias, _, artifact, err := runtimeAlias(item)
	if err != nil {
		return err
	}
	if !verifyStoredArtifact(ctx, files, storagePath, artifact, true) {
		return fmt.Errorf("内部版本文件 %s 不存在或校验失败", storagePath)
	}
	alias := path.Join(files.root, relativeAlias)
	stored := path.Join(files.root, storagePath)
	linkTarget := relativeSymlinkTarget(path.Dir(alias), stored)
	output, readErr := files.run(ctx, "if [ -L "+shellQuote(alias)+" ]; then readlink "+shellQuote(alias)+"; fi")
	if readErr == nil && strings.TrimSpace(string(output)) == linkTarget && verifyRemoteArtifactFollowingSymlink(ctx, files, alias, artifact) {
		return nil
	}
	if readErr == nil && strings.TrimSpace(string(output)) == "" && verifyRemoteArtifact(ctx, files, alias, artifact) {
		return nil
	}
	backup := path.Join(files.root, ".center-manager", "legacy-backups", item.Model+"-runtime-"+fmt.Sprint(time.Now().UnixNano()))
	if _, err = files.run(ctx, releasePathGuard(files.root, alias, backup)); err != nil {
		return fmt.Errorf("扁平运行时模型路径包含不安全符号链接: %w", err)
	}
	if _, err = files.run(ctx, runtimeAliasCommand(alias, linkTarget, backup)); err != nil {
		return fmt.Errorf("创建扁平运行时模型路径 %s 失败: %w", alias, err)
	}
	if !verifyRemoteArtifactFollowingSymlink(ctx, files, alias, artifact) {
		return fmt.Errorf("扁平运行时模型路径 %s 校验失败", alias)
	}
	r.Logf("已更新扁平运行时模型路径：%s -> %s", alias, linkTarget)
	return nil
}

// Repair the old profile-qualified NVIDIA alias and its persisted state together.
func repairStoredCurrentVersion(ctx context.Context, files remoteFiles, current CurrentVersion, item ReleaseItem, r Reporter) (CurrentVersion, error) {
	alias, _, artifact, err := runtimeAlias(item)
	if err != nil {
		return current, err
	}
	if current.TargetPath == alias {
		return current, ensureRuntimeAliasToStorage(ctx, files, item, current.StoragePath, r)
	}
	if !verifyStoredArtifact(ctx, files, current.StoragePath, artifact, true) {
		return current, fmt.Errorf("内部版本文件 %s 不存在或校验失败", current.StoragePath)
	}
	migrated := current
	migrated.TargetPath = alias
	data, err := json.Marshal(migrated)
	if err != nil {
		return current, err
	}
	alias = path.Join(files.root, alias)
	state := releaseStatePath(files.root, item.Model)
	backup := path.Join(files.root, ".center-manager", "legacy-backups", item.Model+"-runtime-"+fmt.Sprint(time.Now().UnixNano()))
	if _, err = files.run(ctx, releasePathGuard(files.root, alias, state, state+".tmp", backup)); err != nil {
		return current, err
	}
	if _, err = files.run(ctx, runtimeActivationCommand(alias, path.Join(files.root, current.StoragePath), state, backup, string(data))); err != nil {
		return current, err
	}
	if !currentVersionFilesReady(ctx, files, migrated, item, true) {
		return current, fmt.Errorf("模型 %s 运行时路径迁移后校验失败", item.Model)
	}
	r.Logf("已修复 NVIDIA 模型运行时路径：%s", alias)
	return migrated, nil
}

func runtimeActivationCommand(alias, stored, statePath, backup, stateJSON string) string {
	linkTarget := relativeSymlinkTarget(path.Dir(alias), stored)
	return "set -e; umask 022; alias_path=" + shellQuote(alias) + "; stored=" + shellQuote(stored) + "; link_target=" + shellQuote(linkTarget) + "; state=" + shellQuote(statePath) + "; backup=" + shellQuote(backup) + `; link_tmp="$alias_path.center-manager.tmp"; state_tmp="$state.tmp";
mkdir -p "$(dirname "$alias_path")" "$(dirname "$state")" "$(dirname "$backup")"
test -f "$stored"
test ! -L "$state_tmp"
printf %s ` + shellQuote(stateJSON) + ` > "$state_tmp"
old_link=''; had_link=0; moved_legacy=0; switched=0
if [ -L "$alias_path" ]; then old_link=$(readlink "$alias_path"); had_link=1; fi
rollback() {
  result=$?
  trap - EXIT
  if [ "$result" -ne 0 ]; then
    if [ "$had_link" -eq 1 ] && [ "$switched" -eq 1 ]; then
      rm -f "$link_tmp"; ln -s "$old_link" "$link_tmp" && mv -Tf "$link_tmp" "$alias_path"
    elif [ "$moved_legacy" -eq 1 ]; then
      if [ "$switched" -eq 1 ]; then rm -f "$alias_path"; fi
      mv -T "$backup" "$alias_path"
    elif [ "$switched" -eq 1 ]; then rm -f "$alias_path"; fi
  fi
  rm -f "$link_tmp" "$state_tmp"
  exit "$result"
}
trap rollback EXIT
rm -f "$link_tmp"
ln -s "$link_target" "$link_tmp"
if [ -e "$alias_path" ] && [ ! -L "$alias_path" ]; then mv -T "$alias_path" "$backup"; moved_legacy=1; fi
mv -Tf "$link_tmp" "$alias_path"; switched=1
mv -Tf "$state_tmp" "$state"
trap - EXIT`
}

func relativeSymlinkTarget(fromDirectory, target string) string {
	segments := func(value string) []string {
		value = strings.Trim(path.Clean(value), "/")
		if value == "" {
			return nil
		}
		return strings.Split(value, "/")
	}
	from := segments(fromDirectory)
	to := segments(target)
	common := 0
	for common < len(from) && common < len(to) && from[common] == to[common] {
		common++
	}
	parts := make([]string, 0, len(from)-common+len(to)-common)
	for index := common; index < len(from); index++ {
		parts = append(parts, "..")
	}
	parts = append(parts, to[common:]...)
	if len(parts) == 0 {
		return "."
	}
	return path.Join(parts...)
}

func repositoryLinkPortable(ctx context.Context, files remoteFiles, item ReleaseItem) bool {
	target := path.Join(files.root, item.RepositoryPath)
	command := "if [ -L " + shellQuote(target) + " ]; then link_target=$(readlink " + shellQuote(target) + "); case \"$link_target\" in /*) exit 1;; esac; fi"
	_, err := files.run(ctx, command)
	return err == nil
}

func retireLegacyRepositoryPath(ctx context.Context, files remoteFiles, item ReleaseItem, r Reporter) error {
	relativeAlias, _, _, err := runtimeAlias(item)
	if err != nil {
		return err
	}
	target := path.Join(files.root, item.RepositoryPath)
	alias := path.Join(files.root, relativeAlias)
	if target == alias || target == path.Dir(alias) || strings.HasPrefix(alias, target+"/") {
		return fmt.Errorf("旧模型目录 %s 与扁平运行时路径 %s 重叠", target, alias)
	}
	backup := path.Join(files.root, ".center-manager", "legacy-backups", item.Model+"-repository-"+fmt.Sprint(time.Now().UnixNano()))
	if _, err = files.run(ctx, releasePathGuard(files.root, target, backup)); err != nil {
		return fmt.Errorf("旧模型目录包含不安全符号链接: %w", err)
	}
	command := "set -e; target=" + shellQuote(target) + "; backup=" + shellQuote(backup) + `;
if [ -L "$target" ]; then
  rm -f "$target"
elif [ -e "$target" ]; then
  mkdir -p "$(dirname "$backup")"
  mv -T "$target" "$backup"
fi`
	if _, err = files.run(ctx, command); err != nil {
		return fmt.Errorf("清理旧模型目录 %s 失败: %w", target, err)
	}
	if _, err = files.run(ctx, "test ! -e "+shellQuote(target)+" && test ! -L "+shellQuote(target)); err != nil {
		return fmt.Errorf("旧模型目录 %s 清理后仍然存在", target)
	}
	r.Logf("已移除公开目录式模型路径：%s", target)
	return nil
}

func migrateLegacyCurrentVersion(ctx context.Context, files remoteFiles, current CurrentVersion, item ReleaseItem, r Reporter) (CurrentVersion, error) {
	relativeAlias, _, artifact, err := runtimeAlias(item)
	if err != nil {
		return current, err
	}
	legacySource := path.Join(files.root, artifact.TargetPath)
	if !verifyRemoteArtifact(ctx, files, legacySource, artifact) {
		return current, fmt.Errorf("旧模型文件 %s 不存在或校验失败", legacySource)
	}
	stage := path.Join(files.root, ".center-manager", "repositories", item.Model, "migrated-"+item.ManifestDigest, "artifact")
	stored, err := repositoryStageArtifactPath(stage, item.RepositoryPath, artifact.TargetPath)
	if err != nil {
		return current, err
	}
	if !verifyRemoteArtifact(ctx, files, stored, artifact) {
		temp := stored + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
		if _, err = files.run(ctx, releasePathGuard(files.root, stored, temp)); err != nil {
			return current, fmt.Errorf("迁移模型文件路径包含不安全符号链接: %w", err)
		}
		command := "set -e; umask 022; mkdir -p " + shellQuote(path.Dir(stored)) + "; rm -f " + shellQuote(temp) + "; cp " + shellQuote(legacySource) + " " + shellQuote(temp) + "; chmod 0644 " + shellQuote(temp) + "; mv -Tf " + shellQuote(temp) + " " + shellQuote(stored)
		if _, err = files.run(ctx, command); err != nil {
			return current, fmt.Errorf("迁移旧模型文件 %s 失败: %w", legacySource, err)
		}
	}
	if !verifyRemoteArtifact(ctx, files, stored, artifact) {
		return current, fmt.Errorf("迁移后的内部模型文件 %s 校验失败", stored)
	}
	storagePath := strings.TrimPrefix(stored, files.root+"/")
	migrated := current
	migrated.SchemaVersion = manifestSchemaVersion
	migrated.Model = item.Model
	migrated.Version = item.Version
	migrated.FormatID = item.FormatID
	migrated.Platform = item.Platform
	migrated.HardwareProfile = item.HardwareProfile
	migrated.ManifestDigest = item.ManifestDigest
	migrated.RepositoryPath = item.RepositoryPath
	migrated.Artifacts = item.Artifacts
	migrated.TargetPath = relativeAlias
	migrated.StoragePath = storagePath
	migrated.ArtifactSize = artifactBytes(item.Artifacts)
	if migrated.ActivatedAt == "" {
		migrated.ActivatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(migrated)
	if err != nil {
		return current, err
	}
	alias := path.Join(files.root, relativeAlias)
	statePath := releaseStatePath(files.root, item.Model)
	backup := path.Join(files.root, ".center-manager", "legacy-backups", item.Model+"-runtime-"+fmt.Sprint(time.Now().UnixNano()))
	if _, err = files.run(ctx, releasePathGuard(files.root, alias, statePath, statePath+".tmp", backup)); err != nil {
		return current, err
	}
	if _, err = files.run(ctx, runtimeActivationCommand(alias, stored, statePath, backup, string(data))); err != nil {
		return current, fmt.Errorf("激活迁移后的扁平模型 %s 失败: %w", item.Model, err)
	}
	if err = retireLegacyRepositoryPath(ctx, files, item, r); err != nil {
		return current, err
	}
	if !currentVersionFilesReady(ctx, files, migrated, item, true) {
		return current, fmt.Errorf("迁移后的扁平模型 %s 校验失败", item.Model)
	}
	r.Logf("已将旧目录式模型迁移到扁平路径：%s", alias)
	return migrated, nil
}

type artifactSigner interface {
	SignURL(string, aliyunoss.HTTPMethod, int64, ...aliyunoss.Option) (string, error)
}

func releaseRepository(ctx context.Context, files remoteFiles, bucket artifactSigner, item ReleaseItem, current CurrentVersion, base, total int64, r Reporter) error {
	relativeAlias, _, runtimeArtifact, err := runtimeAlias(item)
	if err != nil {
		return err
	}
	if current.ManifestDigest == item.ManifestDigest {
		if current.StoragePath != "" && verifyStoredArtifact(ctx, files, current.StoragePath, runtimeArtifact, true) {
			if _, err = repairStoredCurrentVersion(ctx, files, current, item, r); err != nil {
				return err
			}
			r.Logf("模型 %s 已是目标版本 %s，扁平运行时文件校验通过，跳过", item.Model, item.Version)
			return nil
		}
		if current.StoragePath == "" && verifyRepository(ctx, files, item.RepositoryPath, item.Artifacts, true) {
			if _, err = migrateLegacyCurrentVersion(ctx, files, current, item, r); err != nil {
				return err
			}
			r.Logf("模型 %s 已是目标版本 %s，已迁移为扁平运行时文件", item.Model, item.Version)
			return nil
		}
	}
	// A fresh staging tree never modifies the repository currently used by inference.
	stage := path.Join(files.root, ".center-manager", "repositories", item.Model, item.Version+"-"+item.ManifestDigest, fmt.Sprint(time.Now().UnixNano()), "artifact")
	var completed int64
	var runtimeStorage string
	for index, artifact := range item.Artifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		blob := path.Join(files.root, ".center-manager", "blobs", artifact.SHA256)
		part := path.Join(files.root, ".center-manager", "downloads", artifact.SHA256+".part")
		destination, err := repositoryStageArtifactPath(stage, item.RepositoryPath, artifact.TargetPath)
		if err != nil {
			return err
		}
		if _, err := files.run(ctx, releasePathGuard(files.root, blob, part, destination)); err != nil {
			return fmt.Errorf("仓库文件路径包含不安全符号链接: %w", err)
		}
		if _, err := files.run(ctx, "umask 022; mkdir -p "+shellQuote(path.Dir(blob))+" "+shellQuote(path.Dir(part))+" "+shellQuote(path.Dir(destination))); err != nil {
			return err
		}
		if !verifyRemoteArtifact(ctx, files, blob, artifact) {
			// Existing matching files can seed the content cache during migration.
			active := path.Join(files.root, artifact.TargetPath)
			if verifyRemoteArtifact(ctx, files, active, artifact) {
				if _, err := files.run(ctx, "cp "+shellQuote(active)+" "+shellQuote(part)); err != nil {
					return err
				}
			}
			if !verifyRemoteArtifact(ctx, files, part, artifact) {
				if files.fileSize(ctx, part) >= artifact.Size {
					if _, err := files.run(ctx, "rm -f "+shellQuote(part)); err != nil {
						return err
					}
				}
				url, err := bucket.SignURL(path.Join(path.Dir(item.ManifestKey), artifact.Path), aliyunoss.HTTPGet, 2*60*60)
				if err != nil {
					return err
				}
				r.Logf("[%d/%d] 下载 %s", index+1, len(item.Artifacts), artifact.Path)
				if err = runDownload(ctx, files, downloadCommand(part, url), url, part, artifact.Path, base+completed, total, r); err != nil {
					return err
				}
			}
			if !verifyRemoteArtifact(ctx, files, part, artifact) {
				return fmt.Errorf("文件 %s 的 SHA-256 或大小校验失败，当前仓库版本未切换", artifact.Path)
			}
			if _, err := files.run(ctx, "chmod 0644 "+shellQuote(part)+" && mv -T "+shellQuote(part)+" "+shellQuote(blob)); err != nil {
				return err
			}
		} else {
			r.Logf("[%d/%d] 复用已校验文件：%s", index+1, len(item.Artifacts), artifact.Path)
		}
		if _, err := files.run(ctx, "cp "+shellQuote(blob)+" "+shellQuote(destination)+" && chmod 0644 "+shellQuote(destination)); err != nil {
			return err
		}
		if !verifyRemoteArtifact(ctx, files, destination, artifact) {
			return fmt.Errorf("暂存文件 %s 校验失败，当前仓库版本未切换", artifact.Path)
		}
		if artifact.TargetPath == runtimeArtifact.TargetPath {
			runtimeStorage = destination
		}
		completed += artifact.Size
		progress(r, base+completed, total)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtimeStorage == "" || !strings.HasPrefix(runtimeStorage, files.root+"/") {
		return fmt.Errorf("模型 %s 的内部版本文件路径无效", item.Model)
	}
	storagePath := strings.TrimPrefix(runtimeStorage, files.root+"/")
	state := CurrentVersion{SchemaVersion: manifestSchemaVersion, Model: item.Model, Version: item.Version, FormatID: item.FormatID, Platform: item.Platform, HardwareProfile: item.HardwareProfile,
		ManifestDigest: item.ManifestDigest, TargetPath: relativeAlias, RepositoryPath: item.RepositoryPath, StoragePath: storagePath, Artifacts: item.Artifacts,
		ArtifactSize: artifactBytes(item.Artifacts), ActivatedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	alias, statePath := path.Join(files.root, relativeAlias), releaseStatePath(files.root, item.Model)
	backup := path.Join(files.root, ".center-manager", "legacy-backups", item.Model+"-"+fmt.Sprint(time.Now().UnixNano()))
	if _, err = files.run(ctx, releasePathGuard(files.root, stage, statePath, statePath+".tmp", alias, backup)); err != nil {
		return err
	}
	if _, err = files.run(ctx, runtimeActivationCommand(alias, runtimeStorage, statePath, backup, string(data))); err != nil {
		return fmt.Errorf("激活扁平模型 %s:%s 失败: %w", item.Model, item.Version, err)
	}
	if err = retireLegacyRepositoryPath(ctx, files, item, r); err != nil {
		return err
	}
	if !currentVersionFilesReady(ctx, files, state, item, true) {
		return fmt.Errorf("扁平模型路径 %s 激活后校验失败", alias)
	}
	r.Logf("已发布模型 %s:%s 到扁平运行时路径 %s", item.Model, item.Version, alias)
	return nil
}

func repositoryStageArtifactPath(stage, repository, target string) (string, error) {
	if !safeRelativePath(repository) || !safeRelativePath(target) {
		return "", fmt.Errorf("模型仓库或目标文件路径无效")
	}
	prefix := strings.TrimSuffix(repository, "/") + "/"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("目标文件 %s 不在模型仓库 %s 下", target, repository)
	}
	relative := strings.TrimPrefix(target, prefix)
	if !safeRelativePath(relative) {
		return "", fmt.Errorf("模型仓库内目标文件路径无效: %s", target)
	}
	return path.Join(stage, relative), nil
}
