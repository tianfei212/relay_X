package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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

func (w *frameWriter) WriteData(seq uint64, sent time.Time, payload []byte) error {
	w.mu.Lock()
	err := video.WriteDataFrame(w.c, seq, sent, payload)
	w.mu.Unlock()
	return err
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
	id := flag.String("id", "video-fe-1", "前端(client) ID")
	token := flag.String("token", "t", "前端(client) token")

	filePath := flag.String("file", "client_test/IMG_4127.MOV", "要发送的视频文件路径（作为字节流分片发送）")
	needZMQ := flag.Bool("zmq", false, "是否测试 ZMQ(TCP) 数据端口")
	needSRT := flag.Bool("srt", true, "是否测试 SRT 数据端口")
	chunkKB := flag.Int("chunk_kb", 64, "每帧分片大小（KB），用于“帧率/延迟”统计")
	rateMbps := flag.Float64("rate_mbps", 0, "发送限速（Mbps），0 表示不限制")
	drain := flag.Duration("drain", 1500*time.Millisecond, "发送完成后等待回环数据的时间（用于避免过早断链）")

	baseDelayMS := flag.Int("delay_ms", 0, "基础发送延迟（ms）")
	jitterMaxMS := flag.Int("jitter_ms", 0, "额外抖动延迟上限（ms）")
	dropPct := flag.Int("drop_pct", 0, "随机丢包百分比（0-100），在应用层不发送该帧")

	keepSamples := flag.Bool("keep_samples", false, "是否在报告里保存每帧样本（会显著增大输出）")
	reportFile := flag.String("report", "", "报告输出 JSON 文件路径（为空则只输出到 stdout）")
	maxPayloadMB := flag.Int("max_payload_mb", 32, "最大单帧负载（MB），用于防御异常输入")
	waitRelease := flag.Duration("wait_release", 8*time.Second, "拆链后等待端口池状态回到基线的最长时间")
	flag.Parse()

	if !*needZMQ && !*needSRT {
		fmt.Println("need至少开启一种链路：-zmq 或 -srt")
		return
	}

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	_ = enc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{
		Role:    control.RoleClient,
		ID:      *id,
		Token:   *token,
		MaxConn: 1,
	}})
	env := expect(dec, control.MsgRegisterAck)
	if !isOK(env.Payload) {
		_ = json.NewEncoder(os.Stdout).Encode(env)
		return
	}

	pairGrantCh := make(chan control.PairGrantPayload, 4)
	portStateCh := make(chan control.PortStatePayload, 64)
	errCh := make(chan error, 1)
	go decodeControlLoop(dec, pairGrantCh, portStateCh, errCh)

	baseState, ok := waitPortStateFromLoop(portStateCh, errCh, 5*time.Second)
	if !ok {
		fmt.Println("未能获取端口池基线（port_state），请确认服务端已启动且本连接在持续接收广播")
		return
	}
	fmt.Printf("端口池基线：ZMQ occupied=%d reserved=%d | SRT occupied=%d reserved=%d\n",
		baseState.ZMQ.Occupied, baseState.ZMQ.Reserved, baseState.SRT.Occupied, baseState.SRT.Reserved)

	reqID := fmt.Sprintf("video-%d", time.Now().UnixNano())
	_ = enc.Encode(control.Envelope{Type: control.MsgPairRequest, Payload: control.PairRequestPayload{
		RequestID: reqID,
		NeedZMQ:   *needZMQ,
		NeedSRT:   *needSRT,
	}})

	grant, ok := waitPairGrant(pairGrantCh, errCh, 15*time.Second)
	if !ok {
		fmt.Println("等待配对授权超时/失败，请检查控制面与后端(server)是否在线")
		return
	}

	fmt.Printf("链路计划：session=%s server=%s client=%s zmq=%d srt=%d\n", grant.SessionID, grant.ServerID, grant.ClientID, grant.ZMQPort, grant.SRTPort)

	host, _, _ := net.SplitHostPort(*addr)
	chunkBytes := *chunkKB * 1024
	if chunkBytes <= 0 {
		chunkBytes = 64 * 1024
	}
	maxPayloadBytes := *maxPayloadMB * 1024 * 1024

	sim := video.NewSimulator(time.Now().UnixNano(), time.Duration(*baseDelayMS)*time.Millisecond, time.Duration(*jitterMaxMS)*time.Millisecond, *dropPct)
	rl := video.NewRateLimiter(*rateMbps)

	var reports []video.Report

	if *needZMQ && grant.ZMQPort > 0 {
		r, err := runOne("zmq(tcp)", host, grant.ZMQPort, grant.SessionID, *id, *filePath, chunkBytes, maxPayloadBytes, sim, rl, *drain, *keepSamples)
		if err != nil {
			fmt.Printf("ZMQ(TCP) 测试失败：%v\n", err)
		} else {
			reports = append(reports, r)
		}
	}
	if *needSRT && grant.SRTPort > 0 {
		r, err := runOneSRT("srt", host, grant.SRTPort, grant.SessionID, *id, *filePath, chunkBytes, maxPayloadBytes, sim, rl, *drain, *keepSamples)
		if err != nil {
			fmt.Printf("SRT 测试失败：%v\n", err)
		} else {
			reports = append(reports, r)
		}
	}

	for _, r := range reports {
		out, _ := r.MarshalIndent()
		fmt.Printf("测试报告（%s）：\n%s\n", r.Transport, string(out))
	}

	if *reportFile != "" && len(reports) > 0 {
		if err := os.MkdirAll(filepath.Dir(*reportFile), 0o755); err == nil {
			raw, _ := json.MarshalIndent(reports, "", "  ")
			_ = os.WriteFile(*reportFile, raw, 0o644)
			fmt.Printf("已写入报告：%s\n", *reportFile)
		}
	}

	deadline := time.Now().Add(*waitRelease)
	for time.Now().Before(deadline) {
		st, ok := waitPortStateFromLoop(portStateCh, errCh, 2*time.Second)
		if !ok {
			continue
		}
		if st.ZMQ.Occupied == baseState.ZMQ.Occupied &&
			st.ZMQ.Reserved == baseState.ZMQ.Reserved &&
			st.SRT.Occupied == baseState.SRT.Occupied &&
			st.SRT.Reserved == baseState.SRT.Reserved {
			fmt.Println("链路拆除完成：端口池状态已回到基线（确认缓冲/会话已释放）")
			return
		}
	}
	fmt.Println("链路拆除等待超时：端口池状态未在规定时间回到基线（请检查服务端日志）")
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

