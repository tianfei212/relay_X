package v4testing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"

	"relay-x/v4/config"
	"relay-x/v4/control"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
	"relay-x/v4/relay"
	"relay-x/v4/testing/video"
)

func TestVideoE2ESRTFileStream(t *testing.T) {
	if _, err := os.Stat("client_test/IMG_4127.MOV"); err != nil {
		t.Skip("missing client_test/IMG_4127.MOV")
	}

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

	_ = srvEnc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{Role: control.RoleServer, ID: "srv1", Token: "t1", MaxConn: 10}})
	expectRegisterOK(t, srvDec)
	_ = cliEnc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{Role: control.RoleClient, ID: "cli1", Token: "t2", MaxConn: 1}})
	expectRegisterOK(t, cliDec)

	base := waitPortState(t, cliDec)

	_ = cliEnc.Encode(control.Envelope{Type: control.MsgPairRequest, Payload: control.PairRequestPayload{RequestID: "r1", NeedSRT: true}})

	grantCli := expectType(t, cliDec, control.MsgPairGrant)
	grantSrv := expectType(t, srvDec, control.MsgPairGrant)

	var gc control.PairGrantPayload
	_ = json.Unmarshal(mustJSON(grantCli.Payload), &gc)
	var gs control.PairGrantPayload
	_ = json.Unmarshal(mustJSON(grantSrv.Payload), &gs)

	if gc.SessionID == "" || gc.SRTPort == 0 {
		t.Fatalf("bad grant: %+v", gc)
	}
	if gs.SessionID != gc.SessionID || gs.SRTPort != gc.SRTPort {
		t.Fatalf("server/client grant mismatch")
	}

	host := "127.0.0.1"

	srvData, err := srt.Dial("srt", fmt.Sprintf("%s:%d", host, gc.SRTPort), srt.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer srvData.Close()
	writeHello(t, srvData, "server", gc.SessionID, "srv1")

	go func() {
		r := bufio.NewReaderSize(srvData, 128*1024)
		for {
			fr, err := video.ReadFrame(r, 8*1024*1024)
			if err != nil {
				return
			}
			if fr.Type != video.MsgData {
				continue
			}
			nowRecv := time.Now().UnixNano()
			nowSend := time.Now().UnixNano()
			_ = video.WriteEchoFrame(srvData, fr.Seq, fr.SentUnixNs, nowRecv, nowSend, fr.Payload)
		}
	}()

	cliData, err := srt.Dial("srt", fmt.Sprintf("%s:%d", host, gc.SRTPort), srt.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer cliData.Close()
	writeHello(t, cliData, "client", gc.SessionID, "cli1")

	go func() {
		f, _ := os.Open("client_test/IMG_4127.MOV")
		defer f.Close()
		buf := make([]byte, 64*1024)
		var seq uint64
		var sentBytes int64
		for sentBytes < 1024*1024 {
			n, err := io.ReadFull(f, buf)
			if err == io.ErrUnexpectedEOF {
				err = io.EOF
			}
			if err == io.EOF && n == 0 {
				return
			}
			seq++
			_ = video.WriteDataFrame(cliData, seq, time.Now(), buf[:n])
			sentBytes += int64(n)
			if err == io.EOF {
				return
			}
		}
	}()

	r := bufio.NewReaderSize(cliData, 128*1024)
	var got int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && got < 5 {
		fr, err := video.ReadFrame(r, 8*1024*1024)
		if err != nil {
			t.Fatal(err)
		}
		if fr.Type == video.MsgEcho {
			got++
		}
	}
	if got == 0 {
		t.Fatalf("no echo frames")
	}

	_ = cliData.Close()
	_ = srvData.Close()

	waitPortStateEqual(t, cliDec, base, 8*time.Second)
}

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
	h := control.NewHub(cfg, rm, zmqPool, srtPool)
	go func() { _ = h.Start(ctx) }()

	time.Sleep(150 * time.Millisecond)

	stop := func() {
		cancel()
		rm.Stop()
	}
	return cfg, stop
}

func dialJSON(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func expectRegisterOK(t *testing.T, dec *json.Decoder) {
	t.Helper()
	env := expectType(t, dec, control.MsgRegisterAck)
	b := mustJSON(env.Payload)
	var ack control.RegisterAckPayload
	_ = json.Unmarshal(b, &ack)
	if ack.Status != "ok" {
		t.Fatalf("register not ok: %+v", ack)
	}
}

func expectType(t *testing.T, dec *json.Decoder, typ control.MessageType) control.Envelope {
	t.Helper()
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			t.Fatal(err)
		}
		if env.Type == typ {
			return env
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func waitPortState(t *testing.T, dec *json.Decoder) control.PortStatePayload {
	t.Helper()
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			t.Fatal(err)
		}
		if env.Type != control.MsgPortState {
			continue
		}
		b := mustJSON(env.Payload)
		var ps control.PortStatePayload
		_ = json.Unmarshal(b, &ps)
		return ps
	}
}

func waitPortStateEqual(t *testing.T, dec *json.Decoder, base control.PortStatePayload, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := waitPortState(t, dec)
		if st.ZMQ.Occupied == base.ZMQ.Occupied &&
			st.ZMQ.Reserved == base.ZMQ.Reserved &&
			st.SRT.Occupied == base.SRT.Occupied &&
			st.SRT.Reserved == base.SRT.Reserved {
			return
		}
	}
	t.Fatalf("port state not back to baseline")
}

func writeHello(t *testing.T, c net.Conn, role, sessionID, id string) {
	t.Helper()
	payload := fmt.Sprintf("%s{\"role\":\"%s\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, role, sessionID, id)
	_, _ = c.Write([]byte(payload))
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
