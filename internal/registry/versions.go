package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"center-cv-manager/internal/build"
)

// PlatformTag 是一个版本在仓库内的平台标签（nvidia/denglin/cpu；shared 表示
// 未区分平台的历史标签，与所有平台兼容）。
type PlatformTag struct {
	Platform string `json:"platform"`
	Tag      string `json:"tag"`
	Digest   string `json:"digest"`
}

// SoftwareVersion 是一组平台标签聚合出的软件版本：version 为 ref-commit 主体
// （不含平台后缀），ref/commit 来自镜像构建时的来源标签。
type SoftwareVersion struct {
	Version   string        `json:"version"`
	Ref       string        `json:"ref"`
	Commit    string        `json:"commit"`
	CreatedAt time.Time     `json:"created_at"`
	Platforms []PlatformTag `json:"platforms"`
}

// SourceVersions 是一个软件（源码仓库）在镜像仓库中的全部版本。
type SourceVersions struct {
	Repository string            `json:"repository"`
	SourceID   string            `json:"source_id"`
	SourceName string            `json:"source_name"`
	Versions   []SoftwareVersion `json:"versions"`
}

// Catalog 列出仓库内的全部 repository。
func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v2/_catalog", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("读取镜像仓库清单失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取镜像仓库清单返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Repositories []string `json:"repositories"`
	}
	if err = json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Repositories, nil
}

// Tags 列出 repository 的全部标签。
func (c *Client) Tags(ctx context.Context, repository string) ([]string, error) {
	if !validRepository(repository) {
		return nil, fmt.Errorf("镜像仓库路径无效: %s", repository)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v2/%s/tags/list", c.baseURL, repository), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 标签失败: %w", repository, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取 %s 标签返回 HTTP %d", repository, response.StatusCode)
	}
	var payload struct {
		Tags []string `json:"tags"`
	}
	if err = json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Tags, nil
}

