package registry

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client 是 registry v2 HTTP API 的最小客户端。registry 无认证（只绑管理端
// 回环地址，唯一入口是管理端自己的 SSH 隧道），只需探活与 manifest 查询。
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(endpoint string) (*Client, error) {
	baseURL, _, err := ParseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Ping 验证 registry 可达且实现了 v2 接口（GET /v2/ 返回 200）。
func (c *Client) Ping(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v2/", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("镜像仓库不可达: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("镜像仓库返回 HTTP %d（GET /v2/）", response.StatusCode)
	}
	return nil
}

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ", ")

// ManifestDigest 返回 tag 当前指向的 manifest digest；镜像不存在时返回空串。
func (c *Client) ManifestDigest(ctx context.Context, repository, tag string) (string, error) {
	if !validRepository(repository) {
		return "", fmt.Errorf("镜像仓库路径无效: %s", repository)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, repository, tag), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", manifestAccept)
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("查询镜像仓库失败: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", nil
	default:
		return "", fmt.Errorf("查询 %s:%s 返回 HTTP %d", repository, tag, response.StatusCode)
	}
	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("镜像仓库未返回 %s:%s 的 digest", repository, tag)
	}
	return digest, nil
}

// validRepository 防止仓库路径被拼进 URL 时越界（路径来自设置前缀 + 软件名，
// 正常都经过清洗，这里兜底）。
func validRepository(repository string) bool {
	return repository != "" && !strings.Contains(repository, "..") && !strings.Contains(repository, " ") && !strings.Contains(repository, "\n")
}
