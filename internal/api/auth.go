package api

import (
	"errors"
	"net/http"

	"center-cv-manager/internal/auth"
	"github.com/gin-gonic/gin"
)

const authCookieName = "center_manager_session"

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}
type passwordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, errors.New("请输入账号和密码"))
		return
	}
	token, status, err := s.auth.Login(c.ClientIP(), req.Username, req.Password)
	if err != nil {
		fail(c, http.StatusUnauthorized, err)
		return
	}
	setSessionCookie(c, token, int(auth.SessionTTL.Seconds()))
	ok(c, status)
}
func (s *Server) authStatus(c *gin.Context) {
	token, _ := c.Cookie(authCookieName)
	ok(c, s.auth.Authenticate(token))
}
func (s *Server) logout(c *gin.Context) {
	token, _ := c.Cookie(authCookieName)
	s.auth.Logout(token)
	setSessionCookie(c, "", -1)
	ok(c, true)
}
func (s *Server) changePassword(c *gin.Context) {
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, errors.New("请输入原密码和新密码"))
		return
	}
	token, _ := c.Cookie(authCookieName)
	if err := s.auth.ChangePassword(token, req.OldPassword, req.NewPassword); err != nil {
		status := 400
		if errors.Is(err, auth.ErrInvalidSession) {
			status = 401
		}
		fail(c, status, err)
		return
	}
	ok(c, true)
}
func (s *Server) skipPasswordChange(c *gin.Context) {
	token, _ := c.Cookie(authCookieName)
	if err := s.auth.SkipPasswordChange(token); err != nil {
		fail(c, 401, err)
		return
	}
	ok(c, true)
}
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, _ := c.Cookie(authCookieName)
		status := s.auth.Authenticate(token)
		if !status.Authenticated {
			fail(c, 401, errors.New("请先登录"))
			c.Abort()
			return
		}
		if status.MustChangePassword {
			fail(c, 403, errors.New("首次登录请修改密码或选择暂时跳过"))
			c.Abort()
			return
		}
		c.Set("auth_username", status.Username)
		c.Set("auth_role", status.Role)
		c.Next()
	}
}

// requireAdmin 只允许管理员角色通过；角色取自 requireAuth 刚解析的会话状态。
func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if role, _ := c.Get("auth_role"); role != auth.RoleAdmin {
			fail(c, http.StatusForbidden, errors.New("需要管理员权限"))
			c.Abort()
			return
		}
		c.Next()
	}
}
func setSessionCookie(c *gin.Context, value string, maxAge int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(authCookieName, value, maxAge, "/", "", false, true)
}