// ImageInfo 读取 tag 的 manifest 与镜像 config：digest、构建来源标签、创建
// 时间。按 manifest digest 缓存——镜像内容不可变，同 digest 不重复请求。
func (c *Client) ImageInfo(ctx context.Context, repository, tag string) (ImageInfo, error) {
	if !validRepository(repository) {
		return ImageInfo{}, fmt.Errorf("镜像仓库路径无效: %s", repository)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, repository, tag), nil)
	if err != nil {
		return ImageInfo{}, err
	}
	request.Header.Set("Accept", manifestAccept)
	response, err := c.http.Do(request)
	if err != nil {
		return ImageInfo{}, fmt.Errorf("读取 %s:%s manifest 失败: %w", repository, tag, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ImageInfo{}, fmt.Errorf("读取 %s:%s manifest 返回 HTTP %d", repository, tag, response.StatusCode)
	}
	digest := response.Header.Get("Docker-Content-Digest")
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err = json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		return ImageInfo{}, err
	}
	if manifest.Config.Digest == "" {
		return ImageInfo{}, fmt.Errorf("%s:%s 的 manifest 缺少 config 引用", repository, tag)
	}
	cacheKey := repository + "@" + manifest.Config.Digest
	c.mu.Lock()
	cached, hit := c.cache[cacheKey]
	c.mu.Unlock()
	if hit {
		return cached, nil
	}
	info := ImageInfo{Digest: digest}
	blob, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v2/%s/blobs/%s", c.baseURL, repository, manifest.Config.Digest), nil)
	if err != nil {
		return ImageInfo{}, err
	}
	blobResponse, err := c.http.Do(blob)
	if err != nil {
		return ImageInfo{}, fmt.Errorf("读取 %s:%s 镜像配置失败: %w", repository, tag, err)
	}
	defer blobResponse.Body.Close()
	// blob 经 307 重定向到 OSS 签名 URL，Go 默认跟随重定向。
	if blobResponse.StatusCode != http.StatusOK {
		return ImageInfo{}, fmt.Errorf("读取 %s:%s 镜像配置返回 HTTP %d", repository, tag, blobResponse.StatusCode)
	}
	var config struct {
		Created time.Time `json:"created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err = json.NewDecoder(blobResponse.Body).Decode(&config); err != nil {
		return ImageInfo{}, err
	}
	info.Created = config.Created
	info.Labels = config.Config.Labels
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]ImageInfo{}
	}
	c.cache[cacheKey] = info
	c.mu.Unlock()
	return info, nil
}

// ListVersions 汇总镜像仓库内的软件版本：只统计本管理端前缀下、带来源标签
// 的镜像；按 ref-commit 主体聚合各平台标签。
func (s *Service) ListVersions(ctx context.Context) ([]SourceVersions, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	repositories, err := client.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := []SourceVersions{}
	for _, repository := range repositories {
		if !strings.HasPrefix(repository, DefaultPrefix+"/") {
			continue
		}
		tags, err := client.Tags(ctx, repository)
		if err != nil {
			return nil, err
		}
		source := SourceVersions{Repository: repository}
		versions := map[string]*SoftwareVersion{}
		for _, tag := range tags {
			info, err := client.ImageInfo(ctx, repository, tag)
			if err != nil {
				return nil, err
			}
			if info.Labels[build.LabelSourceID] == "" {
				continue
			}
			version, platform := splitTag(tag)
			entry, exists := versions[version]
			if !exists {
				entry = &SoftwareVersion{
					Version: version, Ref: info.Labels[build.LabelSourceRef], Commit: info.Labels[build.LabelCommit],
					CreatedAt: info.Created,
				}
				versions[version] = entry
			}
			entry.Platforms = append(entry.Platforms, PlatformTag{Platform: platform, Tag: tag, Digest: info.Digest})
			if source.SourceID == "" {
				source.SourceID = info.Labels[build.LabelSourceID]
				source.SourceName = info.Labels[build.LabelSourceName]
			}
		}
		for _, entry := range versions {
			sort.Slice(entry.Platforms, func(i, j int) bool { return entry.Platforms[i].Platform < entry.Platforms[j].Platform })
			source.Versions = append(source.Versions, *entry)
		}
		if len(source.Versions) == 0 {
			continue
		}
		sort.Slice(source.Versions, func(i, j int) bool { return source.Versions[i].CreatedAt.After(source.Versions[j].CreatedAt) })
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceName < out[j].SourceName })
	return out, nil
}

// VersionsForPlatform 过滤出指定显卡平台可用的版本并剔除没有可用版本的
// 软件。平台后缀标签精确匹配；通用（无后缀）标签是平台无关构建的产物，
// 对所有平台可用——发布链路（manifestDigestForPlatform）有对应的取用顺序。
func VersionsForPlatform(list []SourceVersions, platform string) []SourceVersions {
	out := make([]SourceVersions, 0, len(list))
	for _, source := range list {
		filtered := SourceVersions{Repository: source.Repository, SourceID: source.SourceID, SourceName: source.SourceName}
		for _, version := range source.Versions {
			if TagForPlatform(version, platform) != "" {
				filtered.Versions = append(filtered.Versions, version)
			}
		}
		if len(filtered.Versions) > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

var platformSuffixes = []string{"nvidia", "denglin", "cpu"}

// DeleteVersion 删除一个软件版本的全部平台 manifest（各平台标签随之失效，
// 中心上已运行的实例不受影响，但该版本不能再发布；blob 存储由仓库 GC 回收）。
// 只允许删除本管理端前缀下的仓库。
func (s *Service) DeleteVersion(ctx context.Context, repository, version string) error {
	if !strings.HasPrefix(repository, DefaultPrefix+"/") {
		return fmt.Errorf("只能删除 %s 前缀下的软件镜像", DefaultPrefix)
	}
	if version == "" || strings.ContainsAny(version, " /") {
		return fmt.Errorf("镜像版本无效")
	}
	client, err := s.Client()
	if err != nil {
		return err
	}
	tags, err := client.Tags(ctx, repository)
	if err != nil {
		return err
	}
	deleted := 0
	for _, tag := range tags {
		if body, _ := splitTag(tag); body != version {
			continue
		}
		digest, err := client.ManifestDigest(ctx, repository, tag)
		if err != nil {
			return err
		}
		if digest == "" {
			continue
		}
		if err = client.DeleteManifest(ctx, repository, digest); err != nil {
			return err
		}
		deleted++
	}
	if deleted == 0 {
		return fmt.Errorf("镜像仓库中没有版本 %s 的标签", version)
	}
	return nil
}

// TagsHaveVersion 判断标签列表是否包含指定版本主体（ref-commit12，不含仓库
// 名与平台后缀）：软件发布只认已构建发布到仓库的版本。
func TagsHaveVersion(tags []string, version string) bool {
	for _, tag := range tags {
		if body, _ := splitTag(tag); body == version {
			return true
		}
	}
	return false
}

// splitTag 把仓库标签拆成版本主体与平台后缀；无平台后缀视为 shared。
func splitTag(tag string) (version, platform string) {
	for _, candidate := range platformSuffixes {
		if suffix := "-" + candidate; strings.HasSuffix(tag, suffix) {
			return strings.TrimSuffix(tag, suffix), candidate
		}
	}
	return tag, "shared"
}

// TagForPlatform 返回版本在指定平台下的仓库标签：优先平台后缀标签，回退
// 通用（无后缀）标签；两者皆无返回空串。
func TagForPlatform(version SoftwareVersion, platform string) string {
	for _, tag := range version.Platforms {
		if tag.Platform == platform {
			return tag.Tag
		}
	}
	for _, tag := range version.Platforms {
		if tag.Platform == "shared" {
			return tag.Tag
		}
	}
	return ""
}

// ImageInfo 是一个仓库标签的镜像元数据（digest、构建来源标签、创建时间）。
type ImageInfo struct {
	Digest  string
	Labels  map[string]string
	Created time.Time
}
