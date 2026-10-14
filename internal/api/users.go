package api

import (
	"errors"
	"net/http"

	"center-cv-manager/internal/auth"
	"github.com/gin-gonic/gin"
)

type userRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// userFailError 把用户管理错误映射为合适的 HTTP 状态码。
func userFailError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		fail(c, http.StatusNotFound, err)
	case errors.Is(err, auth.ErrUserExists),
		errors.Is(err, auth.ErrLastAdmin),
		errors.Is(err, auth.ErrDeleteSelf),
		errors.Is(err, auth.ErrSuperAdmin):
		fail(c, http.StatusConflict, err)
	case errors.Is(err, auth.ErrInvalidSession):
		fail(c, http.StatusUnauthorized, err)
	default:
		fail(c, http.StatusBadRequest, err)
	}
}

func (s *Server) listUsers(c *gin.Context) {
	ok(c, s.auth.ListUsers())
}
func (s *Server) createUser(c *gin.Context) {
	var req userRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, errors.New("请输入用户名和初始密码"))
		return
	}
	user, err := s.auth.CreateUser(req.Username, req.Password, req.Role)
	if err != nil {
		userFailError(c, err)
		return
	}
	ok(c, user)
}
func (s *Server) setUserRole(c *gin.Context) {
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, errors.New("请选择角色"))
		return
	}
	user, err := s.auth.SetUserRole(c.Param("username"), req.Role)
	if err != nil {
		userFailError(c, err)
		return
	}
	ok(c, user)
}
func (s *Server) resetUserPassword(c *gin.Context) {
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, errors.New("请输入新密码"))
		return
	}
	user, err := s.auth.ResetUserPassword(c.Param("username"), req.Password)
	if err != nil {
		userFailError(c, err)
		return
	}
	ok(c, user)
}
func (s *Server) deleteUser(c *gin.Context) {
	actor, _ := c.Get("auth_username")
	username := c.Param("username")
	if err := s.auth.DeleteUser(actor.(string), username); err != nil {
		userFailError(c, err)
		return
	}
	ok(c, true)
}
