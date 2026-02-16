# V4 视频回环测试（SRT / ZMQ(TCP)）

本目录提供一套“端到端”联调/压测工具，用于：

- 通过控制面完成：注册 -> 链路计划 -> 配对 -> 建链
- 通过数据面完成：视频文件字节流发送 -> 服务端回环(echo) -> 客户端统计
- 拆链后验证：端口池 occupied/reserved 回到基线（确认会话/缓冲已释放）

说明：

- 目前仓库内的 “ZMQ/ZeroMQ” 为 **TCP 数据端口类型标签**（未接入真实 libzmq）。工具中 `-zmq` 表示走 **TCP 端口**。
- TurboJPEG/libjpeg-turbo 当前未作为硬依赖引入；本测试先聚焦链路抖动/延迟/速率/吞吐统计与端口回收验证。

## 1. 启动顺序

### 1) 启动 V4 服务端

```bash
go run ./cmd/relay-server-v4
```

健康检查：

```bash
curl -s http://127.0.0.1:5555/status
```

### 2) 启动视频回环后端（BE，RoleServer）

```bash
go run ./v4/testing/video_be -addr 127.0.0.1:5555 -id video-be-1 -token t -max 100
```

### 3) 启动视频测试前端（FE，RoleClient）

SRT 发送本地视频文件（默认路径为 `client_test/IMG_4127.MOV`，该文件不随仓库分发，请自行准备）并统计报告：

```bash
go run ./v4/testing/video_fe -addr 127.0.0.1:5555 -id video-fe-1 -token t -srt=true -zmq=false
```

同时测试 TCP(ZMQ 端口) + SRT：

```bash
go run ./v4/testing/video_fe -addr 127.0.0.1:5555 -id video-fe-1 -token t -srt=true -zmq=true
```

## 2. 抖动/延迟/限速模拟

在发送侧注入模拟：

- `-delay_ms`：基础发送延迟
- `-jitter_ms`：额外抖动上限（随机 0~jitter）
- `-drop_pct`：随机丢帧百分比（应用层不发送该帧）
- `-rate_mbps`：发送限速（Mbps）

示例：模拟 20ms 基础延迟 + 0~50ms 抖动，限速 20Mbps：

```bash
go run ./v4/testing/video_fe -addr 127.0.0.1:5555 -srt=true -delay_ms 20 -jitter_ms 50 -rate_mbps 20
```

## 3. 指标与报告说明

前端会输出每条链路的 JSON 报告（示例字段）：

- `goodput_mbps_up/down`：上行/下行有效吞吐（按发送/回环字节数计算）
- `rtt_ms_*`：按“分片帧”的往返时延分位数/均值/标准差
- `server_proc_ms_*`：服务端处理耗时（recv->send）分位数
- `sent_frames/echo_frames`：发送/回环的分片帧数量（可用于近似帧率 = frames / duration）

如需保留每帧样本（用于离线分析）：

```bash
go run ./v4/testing/video_fe -addr 127.0.0.1:5555 -srt=true -keep_samples=true -report /tmp/video_report.json
```

## 4. 自动化测试

仓库内包含一个轻量端到端测试（默认发送文件前 1MB 并验证端口池回收）：

```bash
go test ./v4/testing -run TestVideoE2ESRTFileStream -v
```
