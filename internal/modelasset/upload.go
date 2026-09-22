package modelasset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/google/uuid"
)

const (
	maxUploadFileSize = int64(100 << 30)
	maxUploadTotal    = int64(500 << 30)
)

type ModelUploadFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	UploadURL string `json:"upload_url,omitempty"`
}

type ModelUploadFormat struct {
	ID              string            `json:"id"`
	Platform        string            `json:"platform"`
	HardwareProfile string            `json:"hardware_profile,omitempty"`
	Files           []ModelUploadFile `json:"files"`
}

type ModelUpload struct {
	UploadID string              `json:"upload_id"`
	Model    string              `json:"model"`
	Version  string              `json:"version"`
	Formats  []ModelUploadFormat `json:"formats"`
}

func (s *Service) CreateModelUpload(input ModelUpload) (ModelUpload, error) {
	input.UploadID = uuid.NewString()
	manifest, normalized, err := normalizeModelUpload(input)
	if err != nil {
		return ModelUpload{}, err
	}
	_, bucket, config, err := s.client()
	if err != nil {
		return ModelUpload{}, err
	}
	manifestKey := path.Join(config.ModelsPrefix, manifest.Model, manifest.Version, "manifest.json")
	exists, err := bucket.IsObjectExist(manifestKey)
	if err != nil {
		return ModelUpload{}, fmt.Errorf("检查模型版本是否存在: %w", err)
	}
	if exists {
		return ModelUpload{}, fmt.Errorf("模型版本已存在且不可覆盖: %s:%s", manifest.Model, manifest.Version)
	}
	for formatIndex := range normalized.Formats {
		format := &normalized.Formats[formatIndex]
		for fileIndex := range format.Files {
			file := &format.Files[fileIndex]
			key := uploadTemporaryKey(config.ModelsPrefix, normalized.UploadID, format.ID, file.Path)
			file.UploadURL, err = bucket.SignURL(key, aliyunoss.HTTPPut, 2*60*60)
			if err != nil {
				return ModelUpload{}, fmt.Errorf("生成 %s/%s 上传地址: %w", format.ID, file.Path, err)
			}
		}
	}
	return normalized, nil
}

func (s *Service) UploadModelFile(uploadID, formatID, relativePath string, size int64, reader io.Reader) error {
	if _, err := uuid.Parse(uploadID); err != nil {
		return fmt.Errorf("upload_id 无效")
	}
	if !manifestNamePattern.MatchString(formatID) || !validUploadRelativePath(relativePath) {
		return fmt.Errorf("上传文件路径无效")
	}
	if size <= 0 || size > maxUploadFileSize {
		return fmt.Errorf("上传文件大小无效")
	}
	_, bucket, config, err := s.client()
	if err != nil {
		return err
	}
	// The API handler has already verified that the request Content-Length equals
	// the declared size. Keep the OSS reader length identical: the OSS SDK derives
	// its outbound Content-Length from LimitedReader.N.
	limited := modelUploadReader(reader, size)
	key := uploadTemporaryKey(config.ModelsPrefix, uploadID, formatID, relativePath)
	if err = bucket.PutObject(key, limited, aliyunoss.ContentLength(size)); err != nil {
		return fmt.Errorf("上传模型文件: %w", err)
	}
	if limited.N != 0 {
		_ = bucket.DeleteObject(key)
		return fmt.Errorf("上传内容小于声明大小")
	}
	return nil
}

func modelUploadReader(reader io.Reader, size int64) *io.LimitedReader {
	return &io.LimitedReader{R: reader, N: size}
}

func (s *Service) AbortModelUpload(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil {
		return fmt.Errorf("upload_id 无效")
	}
	_, bucket, config, err := s.client()
	if err != nil {
		return err
	}
	prefix := path.Join(config.ModelsPrefix, ".uploads", uploadID) + "/"
	marker := ""
	for {
		result, listErr := bucket.ListObjects(aliyunoss.Prefix(prefix), aliyunoss.Marker(marker), aliyunoss.MaxKeys(1000))
		if listErr != nil {
			return fmt.Errorf("列举上传临时文件: %w", listErr)
		}
		keys := make([]string, 0, len(result.Objects))
		for _, object := range result.Objects {
			keys = append(keys, object.Key)
		}
		if len(keys) > 0 {
			if _, deleteErr := bucket.DeleteObjects(keys); deleteErr != nil {
				return fmt.Errorf("清理上传临时文件: %w", deleteErr)
			}
		}
		if !result.IsTruncated {
			return nil
		}
		marker = result.NextMarker
	}
}