func waitPortStateFromLoop(portStateCh <-chan control.PortStatePayload, errCh <-chan error, timeout time.Duration) (control.PortStatePayload, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ps := <-portStateCh:
		return ps, true
	case <-timer.C:
		return control.PortStatePayload{}, false
	case <-errCh:
		return control.PortStatePayload{}, false
	}
}

func waitPairGrant(pairGrantCh <-chan control.PairGrantPayload, errCh <-chan error, timeout time.Duration) (control.PairGrantPayload, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case g := <-pairGrantCh:
		return g, true
	case <-timer.C:
		return control.PairGrantPayload{}, false
	case <-errCh:
		return control.PairGrantPayload{}, false
	}
}

func decodeControlLoop(dec *json.Decoder, pairGrantCh chan<- control.PairGrantPayload, portStateCh chan<- control.PortStatePayload, errCh chan<- error) {
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		switch env.Type {
		case control.MsgPairGrant:
			b, _ := json.Marshal(env.Payload)
			var g control.PairGrantPayload
			_ = json.Unmarshal(b, &g)
			select {
			case pairGrantCh <- g:
			default:
			}
		case control.MsgPortState:
			b, _ := json.Marshal(env.Payload)
			var ps control.PortStatePayload
			_ = json.Unmarshal(b, &ps)
			select {
			case portStateCh <- ps:
			default:
			}
		default:
		}
	}
}

func waitPortState(dec *json.Decoder) control.PortStatePayload {
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return control.PortStatePayload{}
		}
		if env.Type != control.MsgPortState {
			continue
		}
		b, _ := json.Marshal(env.Payload)
		var ps control.PortStatePayload
		_ = json.Unmarshal(b, &ps)
		return ps
	}
}

