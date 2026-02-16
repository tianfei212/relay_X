# relay_X（V4 / 1.4）

这是一个包含“控制面 TCP + JSON + 数据面端口中转”的网关程序，用于在 FE（前端/client）与 BE（后端/server）之间进行端口协商与数据转发。

本仓库当前对外版本以 **V4 / 1.4** 为准（核心实现位于 [v4](file:///home/ubuntu/codes/go_work/relay_X/v4)，入口为 [relay-server-v4](file:///home/ubuntu/codes/go_work/relay_X/cmd/relay-server-v4/main.go)）。

## 目录结构（V4 相关）

- [cmd/relay-server-v4](file:///home/ubuntu/codes/go_work/relay_X/cmd/relay-server-v4)：网关启动入口（支持 `--config_path/--version/--help`）
- [cmd/bin](file:///home/ubuntu/codes/go_work/relay_X/cmd/bin)：构建产物与推荐配置示例
- [v4/config](file:///home/ubuntu/codes/go_work/relay_X/v4/config)：V4 配置结构与加载
- [v4/control](file:///home/ubuntu/codes/go_work/relay_X/v4/control)：控制面协议、注册/配对、状态与遥测广播
- [v4/ports](file:///home/ubuntu/codes/go_work/relay_X/v4/ports)：端口池（Idle/Reserved/Occupied/Blocked）与会话分配
- [v4/relay](file:///home/ubuntu/codes/go_work/relay_X/v4/relay)：数据面（ZMQ/TCP + SRT/UDP）监听、握手、配对与转发
- [v4/testing/video](file:///home/ubuntu/codes/go_work/relay_X/v4/testing/video)：视频压测用二进制帧协议（示例协议）
- [v4/testing/video_be](file:///home/ubuntu/codes/go_work/relay_X/v4/testing/video_be)：BE 回环模拟器（用于验证）
- [v4/testing/video_fe](file:///home/ubuntu/codes/go_work/relay_X/v4/testing/video_fe)：FE 压测客户端（用于验证）

## 环境要求

- Go：以 [go.mod](file:///home/ubuntu/codes/go_work/relay_X/go.mod) 为准
- OS：Ubuntu 24.04（linux/amd64）已验证
- 端口：
  - 控制面：`gateway.tcp_port`（默认 5555，同时支持 HTTP GET /status）
  - 数据面（TCP）：`zeromq.port_range`
  - 数据面（SRT/UDP）：`gosrt.port_range`

## 快速开始

### 1) 直接运行（推荐使用 cmd/bin 内产物）

```bash
./cmd/bin/relay-server-v4 --config_path ./cmd/bin/config.yaml
```

查看版本与帮助：

```bash
./cmd/bin/relay-server-v4 --version
./cmd/bin/relay-server-v4 --help
```

### 2) 源码运行

```bash
go run ./cmd/relay-server-v4 --config_path ./configs/config.yaml
```

## 配置参数（V4 / 1.4）

配置结构定义在 [Config](file:///home/ubuntu/codes/go_work/relay_X/v4/config/config.go)。

### gateway（控制面）

- `tcp_port`：控制面监听端口（TCP），同时复用该端口支持 `GET /status`
- `max_connections`：控制面最大在线连接数（达到后拒绝新连接）
- `auth_timeout`：控制面注册阶段超时；同时用于数据面端口 reservation 的过期回收窗口
- `shutdown_timeout`：优雅退出等待时间

### zeromq（数据面：ZMQ(TCP)）

注意：这里的 “ZMQ” 是历史命名，实际是 **TCP 中转通道**，不要求 ZeroMQ runtime。

- `port_range`：TCP 数据端口范围（例如 `"2300-2320"`）
- `sndhwm/rcvhwm`：队列水位（用于限制极端情况下的队列堆积）
- `sndbuf/rcvbuf`：TCP socket 读写 buffer（字节）
- `linger`：TCP Linger（秒）；`0` 表示 close 时丢弃未发送数据并快速释放
- `reconnect_ivl`：客户端侧重连间隔（仅用于测试/工具约定）

### gosrt（数据面：SRT/UDP）

SRT 基于 UDP，网关使用 GoSRT（`github.com/datarhei/gosrt`）监听并转发。

- `port_range`：SRT 端口范围（例如 `"2350-2380"`）
- `latency`：SRT Latency（ms），会显著影响端到端 RTT（越大越抗抖动，延迟也越高）
- `maxbw`：SRT MaxBW（bps），`0` 为不限
- `ipttl`：IP TTL

### logging（日志）

- `level`：日志等级（info/debug/...）
- `format`：日志格式（json）
- `output`：输出位置（console/file）
- `file_path`：日志文件路径
- `max_size/max_age/compress`：滚动策略

仓库内提供面向 2C/4G/200Mbps 的示例配置：
- [cmd/bin/config.yaml](file:///home/ubuntu/codes/go_work/relay_X/cmd/bin/config.yaml)

## 控制面协议（TCP + JSON）

代码定义： [protocol.go](file:///home/ubuntu/codes/go_work/relay_X/v4/control/protocol.go)

消息统一为：

```json
{ "type": "xxx", "payload": { } }
```

### 1) register（首条消息必须是 register）

客户端连接建立后，首条消息必须是：

```json
{
  "type": "register",
  "payload": {
    "role": "server|client",
    "id": "string",
    "token": "string",
    "max_conn": 100
  }
}
```

返回：

```json
{ "type": "register_ack", "payload": { "status": "ok" } }
```

约束：
- `role` 仅支持 `server` / `client`
- `id/token` 必须非空
- `id` 需全局唯一（重复会返回 503/409 类错误）
- `max_conn` 仅对 `server` 有意义，表示该 server 能承载的并发会话上限

### 2) pair_request / pair_grant（端口协商）

仅 `role=client` 可以发起：

```json
{
  "type": "pair_request",
  "payload": {
    "request_id": "r1",
    "server_id": "可选：指定 server",
    "need_zmq": true,
    "need_srt": true,
    "qos": {}
  }
}
```

网关返回 `pair_grant` 给 client，同时也会把同一份 grant 发送给对应的 server：

```json
{
  "type": "pair_grant",
  "payload": {
    "request_id": "r1",
    "session_id": "会话ID",
    "server_id": "server-1",
    "client_id": "client-1",
    "zmq_port": 2300,
    "srt_port": 2350
  }
}
```

说明：
- `session_id` 是数据面握手的关键字段
- `need_zmq/need_srt` 可单独启用其中一种，或两种都启用

### 3) /status（HTTP 健康检查）

同一个 `gateway.tcp_port` 上支持：

```bash
curl http://127.0.0.1:5555/status
```

返回结构： [HealthStatusPayload](file:///home/ubuntu/codes/go_work/relay_X/v4/control/protocol.go#L103-L110)

## 数据面协议（ZMQ/TCP 与 SRT/UDP）

数据面端口由 `pair_grant` 下发，FE 与 BE 需要分别连接到对应端口，并在连接建立后发送首行握手：

代码定义： [hello.go](file:///home/ubuntu/codes/go_work/relay_X/v4/relay/hello.go)

### 1) 首行握手（必须）

```text
V4HELLO {"role":"server|client","session_id":"...","id":"..."}\n
```

- `role`：数据面角色（`server` 或 `client`）
- `session_id`：必须与控制面 `pair_grant.session_id` 一致，否则连接被拒绝（alloc_mismatch）
- `id`：用于日志标识（通常填 server_id / client_id）

### 2) 握手后数据内容

握手完成后，网关进入双向转发。后续 payload 对网关而言是透明字节流，协议由业务自行定义。

本仓库提供了一个用于压测/验证的“视频二进制帧协议”（示例协议，便于统计 RTT/吞吐/丢包）：
- 说明文档：[video/README.md](file:///home/ubuntu/codes/go_work/relay_X/v4/testing/video/README.md)
- 协议实现：[proto.go](file:///home/ubuntu/codes/go_work/relay_X/v4/testing/video/proto.go)

其帧格式要点（big-endian）：
- magic：`0x56345654`
- version：`1`
- type：`data/echo/ping/pong`
- seq：`uint64`
- sent_unix_ns / server_recv_unix_ns / server_send_unix_ns：时间戳字段
- crc32 + payload_len + payload

## 联调与压测（可选）

1) 启动网关：

```bash
go run ./cmd/relay-server-v4 --config_path ./configs/config.yaml
```

2) 启动 BE（server）：

```bash
go run ./v4/testing/video_be -addr 127.0.0.1:5555 -id video-be-1 -token t -max 100
```

3) 启动 FE（client）：

```bash
go run ./v4/testing/video_fe -addr 127.0.0.1:5555 -id video-fe-1 -token t -srt=true -zmq=true -drain 2s -delay_ms 20 -jitter_ms 50 -rate_mbps 20
```

## 测试

```bash
go test ./...
```
