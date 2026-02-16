package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"

	"relay-x/v4/control"
	"relay-x/v4/relay"
	"relay-x/v4/testing/video"
)

const (
	keepaliveInterval = 1 * time.Second
	keepaliveTimeout  = 200 * time.Millisecond
)

type frameWriter struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *frameWriter) WriteEcho(seq uint64, sentUnixNs, serverRecvUnixNs, serverSendUnixNs int64, payload []byte) {
	w.mu.Lock()
	_ = video.WriteEchoFrame(w.c, seq, sentUnixNs, serverRecvUnixNs, serverSendUnixNs, payload)
	w.mu.Unlock()
}

func (w *frameWriter) WritePong(seq uint64, sentUnixNs, serverRecvUnixNs, serverSendUnixNs int64) {
	w.mu.Lock()
	_ = video.WritePongFrame(w.c, seq, sentUnixNs, serverRecvUnixNs, serverSendUnixNs)
	w.mu.Unlock()
}

func (w *frameWriter) WritePing(seq uint64, sent time.Time) error {
	w.mu.Lock()
	_ = w.c.SetWriteDeadline(sent.Add(keepaliveTimeout))
	err := video.WritePingFrame(w.c, seq, sent)
	_ = w.c.SetWriteDeadline(time.Time{})
	w.mu.Unlock()
	return err
}

func main() {
	addr := flag.String("addr", "127.0.0.1:5555", "控制面地址 host:port")
	id := flag.String("id", "video-be-1", "后端(server) ID")
	token := flag.String("token", "t", "后端(server) token")
	maxConn := flag.Int("max", 100, "最大会话数")
	maxPayloadMB := flag.Int("max_payload_mb", 32, "最大单帧负载（MB），用于防御异常输入")
	flag.Parse()

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	_ = enc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{
		Role:    control.RoleServer,
		ID:      *id,
		Token:   *token,
		MaxConn: *maxConn,
	}})
	env := expect(dec, control.MsgRegisterAck)
	if !isOK(env.Payload) {
		_ = json.NewEncoder(os.Stdout).Encode(env)
		return
	}

	host, _, _ := net.SplitHostPort(*addr)
	maxPayloadBytes := *maxPayloadMB * 1024 * 1024

	var wg sync.WaitGroup
	for {
		var e control.Envelope
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.Type != control.MsgPairGrant {
			continue
		}
		var grant control.PairGrantPayload
		b, _ := json.Marshal(e.Payload)
		_ = json.Unmarshal(b, &grant)

		fmt.Printf("收到配对授权 session=%s zmq=%d srt=%d client=%s\n", grant.SessionID, grant.ZMQPort, grant.SRTPort, grant.ClientID)

		if grant.ZMQPort > 0 {
			wg.Add(1)
			go func(port int, sessionID string) {
				defer wg.Done()
				runTCPEchoLoop(host, port, sessionID, *id, maxPayloadBytes)
			}(grant.ZMQPort, grant.SessionID)
		}
		if grant.SRTPort > 0 {
			wg.Add(1)
			go func(port int, sessionID string) {
				defer wg.Done()
				runSRTEchoLoop(host, port, sessionID, *id, maxPayloadBytes)
			}(grant.SRTPort, grant.SessionID)
		}
	}
	wg.Wait()
}

func expect(dec *json.Decoder, typ control.MessageType) control.Envelope {
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			panic(err)
		}
		if env.Type == typ {
			return env
		}
	}
}

func isOK(payload any) bool {
	b, _ := json.Marshal(payload)
	var ack control.RegisterAckPayload
	_ = json.Unmarshal(b, &ack)
	return ack.Status == "ok"
}

func writeHello(c net.Conn, role, sessionID, id string) {
	payload := fmt.Sprintf("%s{\"role\":\"%s\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, role, sessionID, id)
	_, _ = c.Write([]byte(payload))
}

func runTCPEchoLoop(host string, port int, sessionID, id string, maxPayloadBytes int) {
	for {
		if err := runTCPEchoOnce(host, port, sessionID, id, maxPayloadBytes); err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return
	}
}

func runTCPEchoOnce(host string, port int, sessionID, id string, maxPayloadBytes int) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
	if err != nil {
		fmt.Printf("TCP 连接失败 port=%d err=%v\n", port, err)
		return err
	}
	defer c.Close()
	writeHello(c, "server", sessionID, id)

	r := bufio.NewReaderSize(c, 128*1024)
	done := make(chan struct{})
	fw := &frameWriter{c: c}
	go keepaliveLoop(fw, done)
	for {
		f, err := video.ReadFrame(r, maxPayloadBytes)
		if err != nil {
			close(done)
			return err
		}
		switch f.Type {
		case video.MsgData:
			nowRecv := time.Now().UnixNano()
			nowSend := time.Now().UnixNano()
			fw.WriteEcho(f.Seq, f.SentUnixNs, nowRecv, nowSend, f.Payload)
		case video.MsgPing:
			nowRecv := time.Now().UnixNano()
			nowSend := time.Now().UnixNano()
			fw.WritePong(f.Seq, f.SentUnixNs, nowRecv, nowSend)
		default:
		}
	}
}

func runSRTEchoLoop(host string, port int, sessionID, id string, maxPayloadBytes int) {
	for {
		if err := runSRTEchoOnce(host, port, sessionID, id, maxPayloadBytes); err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return
	}
}

func runSRTEchoOnce(host string, port int, sessionID, id string, maxPayloadBytes int) error {
	cfg := srt.DefaultConfig()
	c, err := srt.Dial("srt", fmt.Sprintf("%s:%d", host, port), cfg)
	if err != nil {
		fmt.Printf("SRT 连接失败 port=%d err=%v\n", port, err)
		return err
	}
	defer c.Close()
	writeHello(c, "server", sessionID, id)

	r := bufio.NewReaderSize(c, 128*1024)
	done := make(chan struct{})
	fw := &frameWriter{c: c}
	go keepaliveLoop(fw, done)
	for {
		f, err := video.ReadFrame(r, maxPayloadBytes)
		if err != nil {
			close(done)
			return err
		}
		switch f.Type {
		case video.MsgData:
			nowRecv := time.Now().UnixNano()
			nowSend := time.Now().UnixNano()
			fw.WriteEcho(f.Seq, f.SentUnixNs, nowRecv, nowSend, f.Payload)
		case video.MsgPing:
			nowRecv := time.Now().UnixNano()
			nowSend := time.Now().UnixNano()
			fw.WritePong(f.Seq, f.SentUnixNs, nowRecv, nowSend)
		default:
		}
	}
}

func keepaliveLoop(w *frameWriter, done <-chan struct{}) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	var seq uint64
	for {
		select {
		case <-done:
			return
		case <-t.C:
			seq++
			err := w.WritePing(seq, time.Now())
			if err == nil {
				continue
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
	}
}
