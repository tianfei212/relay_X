package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

type MessageType string

const (
	MsgRegister    MessageType = "register"
	MsgAuth        MessageType = "auth"
	MsgPortAcquire MessageType = "port_acquire"
	MsgPortGrant   MessageType = "port_grant"
	MsgPortRelease MessageType = "port_release"
	MsgPortStateQ  MessageType = "port_state_q"
	MsgPortState   MessageType = "port_state"
	MsgPortAlloc   MessageType = "port_alloc"
	MsgPing        MessageType = "ping"
	MsgPong        MessageType = "pong"
	MsgLog         MessageType = "log"
)

type BaseMessage struct {
	Type    MessageType `json:"type"`
	Payload any         `json:"payload,omitempty"`
}

type rawEnvelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type RegisterPayload struct {
	Role     string `json:"role"`
	ClientID string `json:"client_id,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

type PortAcquirePayload struct {
	RequestID  string `json:"request_id,omitempty"`
	NeedZMQ    bool   `json:"need_zmq"`
	NeedSRT    bool   `json:"need_srt"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type PortGrantPayload struct {
	RequestID string `json:"request_id,omitempty"`
	SessionID string `json:"session_id"`
	ZMQPort   int    `json:"zmq_port,omitempty"`
	SRTPort   int    `json:"srt_port,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

type PortReleasePayload struct {
	SessionID string `json:"session_id"`
}

type ClockPayload struct {
	ClientSendTime int64 `json:"c_send"`
	ServerRecvTime int64 `json:"s_recv"`
	ServerSendTime int64 `json:"s_send"`
}

func main() {
	var (
		gatewayAddr = flag.String("gateway", "127.0.0.1:5555", "控制面地址 host:port")
		dataHost    = flag.String("data-host", "", "数据面host（默认取 gateway host）")
		timeout     = flag.Duration("timeout", 10*time.Second, "整体超时")
		tcpBytes    = flag.Int("tcp-bytes", 1024, "TCP 发送字节数")
		udpBytes    = flag.Int("udp-bytes", 1024, "UDP 发送字节数")
		udpTries    = flag.Int("udp-tries", 20, "UDP 往返尝试次数")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	host := *dataHost
	if host == "" {
		h, _, err := net.SplitHostPort(*gatewayAddr)
		if err != nil {
			fmt.Printf("解析 gateway 失败: %v\n", err)
			os.Exit(1)
		}
		host = h
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", *gatewayAddr)
	if err != nil {
		fmt.Printf("连接控制面失败: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(bufio.NewReader(conn))

	clientID := "fe-" + shortID()
	_ = enc.Encode(BaseMessage{
		Type: MsgRegister,
		Payload: RegisterPayload{
			Role:     "FE",
			ClientID: clientID,
		},
	})

	grantCh := make(chan PortGrantPayload, 8)
	go func() {
		for {
			var env rawEnvelope
			if err := dec.Decode(&env); err != nil {
				return
			}
			switch env.Type {
			case MsgPortGrant:
				var g PortGrantPayload
				if json.Unmarshal(env.Payload, &g) == nil {
					select {
					case grantCh <- g:
					default:
					}
				}
			case MsgAuth, MsgPortAlloc, MsgPortState, MsgPong, MsgLog:
			default:
			}
		}
	}()

	reqID := shortID()
	if err := enc.Encode(BaseMessage{
		Type: MsgPortAcquire,
		Payload: PortAcquirePayload{
			RequestID:  reqID,
			NeedZMQ:    true,
			NeedSRT:    true,
			TTLSeconds: 60,
		},
	}); err != nil {
		fmt.Printf("发送 port_acquire 失败: %v\n", err)
		os.Exit(1)
	}

	var grant PortGrantPayload
	select {
	case <-ctx.Done():
		fmt.Printf("等待 port_grant 超时\n")
		os.Exit(1)
	case g := <-grantCh:
		grant = g
	}
	if grant.Error != "" {
		fmt.Printf("port_grant 返回错误: %s\n", grant.Error)
		os.Exit(1)
	}

	port := grant.ZMQPort
	if port <= 0 {
		port = grant.SRTPort
	}
	if port <= 0 {
		fmt.Printf("port_grant 未返回端口\n")
		os.Exit(1)
	}

	dataAddr := fmt.Sprintf("%s:%d", host, port)

	tcpConn, err := net.Dial("tcp", dataAddr)
	if err != nil {
		fmt.Printf("连接数据面 TCP 失败: %v\n", err)
		os.Exit(1)
	}
	defer tcpConn.Close()

	hello, _ := json.Marshal(map[string]string{"role": "FE", "client_id": clientID})
	_, _ = tcpConn.Write(append(append([]byte("RLX1HELLO "), hello...), '\n'))

	udpRemote, err := net.ResolveUDPAddr("udp", dataAddr)
	if err != nil {
		fmt.Printf("解析 UDP 地址失败: %v\n", err)
		os.Exit(1)
	}
	udpConn, err := net.DialUDP("udp", nil, udpRemote)
	if err != nil {
		fmt.Printf("连接数据面 UDP 失败: %v\n", err)
		os.Exit(1)
	}
	defer udpConn.Close()

	_, _ = udpConn.Write([]byte("udp-init"))

	if err := testTCP(ctx, tcpConn, *tcpBytes); err != nil {
		fmt.Printf("TCP 测试失败: %v\n", err)
		os.Exit(1)
	}
	if err := testUDP(ctx, udpConn, *udpBytes, *udpTries); err != nil {
		fmt.Printf("UDP 测试失败: %v\n", err)
		os.Exit(1)
	}

	_ = enc.Encode(BaseMessage{Type: MsgPortRelease, Payload: PortReleasePayload{SessionID: grant.SessionID}})
	_ = enc.Encode(BaseMessage{Type: MsgPortStateQ, Payload: map[string]any{}})
	_ = enc.Encode(BaseMessage{Type: MsgPing, Payload: ClockPayload{ClientSendTime: time.Now().UnixNano()}})

	fmt.Printf("OK port=%d session=%s\n", port, grant.SessionID)
}

func testTCP(ctx context.Context, c net.Conn, n int) error {
	if n <= 0 {
		n = 1
	}
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	if err := c.SetWriteDeadline(deadlineFrom(ctx, 2*time.Second)); err == nil {
		_, _ = c.Write(payload)
	}
	if err := c.SetReadDeadline(deadlineFrom(ctx, 2*time.Second)); err == nil {
		got := make([]byte, n)
		if _, err := io.ReadFull(c, got); err != nil {
			return err
		}
		if !bytesEqual(payload, got) {
			return fmt.Errorf("回包不一致 len=%d", n)
		}
	}
	return nil
}

func testUDP(ctx context.Context, c *net.UDPConn, n, tries int) error {
	if n <= 0 {
		n = 1
	}
	if tries <= 0 {
		tries = 1
	}

	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 251)
	}

	buf := make([]byte, 64*1024)
	for i := 0; i < tries; i++ {
		_, _ = c.Write(payload)
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		rn, err := c.Read(buf)
		if err != nil {
			continue
		}
		if rn != len(payload) {
			continue
		}
		if bytesEqual(buf[:rn], payload) {
			return nil
		}
	}
	return fmt.Errorf("未收到匹配回包（通常需要先启动 server_test 后端回显）")
}

func deadlineFrom(ctx context.Context, d time.Duration) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(d)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func shortID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return strings.ToLower(hex.EncodeToString(b[:]))
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
