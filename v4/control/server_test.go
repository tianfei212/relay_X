package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"relay-x/v4/config"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
	"relay-x/v4/relay"
)

// TestStatusEndpoint 验证 HTTP /status 健康检查可用。
func TestStatusEndpoint(t *testing.T) {
	cfg, stop := startTestGateway(t)
	defer stop()

	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Gateway.TCPPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, _ = c.Write([]byte("GET /status HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("unexpected status line: %q", line)
	}
	for {
		l, _ := r.ReadString('\n')
		if l == "\r\n" || l == "\n" {
			break
		}
	}
	var hs HealthStatusPayload
	if err := json.NewDecoder(r).Decode(&hs); err != nil {
		t.Fatal(err)
	}
	if hs.Status == "" || hs.NowUnixMs == 0 {
		t.Fatalf("invalid health payload: %+v", hs)
	}
}

// TestPairRequestGrant 验证注册后可成功配对并拿到端口授权。
func TestPairRequestGrant(t *testing.T) {
	cfg, stop := startTestGateway(t)
	defer stop()

	srvConn := dialJSON(t, cfg.Gateway.TCPPort)
	defer srvConn.Close()
	cliConn := dialJSON(t, cfg.Gateway.TCPPort)
	defer cliConn.Close()

	srvDec := json.NewDecoder(srvConn)
	srvEnc := json.NewEncoder(srvConn)
	cliDec := json.NewDecoder(cliConn)
	cliEnc := json.NewEncoder(cliConn)

	_ = srvEnc.Encode(Envelope{Type: MsgRegister, Payload: RegisterPayload{Role: RoleServer, ID: "srv1", Token: "t1", MaxConn: 10}})
	expectRegisterOK(t, srvDec)
	_ = cliEnc.Encode(Envelope{Type: MsgRegister, Payload: RegisterPayload{Role: RoleClient, ID: "cli1", Token: "t2", MaxConn: 1}})
	expectRegisterOK(t, cliDec)

	_ = cliEnc.Encode(Envelope{Type: MsgPairRequest, Payload: PairRequestPayload{RequestID: "r1", NeedZMQ: true}})
	var env Envelope
	if err := cliDec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Type != MsgPairGrant {
		t.Fatalf("expected pair_grant, got %s", env.Type)
	}
	b, _ := json.Marshal(env.Payload)
	var grant PairGrantPayload
	_ = json.Unmarshal(b, &grant)
	if grant.SessionID == "" || grant.ZMQPort == 0 || grant.ServerID != "srv1" || grant.ClientID != "cli1" {
		t.Fatalf("bad grant: %+v", grant)
	}
}

// dialJSON 建立控制连接（JSON 协议）。
func dialJSON(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// expectRegisterOK 读取并断言 register_ack 返回 ok。
func expectRegisterOK(t *testing.T, dec *json.Decoder) {
	t.Helper()
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Type != MsgRegisterAck {
		t.Fatalf("expected register_ack, got %s", env.Type)
	}
	b, _ := json.Marshal(env.Payload)
	var ack RegisterAckPayload
	_ = json.Unmarshal(b, &ack)
	if ack.Status != "ok" {
		t.Fatalf("register not ok: %+v", ack)
	}
}

// startTestGateway 启动一个用于测试的 V4 控制面 + 数据面（占用随机端口）。
func startTestGateway(t *testing.T) (config.Config, func()) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Logging.Output = "console"
	_ = v4log.Init(cfg.Logging)

	cfg.Gateway.TCPPort = freeTCPPort(t)
	cfg.Gateway.AuthTimeout = 1 * time.Second
	cfg.Gateway.ShutdownTimeout = 1 * time.Second

	zmqPort := freeTCPPort(t)
	udpPort := freeUDPPort(t)
	cfg.ZeroMQ.PortRange = fmt.Sprintf("%d-%d", zmqPort, zmqPort)
	cfg.GoSRT.PortRange = fmt.Sprintf("%d-%d", udpPort, udpPort)

	zmqPool, _ := ports.NewPool(ports.KindZMQ, zmqPort, zmqPort)
	srtPool, _ := ports.NewPool(ports.KindSRT, udpPort, udpPort)
	rm := relay.NewManager(cfg, zmqPool, srtPool)
	if err := rm.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := NewHub(cfg, rm, zmqPool, srtPool)
	go func() { _ = h.Start(ctx) }()

	time.Sleep(100 * time.Millisecond)

	stop := func() {
		cancel()
		rm.Stop()
	}
	return cfg, stop
}

// freeTCPPort 获取一个可用的临时 TCP 端口（用于测试）。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// freeUDPPort 获取一个可用的临时 UDP 端口（用于测试）。
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
