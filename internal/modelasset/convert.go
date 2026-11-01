package modelasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/sshx"
	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// 转换约定：构建机上工作目录挂载为容器 /work，源模型固定为 /work/source.onnx，
// 产物必须写入 /work/output.plan（nvidia）或 /work/output.engine（denglin）。
const convertInputFile = "source.onnx"

func convertOutputFile(platform string) string {
	if platform == "nvidia" {
		return "output.plan"
	}
	return "output.engine"
}

func (s *Service) ConvertSettings() (domain.ModelConvertSettings, error) {
	return s.store.GetModelConvertSettings()
}

func (s *Service) SaveConvertSettings(v domain.ModelConvertSettings) error {
	return s.store.SaveModelConvertSettings(v)
}

// ConvertVersion 在指定平台的构建机上把模型版本的 ONNX 文件转换为 TensorRT plan
// 或登临 engine，并作为新模型版本（版本号自动派生）发布到模型仓库。
// 源文件与产物都经 OSS 签名 URL 在构建机上直传直下，不经过管理端。
func (s *Service) ConvertVersion(ctx context.Context, platform, manifestKey, manifestDigest string, r Reporter) (Version, error) {
	if platform != "nvidia" && platform != "denglin" {
		return Version{}, fmt.Errorf("platform 必须是 nvidia 或 denglin")
	}
	settings, err := s.ConvertSettings()
	if err != nil {
		return Version{}, err
	}
	conf := settings.Denglin
	if platform == "nvidia" {
		conf = settings.Nvidia
	}
	if !conf.Configured() {
		return Version{}, fmt.Errorf("%s 平台转换未配置（构建机、镜像、命令），请先在模型仓库页保存转换设置", platform)
	}
	source, onnx, err := s.convertSource(manifestKey, manifestDigest)
	if err != nil {
		return Version{}, err
	}
	release, err := centerlock.AcquireContext(ctx, conf.CenterID)
	if err != nil {
		return Version{}, err
	}
	defer release()
	cfg, _, stored, err := s.centers.Connection(conf.CenterID)
	if err != nil {
		return Version{}, fmt.Errorf("连接构建机失败: %w", err)
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		return Version{}, fmt.Errorf("连接构建机失败: %w", err)
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)

	hardwareProfile := ""
	if platform == "nvidia" {
		names, gpuErr := readNVIDIAGPUNames(ctx, client)
		if gpuErr != nil {
			return Version{}, fmt.Errorf("无法读取构建机 NVIDIA GPU 型号: %w", gpuErr)
		}
		hardwareProfile = normalizeGPUProfile(names[0])
		if !manifestNamePattern.MatchString(hardwareProfile) {
			return Version{}, fmt.Errorf("构建机 GPU 型号 %q 无法用作 hardware_profile", names[0])
		}
		r.Logf("构建机 GPU 型号：%s（hardware_profile=%s）", names[0], hardwareProfile)
	}

	_, bucket, config, err := s.client()
	if err != nil {
		return Version{}, err
	}
	work := ""
	defer func() {
		if work == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = runSSH(cleanupCtx, client, "rm -rf "+shellQuote(work), nil)
	}()
	r.Message(fmt.Sprintf("正在准备 %s 构建机工作目录", platform))
	if raw, workErr := runSSH(ctx, client, "umask 077; mktemp -d /tmp/cm-convert.XXXXXXXX", nil); workErr != nil {
		return Version{}, fmt.Errorf("创建工作目录失败: %w", workErr)
	} else {
		work = strings.TrimSpace(string(raw))
		if !strings.HasPrefix(work, "/") || strings.ContainsAny(work, " \t\r\n") {
			return Version{}, errors.New("构建机工作目录路径异常")
		}
	}

	sourceArtifact := onnx.Artifacts[0]
	sourceKey := path.Join(config.ModelsPrefix, sourceArtifact.TargetPath)
	sourceURL, err := bucket.SignURL(sourceKey, aliyunoss.HTTPGet, 2*60*60)
	if err != nil {
		return Version{}, fmt.Errorf("生成源模型下载地址失败: %w", err)
	}
	r.Logf("下载源模型 %s（%s）", sourceArtifact.TargetPath, humanSize(sourceArtifact.Size))
	inputPath := path.Join(work, convertInputFile)
	if output, dlErr := runSSH(ctx, client, downloadCommand(inputPath, sourceURL), nil); dlErr != nil {
		return Version{}, fmt.Errorf("下载源模型失败: %s: %w", sanitizeOutput(tail(string(output), 1500), sourceURL), dlErr)
	}
	r.Progress(20)

	r.Message(fmt.Sprintf("正在执行 %s 转换（docker run）", platform))
	outputName := convertOutputFile(platform)
	command := fmt.Sprintf("docker run --rm %s -v %s:/work %s %s", strings.TrimSpace(conf.DockerArgs), shellQuote(work), strings.TrimSpace(conf.Image), strings.TrimSpace(conf.Command))
	// DockerArgs/Image/Command 是管理员保存的受信配置（同 Compose 模板口径），原样执行；
	// 工作目录等动态路径全部经 shellQuote 注入。
	if output, runErr := runSSH(ctx, client, command, nil); runErr != nil {
		return Version{}, fmt.Errorf("转换命令失败: %s: %w", tail(string(output), 4000), runErr)
	} else {
		r.Logf("转换输出（截尾）：\n%s", tail(string(output), 2000))
	}
	r.Progress(70)

	outputPath := path.Join(work, outputName)
	rawStat, statErr := runSSH(ctx, client, "size=$(stat -c %s "+shellQuote(outputPath)+" 2>/dev/null) && sha=$(sha256sum "+shellQuote(outputPath)+" 2>/dev/null | cut -d' ' -f1) && printf '%s %s' \"$size\" \"$sha\"", nil)
	if statErr != nil {
		return Version{}, fmt.Errorf("读取转换产物失败（产物未生成或不可读，约定输出路径为 /work/%s）: %w", outputName, statErr)
	}
	fields := strings.Fields(strings.TrimSpace(string(rawStat)))
	if len(fields) != 2 || !sha256Pattern.MatchString(fields[1]) {
		return Version{}, fmt.Errorf("转换产物校验输出异常: %q", strings.TrimSpace(string(rawStat)))
	}
	size, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || size <= 0 {
		return Version{}, fmt.Errorf("转换产物为空或大小异常: %q", fields[0])
	}
	r.Logf("转换产物 %s（%s，SHA-256 %s…）", outputName, humanSize(size), fields[1][:16])

	newVersion, err := s.deriveConvertVersion(bucket, config, source.Model, source.Version, platform)
	if err != nil {
		return Version{}, err
	}
	repositoryPath, expectedExt, err := uploadFormatTarget(source.Model, platform, hardwareProfile)
	if err != nil {
		return Version{}, err
	}
	// 产物文件名带派生版本号，避免与既有版本的同名 artifact 相互覆盖。
	artifactName := strings.TrimSuffix(path.Base(sourceArtifact.TargetPath), path.Ext(sourceArtifact.TargetPath)) + "-" + newVersion + expectedExt
	finalKey := path.Join(config.ModelsPrefix, repositoryPath, artifactName)
	uploadURL, err := bucket.SignURL(finalKey, aliyunoss.HTTPPut, 2*60*60)
	if err != nil {
		return Version{}, fmt.Errorf("生成产物上传地址失败: %w", err)
	}
	r.Message("正在上传转换产物到模型仓库")
	if output, upErr := runSSH(ctx, client, "curl -fsSL --retry 3 -T "+shellQuote(outputPath)+" "+shellQuote(uploadURL), nil); upErr != nil {
		return Version{}, fmt.Errorf("上传转换产物失败: %s: %w", sanitizeOutput(tail(string(output), 1500), uploadURL), upErr)
	}
	headers, metaErr := bucket.GetObjectMeta(finalKey)
	if metaErr != nil {
		return Version{}, fmt.Errorf("校验上传结果失败: %w", metaErr)
	}
	if remoteSize, parseErr := strconv.ParseInt(headers.Get("Content-Length"), 10, 64); parseErr != nil || remoteSize != size {
		return Version{}, fmt.Errorf("上传产物大小不一致：转换机 %d 字节，OSS %q", size, headers.Get("Content-Length"))
	}
	r.Progress(90)

	// 新版本引用源版本的全部格式（剔除同平台旧格式，由本次转换产物替代），可独立发布到任意中心。
	manifest, err := buildConvertedManifest(source, platform, hardwareProfile, newVersion, artifactName, repositoryPath, size, fields[1])
	if err != nil {
		return Version{}, err
	}
	manifestKeyNew := path.Join(config.ModelsPrefix, manifest.Model, manifest.Version, "manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Version{}, err
	}
	data = append(data, '\n')
	if err = bucket.PutObject(manifestKeyNew, strings.NewReader(string(data))); err != nil {
		return Version{}, fmt.Errorf("发布模型 Manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	version := Version{SchemaVersion: manifest.SchemaVersion, ManifestKey: manifestKeyNew, ManifestDigest: hex.EncodeToString(digest[:]), Model: manifest.Model, Version: manifest.Version, CreatedAt: manifest.CreatedAt, Formats: manifest.Formats}
	r.Logf("已发布模型版本 %s:%s（digest %s）", version.Model, version.Version, version.ManifestDigest[:12])
	r.Progress(100)
	r.Message("模型转换完成")
	return version, nil
}

// buildConvertedManifest 组装派生版本的 Manifest：保留源版本的全部格式（onnx 等，
// 产物文件按原 OSS 位置引用、不重复上传），剔除与目标平台同平台的旧格式避免
// 平台+硬件配置重复，再追加本次转换产物格式。
func buildConvertedManifest(source Version, platform, hardwareProfile, newVersion, artifactName, repositoryPath string, size int64, sha256 string) (Manifest, error) {
	formats := make([]ManifestFormat, 0, len(source.Formats)+1)
	for _, format := range source.Formats {
		if format.Platform != platform {
			formats = append(formats, format)
		}
	}
	formats = append(formats, ManifestFormat{
		ID: platform, Platform: platform, HardwareProfile: hardwareProfile, RepositoryPath: repositoryPath,
		Artifacts: []ManifestArtifact{{
			Path: path.Join("artifact", platform, artifactName), TargetPath: path.Join(repositoryPath, artifactName),
			Size: size, SHA256: sha256,
		}},
	})
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Model: source.Model, Version: newVersion, CreatedAt: time.Now().UTC(), Formats: formats}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, fmt.Errorf("组装模型 Manifest 失败: %w", err)
	}
	return manifest, nil
}

