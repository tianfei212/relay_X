package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	v4errors "relay-x/v4/errors"
)

const HelloPrefix = "V4HELLO "

type DataHello struct {
	Role      string `json:"role"`
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
}

// ReadHello 读取数据面连接的首行握手（V4HELLO + JSON）。
// 使用说明：
// - 数据连接建立后，第一行必须发送：V4HELLO {"role":"server|client","session_id":"...","id":"..."}\n
// 参数：
// - conn: 连接对象
// - timeout: 读取首行超时时间
// 返回：
// - DataHello: 握手信息
// - *bufio.Reader: 复用的 Reader（避免丢失已读缓冲）
// - error: 握手失败原因
func ReadHello(conn net.Conn, timeout time.Duration) (DataHello, *bufio.Reader, error) {
	r := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	line, err := r.ReadString('\n')
	if err != nil {
		return DataHello{}, r, v4errors.Wrap(v4errors.CodeBadRequest, "read hello failed", err)
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, HelloPrefix) {
		return DataHello{}, r, v4errors.Wrap(v4errors.CodeBadRequest, "missing hello prefix", fmt.Errorf("got=%q", line))
	}
	raw := strings.TrimPrefix(line, HelloPrefix)
	var h DataHello
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return DataHello{}, r, v4errors.Wrap(v4errors.CodeBadRequest, "invalid hello json", err)
	}
	h.Role = strings.ToLower(strings.TrimSpace(h.Role))
	h.SessionID = strings.TrimSpace(h.SessionID)
	h.ID = strings.TrimSpace(h.ID)
	if (h.Role != "server" && h.Role != "client") || h.SessionID == "" || h.ID == "" {
		return DataHello{}, r, v4errors.New(v4errors.CodeBadRequest, "invalid hello fields")
	}
	return h, r, nil
}
