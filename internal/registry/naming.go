package registry

import (
	"strings"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/domain"
)

// DefaultPrefix 是仓库内软件镜像的命名空间（OSS 侧还有 registry config.yml
// 的 rootdirectory 再隔离一层）。
const DefaultPrefix = "center-cv"

// RepoPath 返回仓库内相对路径（不含主机名）：prefix/软件默认镜像名。
func RepoPath(src domain.Source) string {
	return DefaultPrefix + "/" + build.DefaultImageRepository(src)
}

// Reference 拼出带 registry 主机的完整镜像引用（host/repository:tag）。
func Reference(host, repository, tag string) string {
	return host + "/" + repository + ":" + tag
}

// shortDigest 缩短 digest 用于日志展示（保留算法前缀 + 12 位）。
func shortDigest(digest string) string {
	if index := strings.Index(digest, ":"); index >= 0 && len(digest) > index+13 {
		return digest[:index+13]
	}
	return digest
}
