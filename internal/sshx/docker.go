package sshx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
)

type DockerClient struct {
	Client    *client.Client
	SSH       *ssh.Client
	transport *http.Transport
}

func NewDocker(cfg Config, socket string) (*DockerClient, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return NewDockerContext(ctx, cfg, socket)
}

// NewDockerContext 用调用方 ctx 约束 SSH 拨号/握手与 Docker Ping 的总时长，
// 供状态探测等对时延敏感的路径设置比默认 12 秒更紧的上限。
func NewDockerContext(ctx context.Context, cfg Config, socket string) (*DockerClient, string, error) {
	sshClient, fp, err := DialContext(ctx, cfg)
	if err != nil {
		return nil, fp, err
	}
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	transport := &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		return sshClient.Dial("unix", socket)
	}}
	httpClient := &http.Client{Transport: transport}
	cli, err := client.NewClientWithOpts(client.WithHost("http://docker"), client.WithHTTPClient(httpClient), client.WithAPIVersionNegotiation())
	if err != nil {
		sshClient.Close()
		return nil, fp, err
	}
	if _, err = cli.Ping(ctx); err != nil {
		cli.Close()
		sshClient.Close()
		return nil, fp, fmt.Errorf("远端 Docker 不可用（请确认用户有 docker.sock 权限）: %w", err)
	}
	return &DockerClient{Client: cli, SSH: sshClient, transport: transport}, fp, nil
}
func (c *DockerClient) Close() {
	c.transport.CloseIdleConnections()
	_ = c.Client.Close()
	_ = c.SSH.Close()
}
