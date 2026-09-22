package api

import (
	"encoding/json"
	"io"
	"sync"

	"center-cv-manager/internal/sshx"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"
)

type terminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type lockedWebsocketWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *lockedWebsocketWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := websocket.Message.Send(w.conn, append([]byte(nil), data...)); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *Server) terminalSocket(centerID string, ws *websocket.Conn) {
	defer ws.Close()
	ws.MaxPayloadBytes = 64 << 10
	writer := &lockedWebsocketWriter{conn: ws}

	cfg, _, center, err := s.centers.Connection(centerID)
	if err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m无法读取中心连接信息：" + err.Error() + "\x1b[0m\r\n"))
		return
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m" + err.Error() + "\x1b[0m\r\n"))
		return
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(center, fingerprint)

	session, err := client.NewSession()
	if err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m创建 SSH 会话失败：" + err.Error() + "\x1b[0m\r\n"))
		return
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m打开终端输入失败：" + err.Error() + "\x1b[0m\r\n"))
		return
	}
	session.Stdout = writer
	session.Stderr = writer
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err = session.RequestPty("xterm-256color", 36, 120, modes); err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m远端不支持交互终端：" + err.Error() + "\x1b[0m\r\n"))
		return
	}
	if err = session.Shell(); err != nil {
		_, _ = writer.Write([]byte("\r\n\x1b[31m启动 Shell 失败：" + err.Error() + "\x1b[0m\r\n"))
		return
	}

	go func() {
		_ = session.Wait()
		_, _ = writer.Write([]byte("\r\n\x1b[90mSSH 会话已结束\x1b[0m\r\n"))
		_ = ws.Close()
	}()

	for {
		var payload []byte
		if err = websocket.Message.Receive(ws, &payload); err != nil {
			if err != io.EOF {
				_ = session.Signal(ssh.SIGHUP)
			}
			return
		}
		var message terminalMessage
		if err = json.Unmarshal(payload, &message); err != nil {
			continue
		}
		switch message.Type {
		case "input":
			_, _ = io.WriteString(stdin, message.Data)
		case "resize":
			cols, rows := clampTerminalSize(message.Cols, message.Rows)
			_ = session.WindowChange(rows, cols)
		}
	}
}

func clampTerminalSize(cols, rows int) (int, int) {
	if cols < 20 {
		cols = 20
	} else if cols > 500 {
		cols = 500
	}
	if rows < 8 {
		rows = 8
	} else if rows > 200 {
		rows = 200
	}
	return cols, rows
}