func (s *Service) FinalizeModelUpload(input ModelUpload) (Version, error) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if _, err := uuid.Parse(input.UploadID); err != nil {
		return Version{}, fmt.Errorf("upload_id 无效")
	}
	manifest, normalized, err := normalizeModelUpload(input)
	if err != nil {
		return Version{}, err
	}
	_, bucket, config, err := s.client()
	if err != nil {
		return Version{}, err
	}
	baseKey := path.Join(config.ModelsPrefix, manifest.Model, manifest.Version)
	manifestKey := path.Join(baseKey, "manifest.json")
	exists, err := bucket.IsObjectExist(manifestKey)
	if err != nil {
		return Version{}, fmt.Errorf("检查模型版本是否存在: %w", err)
	}
	if exists {
		return Version{}, fmt.Errorf("模型版本已存在且不可覆盖: %s:%s", manifest.Model, manifest.Version)
	}
	temporaryKeys := make([]string, 0)
	for formatIndex := range manifest.Formats {
		format := &manifest.Formats[formatIndex]
		files := normalized.Formats[formatIndex].Files
		for fileIndex := range format.Artifacts {
			artifact := &format.Artifacts[fileIndex]
			relative := files[fileIndex].Path
			temporaryKey := uploadTemporaryKey(config.ModelsPrefix, input.UploadID, format.ID, relative)
			body, getErr := bucket.GetObject(temporaryKey)
			if getErr != nil {
				return Version{}, fmt.Errorf("上传文件不存在 %s/%s: %w", format.ID, relative, getErr)
			}
			hash := sha256.New()
			actualSize, copyErr := io.Copy(hash, body)
			closeErr := body.Close()
			if copyErr != nil {
				return Version{}, fmt.Errorf("校验上传文件 %s/%s: %w", format.ID, relative, copyErr)
			}
			if closeErr != nil {
				return Version{}, closeErr
			}
			if actualSize != artifact.Size {
				return Version{}, fmt.Errorf("文件 %s/%s 大小不一致：声明 %d，实际 %d", format.ID, relative, artifact.Size, actualSize)
			}
			artifact.SHA256 = hex.EncodeToString(hash.Sum(nil))
			finalKey := path.Join(baseKey, artifact.Path)
			if artifact.Size > 1024*1024*1024 {
				copyErr = bucket.CopyFile(config.Bucket, temporaryKey, finalKey, 256*1024*1024, aliyunoss.Routines(3))
			} else {
				_, copyErr = bucket.CopyObject(temporaryKey, finalKey)
			}
			if copyErr != nil {
				return Version{}, fmt.Errorf("固化模型文件 %s/%s: %w", format.ID, relative, copyErr)
			}
			temporaryKeys = append(temporaryKeys, temporaryKey)
		}
	}
	manifest.CreatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Version{}, err
	}
	data = append(data, '\n')
	if err = bucket.PutObject(manifestKey, bytes.NewReader(data)); err != nil {
		return Version{}, fmt.Errorf("发布模型 Manifest: %w", err)
	}
	if len(temporaryKeys) > 0 {
		_, _ = bucket.DeleteObjects(temporaryKeys)
	}
	digest := sha256.Sum256(data)
	return Version{SchemaVersion: manifest.SchemaVersion, ManifestKey: manifestKey, ManifestDigest: hex.EncodeToString(digest[:]), Model: manifest.Model, Version: manifest.Version, CreatedAt: manifest.CreatedAt, Formats: manifest.Formats}, nil
}

