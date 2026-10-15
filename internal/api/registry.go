package api

import (
	"context"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/registry"
	"github.com/gin-gonic/gin"
)

func (s *Server) getRegistrySettings(c *gin.Context) {
	v, err := s.registries.Settings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, v)
}

func (s *Server) saveRegistrySettings(c *gin.Context) {
	var v domain.RegistrySettings
	if err := c.ShouldBindJSON(&v); err != nil {
		fail(c, 400, err)
		return
	}
	out, err := s.registries.SaveSettings(v)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, out)
}

// testRegistry 探活镜像仓库：GET /v2/ 返回 200 才视为可用。
func (s *Server) testRegistry(c *gin.Context) {
	settings, err := s.registries.Settings()
	if err != nil {
		fail(c, 500, err)
		return
	}
	if !settings.Configured {
		fail(c, 400, registry.ErrNotConfigured)
		return
	}
	client, err := registry.NewClient(settings.Endpoint)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	if err = client.Ping(ctx); err != nil {
		fail(c, 502, err)
		return
	}
	ok(c, gin.H{"reachable": true, "endpoint": settings.Endpoint})
}

// listRegistryVersions 列出镜像仓库全部软件版本（不按中心平台过滤），供
// 统一发布页选择；选择器仍按各中心平台在预检时校验可用性。
func (s *Server) listRegistryVersions(c *gin.Context) {
	list, err := s.registries.ListVersions(c.Request.Context())
	if err != nil {
		status := 400
		if err != registry.ErrNotConfigured {
			status = 502
		}
		fail(c, status, err)
		return
	}
	ok(c, list)
}

func (s *Server) getRegistryStorage(c *gin.Context) {
	v, err := s.registries.StorageConfig()
	if err != nil {
		fail(c, 500, err)
		return
	}
	ok(c, v)
}

func (s *Server) saveRegistryStorage(c *gin.Context) {
	var v domain.RegistryStorage
	if err := c.ShouldBindJSON(&v); err != nil {
		fail(c, 400, err)
		return
	}
	out, err := s.registries.SaveStorageConfig(v)
	if err != nil {
		fail(c, 400, err)
		return
	}
	ok(c, out)
}
