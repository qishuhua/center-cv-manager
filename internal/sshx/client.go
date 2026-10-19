package sshx

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

type Config struct {
	Host       string
	Port       int
	Username   string
	Password   string
	PrivateKey string
	Passphrase string
	HostKey    string
}

func Dial(cfg Config) (*ssh.Client, string, error) {
	return DialContext(context.Background(), cfg)
}

// DialContext bounds both TCP connection and SSH handshake, and honors cancellation.
func DialContext(ctx context.Context, cfg Config) (*ssh.Client, string, error) {
	var auth ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, "", fmt.Errorf("解析 SSH 私钥: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	} else if cfg.Password != "" {
		auth = ssh.Password(cfg.Password)
	} else {
		return nil, "", fmt.Errorf("未配置 SSH 认证信息")
	}
	fingerprint := ""
	callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		if cfg.HostKey != "" && cfg.HostKey != fingerprint {
			return fmt.Errorf("SSH 主机密钥已变化：期望 %s，实际 %s", cfg.HostKey, fingerprint)
		}
		return nil
	}
	address := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	connectCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(connectCtx, "tcp", address)
	if err != nil {
		return nil, fingerprint, fmt.Errorf("SSH 连接失败: %w", err)
	}
	deadline, _ := connectCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(connectCtx, func() { _ = conn.Close() })
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: cfg.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: callback})
	stop()
	if err != nil {
		_ = conn.Close()
		return nil, fingerprint, fmt.Errorf("SSH 连接失败: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, channels, requests)
	return client, fingerprint, nil
}