func normalizeModelUpload(input ModelUpload) (Manifest, ModelUpload, error) {
	input.Model = strings.TrimSpace(input.Model)
	input.Version = strings.TrimSpace(input.Version)
	if !manifestNamePattern.MatchString(input.Model) {
		return Manifest{}, ModelUpload{}, fmt.Errorf("model 只能包含字母、数字、点、下划线和短横线")
	}
	if !manifestNamePattern.MatchString(input.Version) {
		return Manifest{}, ModelUpload{}, fmt.Errorf("version 只能包含字母、数字、点、下划线和短横线")
	}
	if len(input.Formats) == 0 {
		return Manifest{}, ModelUpload{}, fmt.Errorf("请至少上传一种模型格式")
	}
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Model: input.Model, Version: input.Version}
	seenFormats := map[string]bool{}
	var total int64
	fileCount := 0
	for formatIndex := range input.Formats {
		format := &input.Formats[formatIndex]
		format.ID = strings.TrimSpace(format.ID)
		format.Platform = strings.TrimSpace(format.Platform)
		format.HardwareProfile = strings.TrimSpace(format.HardwareProfile)
		if !manifestNamePattern.MatchString(format.ID) || seenFormats[format.ID] {
			return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 ID 无效或重复: %s", format.ID)
		}
		seenFormats[format.ID] = true
		repositoryPath, expectedExtension, err := uploadFormatTarget(input.Model, format.Platform, format.HardwareProfile)
		if err != nil {
			return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 %s: %w", format.ID, err)
		}
		if len(format.Files) != 1 {
			return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 %s 只能包含一个模型文件", format.ID)
		}
		seenFiles := map[string]bool{}
		artifacts := make([]ManifestArtifact, 0, len(format.Files))
		for fileIndex := range format.Files {
			fileCount++
			if fileCount > 1000 {
				return Manifest{}, ModelUpload{}, fmt.Errorf("单个模型版本最多包含 1000 个文件")
			}
			file := &format.Files[fileIndex]
			file.Path = strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/"))
			file.UploadURL = ""
			if !validUploadRelativePath(file.Path) || seenFiles[file.Path] {
				return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 %s 文件路径无效或重复: %s", format.ID, file.Path)
			}
			if path.Base(file.Path) != file.Path {
				return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 %s 的模型文件不能包含目录: %s", format.ID, file.Path)
			}
			if !strings.EqualFold(path.Ext(file.Path), expectedExtension) {
				return Manifest{}, ModelUpload{}, fmt.Errorf("模型格式 %s 必须上传 %s 文件", format.ID, expectedExtension)
			}
			seenFiles[file.Path] = true
			if file.Size <= 0 || file.Size > maxUploadFileSize || total > maxUploadTotal-file.Size {
				return Manifest{}, ModelUpload{}, fmt.Errorf("文件 %s/%s 大小无效或上传总量超限", format.ID, file.Path)
			}
			total += file.Size
			artifactPath := path.Join("artifact", format.ID, file.Path)
			artifacts = append(artifacts, ManifestArtifact{Path: artifactPath, TargetPath: path.Join(repositoryPath, file.Path), Size: file.Size, SHA256: strings.Repeat("0", 64)})
		}
		sort.SliceStable(format.Files, func(i, j int) bool { return format.Files[i].Path < format.Files[j].Path })
		sort.SliceStable(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
		manifest.Formats = append(manifest.Formats, ManifestFormat{ID: format.ID, Platform: format.Platform, HardwareProfile: format.HardwareProfile, RepositoryPath: repositoryPath, Artifacts: artifacts})
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, ModelUpload{}, err
	}
	return manifest, input, nil
}

func uploadFormatTarget(model, platform, hardwareProfile string) (string, string, error) {
	switch platform {
	case "onnx":
		if hardwareProfile != "" {
			return "", "", fmt.Errorf("ONNX 不能设置 hardware_profile")
		}
		return path.Join("onnx", model), ".onnx", nil
	case "denglin":
		if hardwareProfile != "" {
			return "", "", fmt.Errorf("登临不能设置 hardware_profile")
		}
		return path.Join("denglin", model), ".engine", nil
	case "nvidia":
		if !manifestNamePattern.MatchString(hardwareProfile) {
			return "", "", fmt.Errorf("NVIDIA 必须设置有效的 hardware_profile")
		}
		return path.Join("plan", hardwareProfile, model), ".plan", nil
	default:
		return "", "", fmt.Errorf("platform 必须是 onnx、nvidia 或 denglin")
	}
}

func validUploadRelativePath(value string) bool {
	return safeRelativePath(value) && !strings.HasPrefix(value, "artifact/") && path.Clean(value) == value
}

func uploadTemporaryKey(prefix, uploadID, formatID, relativePath string) string {
	return path.Join(prefix, ".uploads", uploadID, formatID, relativePath)
}
