package modelasset

import (
	"fmt"
	"sort"
	"strings"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func modelFolderPrefix(root, model string) (string, error) {
	if !manifestNamePattern.MatchString(model) {
		return "", fmt.Errorf("模型名称无效")
	}
	return strings.TrimRight(root, "/") + "/" + model + "/", nil
}

// DeleteModel removes repository objects only; deployed center files are retained.
func (s *Service) DeleteModel(model string) error {
	if _, err := modelFolderPrefix("", model); err != nil {
		return err
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	_, bucket, config, err := s.client()
	if err != nil {
		return err
	}
	prefix, err := modelFolderPrefix(config.ModelsPrefix, model)
	if err != nil {
		return err
	}
	return s.deletePrefix(bucket, prefix, "模型文件夹")
}

type EmptyFolder struct {
	Model string `json:"model"`
	Files int    `json:"files"`
	Size  int64  `json:"size"`
}

// EmptyModelFolders lists model folders that hold repository objects but no
// published version (no manifest.json under <model>/<version>/). These are
// the only folders the UI offers to delete; folders with versions must not
// be removed through the manager.
func (s *Service) EmptyModelFolders() ([]EmptyFolder, error) {
	objects, err := s.Catalog()
	if err != nil {
		return nil, err
	}
	return emptyFoldersFromObjects(objects), nil
}

func emptyFoldersFromObjects(objects []Object) []EmptyFolder {
	stats := map[string]*EmptyFolder{}
	withVersion := map[string]bool{}
	for _, object := range objects {
		segments := strings.SplitN(object.Name, "/", 3)
		if len(segments) < 2 {
			continue // 顶层散文件不属于任何模型文件夹
		}
		model := segments[0]
		if model == "" || strings.HasPrefix(model, ".") {
			continue
		}
		if len(segments) >= 3 && segments[2] == "manifest.json" && manifestNamePattern.MatchString(segments[1]) {
			withVersion[model] = true
			continue
		}
		entry, found := stats[model]
		if !found {
			entry = &EmptyFolder{Model: model}
			stats[model] = entry
		}
		entry.Files++
		entry.Size += object.Size
	}
	folders := make([]EmptyFolder, 0, len(stats))
	for model, entry := range stats {
		if !withVersion[model] {
			folders = append(folders, *entry)
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Model < folders[j].Model })
	return folders
}

// deletePrefix 分页列举并删除 prefix 下全部对象，同时校验 OSS 没有返回越界路径；
// 一个对象都没有说明文件夹本身不存在。
func (s *Service) deletePrefix(bucket *aliyunoss.Bucket, prefix, what string) error {
	found := false
	marker := ""
	for {
		result, err := bucket.ListObjects(aliyunoss.Prefix(prefix), aliyunoss.Marker(marker), aliyunoss.MaxKeys(1000))
		if err != nil {
			return fmt.Errorf("列举%s失败: %w", what, err)
		}
		for _, object := range result.Objects {
			if !strings.HasPrefix(object.Key, prefix) {
				return fmt.Errorf("OSS 返回了%s之外的路径", what)
			}
		}
		if len(result.Objects) > 0 {
			found = true
		}
		for _, object := range result.Objects {
			if err := bucket.DeleteObject(object.Key); err != nil {
				return fmt.Errorf("删除%s失败，可能已有部分文件被删除，请刷新后重试: %w", what, err)
			}
		}
		if !result.IsTruncated {
			break
		}
		if result.NextMarker == "" || result.NextMarker == marker {
			return fmt.Errorf("列举%s分页异常，请刷新后重试", what)
		}
		marker = result.NextMarker
	}
	if !found {
		return fmt.Errorf("%s不存在或已删除: %s", what, strings.TrimPrefix(strings.TrimSuffix(prefix, "/"), "/"))
	}
	return nil
}
