package modelasset

import (
	"fmt"
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
	marker := ""
	for {
		result, err := bucket.ListObjects(aliyunoss.Prefix(prefix), aliyunoss.Marker(marker), aliyunoss.MaxKeys(1000))
		if err != nil {
			return fmt.Errorf("列举模型文件夹失败: %w", err)
		}
		for _, object := range result.Objects {
			if !strings.HasPrefix(object.Key, prefix) {
				return fmt.Errorf("OSS 返回了模型文件夹之外的路径")
			}
		}
		for _, object := range result.Objects {
			if err := bucket.DeleteObject(object.Key); err != nil {
				return fmt.Errorf("删除模型文件夹失败，可能已有部分文件被删除，请刷新后重试: %w", err)
			}
		}
		if !result.IsTruncated {
			return nil
		}
		if result.NextMarker == "" || result.NextMarker == marker {
			return fmt.Errorf("列举模型文件夹分页异常，请刷新后重试")
		}
		marker = result.NextMarker
	}
}
