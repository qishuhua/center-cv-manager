package sshx

import (
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
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), &ssh.ClientConfig{User: cfg.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: callback, Timeout: 12 * time.Second})
	if err != nil {
		return nil, fingerprint, fmt.Errorf("SSH 连接失败: %w", err)
	}
	return client, fingerprint, nil
}
