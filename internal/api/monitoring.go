package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

// monitoringStatus 返回统一监控栈与各中心的纳入情况、健康信号（只读，查看
// 用户也可访问；监控栈未部署时 StackOnline=false，不报错）。
func (s *Server) monitoringStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	ok(c, s.monitors.Status(ctx))
}

// syncMonitoring 在中心或凭据变更后重渲染监控动态配置。失败不回滚已保存的
// 业务数据，由调用方以"已保存，但监控同步失败"口径提示用户。
func (s *Server) syncMonitoring(action string) error {
	if err := s.monitors.Sync(); err != nil {
		return fmt.Errorf("%s，但统一监控配置同步失败: %w", action, err)
	}
	return nil
}

// reloadAlertmanager 在飞书 Webhook 变更后重载 alertmanager（url_file 仅在
// 配置加载时读取）。尽力而为：本地 go run 或监控栈未部署时提示手动重启。
func (s *Server) reloadAlertmanager(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := s.monitors.ReloadAlertmanager(ctx); err != nil {
		c.Header("X-Manager-Warning", "飞书 Webhook 已更新，但重载 alertmanager 失败（监控栈未部署？），配置将在容器重启后生效")
	}
}