func writeHello(c net.Conn, role, sessionID, id string) {
	payload := fmt.Sprintf("%s{\"role\":\"%s\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, role, sessionID, id)
	_, _ = c.Write([]byte(payload))
}

func runOne(transport, host string, port int, sessionID, id, filePath string, chunkBytes, maxPayloadBytes int, sim *video.Simulator, rl *video.RateLimiter, drain time.Duration, keepSamples bool) (video.Report, error) {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
	if err != nil {
		return video.Report{}, err
	}
	defer c.Close()
	writeHello(c, "client", sessionID, id)
	return runStream(transport, c, filePath, chunkBytes, maxPayloadBytes, sim, rl, drain, keepSamples)
}

func runOneSRT(transport, host string, port int, sessionID, id, filePath string, chunkBytes, maxPayloadBytes int, sim *video.Simulator, rl *video.RateLimiter, drain time.Duration, keepSamples bool) (video.Report, error) {
	cfg := srt.DefaultConfig()
	c, err := srt.Dial("srt", fmt.Sprintf("%s:%d", host, port), cfg)
	if err != nil {
		return video.Report{}, err
	}
	defer c.Close()
	writeHello(c, "client", sessionID, id)
	return runStream(transport, c, filePath, chunkBytes, maxPayloadBytes, sim, rl, drain, keepSamples)
}

func runStream(transport string, c net.Conn, filePath string, chunkBytes, maxPayloadBytes int, sim *video.Simulator, rl *video.RateLimiter, drain time.Duration, keepSamples bool) (video.Report, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return video.Report{}, err
	}
	defer f.Close()

	col := &video.Collector{Transport: transport, File: filePath, KeepSamples: keepSamples}
	col.Start()

	done := make(chan struct{})
	errCh := make(chan error, 1)
	kaDone := make(chan struct{})
	var kaOnce sync.Once
	closeKA := func() { kaOnce.Do(func() { close(kaDone) }) }
	fw := &frameWriter{c: c}
	go keepaliveLoop(fw, kaDone, col)

	go func() {
		defer close(done)
		r := bufio.NewReaderSize(c, 256*1024)
		for {
			fr, err := video.ReadFrame(r, maxPayloadBytes)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
					closeKA()
					return
				}
				select {
				case errCh <- err:
				default:
				}
				closeKA()
				return
			}
			switch fr.Type {
			case video.MsgEcho:
				col.OnEcho(fr.Seq, fr.SentUnixNs, fr.ServerRecvUnixNs, fr.ServerSendUnixNs, time.Now().UnixNano(), len(fr.Payload))
			case video.MsgPong:
				col.OnEcho(fr.Seq, fr.SentUnixNs, fr.ServerRecvUnixNs, fr.ServerSendUnixNs, time.Now().UnixNano(), 0)
			case video.MsgPing:
				nowRecv := time.Now().UnixNano()
				nowSend := time.Now().UnixNano()
				fw.WritePong(fr.Seq, fr.SentUnixNs, nowRecv, nowSend)
			default:
			}
		}
	}()

	buf := make([]byte, chunkBytes)
	var seq uint64
	for {
		n, rerr := io.ReadFull(f, buf)
		if rerr == io.ErrUnexpectedEOF {
			rerr = io.EOF
		}
		if rerr == io.EOF && n == 0 {
			break
		}
		payload := append([]byte(nil), buf[:n]...)

		if sim != nil && sim.ShouldDrop() {
			continue
		}
		if sim != nil {
			time.Sleep(sim.NextDelay())
		}
		if rl != nil {
			rl.Wait(n)
		}

		seq++
		sent := time.Now()
		if err := fw.WriteData(seq, sent, payload); err != nil {
			col.Finish()
			return video.Report{}, err
		}
		col.OnSend(n)
		if rerr == io.EOF {
			break
		}
	}

	if drain > 0 {
		time.Sleep(drain)
	}
	closeKA()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_ = c.Close()

	select {
	case <-done:
	case err := <-errCh:
		col.Finish()
		return video.Report{}, err
	case <-time.After(3 * time.Second):
	}

	col.Finish()
	return col.BuildReport(), nil
}

func keepaliveLoop(w *frameWriter, done <-chan struct{}, col *video.Collector) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	var seq uint64
	for {
		select {
		case <-done:
			return
		case <-t.C:
			seq++
			sent := time.Now()
			err := w.WritePing(seq, sent)
			if err == nil {
				if col != nil {
					col.OnSend(0)
				}
				continue
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
	}
}
