package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"strings"

	"center-cv-manager/internal/auth"
	"github.com/gin-gonic/gin"
)

func (s *Server) listTokens(c *gin.Context) {
	name, _ := c.Get("auth_username")
	ok(c, s.auth.ListAPITokens(name.(string)))
}
func (s *Server) createToken(c *gin.Context) {
	var in struct {
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		CenterIDs  []string `json:"center_ids"`
		AllCenters bool     `json:"all_centers"`
		Days       int      `json:"days"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, 400, err)
		return
	}
	name, _ := c.Get("auth_username")
	for _, id := range in.CenterIDs {
		if _, err := s.centers.GetPublic(id); err != nil {
			fail(c, 400, errors.New("发布中心不存在"))
			return
		}
	}
	token, raw, err := s.auth.CreateAPIToken(name.(string), in.Name, in.Scopes, in.CenterIDs, in.AllCenters, in.Days)
	if err != nil {
		fail(c, 400, err)
		return
	}
	token.Hash = ""
	c.Header("Cache-Control", "no-store")
	ok(c, gin.H{"token": raw, "info": token})
}
func (s *Server) revokeToken(c *gin.Context) {
	name, _ := c.Get("auth_username")
	if err := s.auth.RevokeAPIToken(name.(string), c.Param("id")); err != nil {
		fail(c, 404, err)
		return
	}
	ok(c, true)
}
func (s *Server) requireToken(scope string, checkCenters bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		t, okToken := s.authenticateToken(c, scope)
		if !okToken {
			c.Abort()
			return
		}
		if checkCenters {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20+1))
			if err != nil || len(body) > 1<<20 {
				fail(c, 400, errors.New("请求内容过大或无效"))
				c.Abort()
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				CenterIDs []string `json:"center_ids"`
			}
			if json.Unmarshal(body, &input) != nil || len(input.CenterIDs) == 0 {
				fail(c, 400, errors.New("请选择发布中心"))
				c.Abort()
				return
			}
			for _, id := range input.CenterIDs {
				found := t.AllCenters
				for _, allowedID := range t.CenterIDs {
					if id == allowedID {
						found = true
					}
				}
				if !found {
					fail(c, 403, errors.New("令牌无权操作指定中心"))
					c.Abort()
					return
				}
			}
		}
		c.Next()
	}
}
// authenticateToken 完成令牌解析、scope 与管理员角色校验；失败时已写响应并返回
// false，两个 requireToken* 中间件共用。
func (s *Server) authenticateToken(c *gin.Context, scope string) (auth.APIToken, bool) {
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		fail(c, 401, errors.New("需要 MCP 令牌"))
		return auth.APIToken{}, false
	}
	t, status := s.auth.AuthenticateAPIToken(strings.TrimPrefix(header, "Bearer "))
	if !status.Authenticated {
		fail(c, 401, errors.New("令牌无效或已过期"))
		return auth.APIToken{}, false
	}
	allowed := false
	for _, v := range t.Scopes {
		if v == scope {
			allowed = true
		}
	}
	if !allowed || (scope != "read" && status.Role != auth.RoleAdmin) {
		fail(c, 403, errors.New("令牌权限不足"))
		return auth.APIToken{}, false
	}
	c.Set("auth_username", status.Username)
	c.Set("auth_role", status.Role)
	return t, true
}

// requireTokenCenter 与 requireToken 相同的令牌/权限校验，但中心授权取路径参数
// :id（模块操作类接口的目标中心在 URL 而非请求体）。
func (s *Server) requireTokenCenter(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		t, okToken := s.authenticateToken(c, scope)
		if !okToken {
			c.Abort()
			return
		}
		found := t.AllCenters
		for _, allowedID := range t.CenterIDs {
			if c.Param("id") == allowedID {
				found = true
			}
		}
		if !found {
			fail(c, 403, errors.New("令牌无权操作指定中心"))
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) registerMCP(root *gin.RouterGroup) {
	g := root.Group("/mcp")
	read := g.Group("")
	read.Use(s.requireToken("read", false))
	read.GET("/centers", s.listCenters)
	read.GET("/centers/:id/overview", s.centerOverview)
	read.POST("/centers/:id/detect", s.detectCenter)
	read.POST("/centers/:id/test", s.testCenter)
	read.GET("/monitoring/status", s.monitoringStatus)
	read.GET("/sources", s.listSources)
	read.GET("/sources/:id/versions", s.versions)
	read.GET("/models", s.modelCatalog)
	read.GET("/registry/versions", s.listRegistryVersions)
	read.GET("/tasks", s.listTasks)
	read.GET("/tasks/:id", s.getTask)
	read.GET("/tasks/:id/logs", s.taskLogs)
	g.POST("/sources/:id/publish", s.requireToken("build", false), s.publishSourceImage)
	g.POST("/models/convert", s.requireToken("build", false), s.convertModel)
	g.POST("/tasks/:id/cancel", s.requireToken("release", false), s.cancelTask)
	g.POST("/centers/:id/modules/:module/actions", s.requireTokenCenter("release"), s.mcpModuleAction)
	g.POST("/centers/:id/modules/:module/publish", s.requireTokenCenter("release"), s.mcpComposePublish)
	g.POST("/releases/plan", s.requireToken("release", true), s.releasePlan)
	g.POST("/releases", s.requireToken("release", true), s.release)
}