// convertSource 校验源版本存在、摘要一致且包含 ONNX 格式。
func (s *Service) convertSource(manifestKey, manifestDigest string) (Version, ManifestFormat, error) {
	versions, err := s.Versions()
	if err != nil {
		return Version{}, ManifestFormat{}, err
	}
	for _, version := range versions {
		if version.ManifestKey != manifestKey {
			continue
		}
		if manifestDigest == "" || manifestDigest != version.ManifestDigest {
			return Version{}, ManifestFormat{}, fmt.Errorf("模型 %s:%s 的 Manifest 已变化，请刷新后重新转换", version.Model, version.Version)
		}
		for _, format := range version.Formats {
			if format.Platform == "onnx" && len(format.Artifacts) > 0 {
				return version, format, nil
			}
		}
		return Version{}, ManifestFormat{}, fmt.Errorf("模型 %s:%s 不包含 ONNX 格式，无法转换", version.Model, version.Version)
	}
	return Version{}, ManifestFormat{}, fmt.Errorf("模型版本不存在或尚未就绪: %s", manifestKey)
}

// deriveConvertVersion 生成不冲突的派生版本号：{源版本}-{平台}，冲突时追加 -2、-3…
func (s *Service) deriveConvertVersion(bucket *aliyunoss.Bucket, config domain.OSSCredential, model, sourceVersion, platform string) (string, error) {
	base := sourceVersion + "-" + platform
	for index := 1; index <= 99; index++ {
		candidate := base
		if index > 1 {
			candidate = fmt.Sprintf("%s-%d", base, index)
		}
		exists, err := bucket.IsObjectExist(path.Join(config.ModelsPrefix, model, candidate, "manifest.json"))
		if err != nil {
			return "", fmt.Errorf("检查派生版本号冲突: %w", err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("派生版本号 %s-* 已用尽，请整理模型版本", base)
}

// normalizeGPUProfile 把 GPU 型号（如 "NVIDIA Tesla T4"）归一为可作为
// hardware_profile 的形态（小写、非字母数字折叠为短横线），与中心侧型号匹配
// （hardwareProfileMatches 的 normalizeHardwareName 子串匹配）保持兼容。
func normalizeGPUProfile(name string) string {
	var normalized strings.Builder
	hyphen := false
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_':
			normalized.WriteRune(char)
			hyphen = false
		case !hyphen && normalized.Len() > 0:
			normalized.WriteRune('-')
			hyphen = true
		}
	}
	return strings.Trim(normalized.String(), "-")
}
