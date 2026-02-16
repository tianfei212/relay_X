# JOJO-Relay-X 前端开发指导（控制面客户端/业务侧接入）

> 适用范围：本文面向“业务侧接入方”（文档中称 FE/前端），负责连接网关控制面、申请数据面端口并建立 FE↔BE 会话。  
> 注意：数据面在 v1 中是 TCP 端口中转（`srt/zmq` 只是端口池的协议标签）；在 v2 中 `zmq` 仍为 TCP 中转，而 `srt` 为真实 SRT/UDP（GoSRT）。浏览器环境无法直接建立原生 TCP 或 SRT/UDP 连接；如果你的“前端”是 Web 页面，需要额外的本地/边缘代理把 WebSocket/HTTP/WebRTC 等转换为 TCP/SRT，再按本文对接网关。

## 协议速查（必读）

| 通道 | 端口来源 | v1 实际协议 | v2 实际协议 | 特别说明 |
|---|---|---|---|---|
| 控制面 | `relay.control_port` | TCP + JSON 流 | TCP + JSON 流 | 固定为 TCP |
| 数据面（ZMQ） | `port_grant.zmq_port` | TCP 字节流（pipe 中转） | TCP 字节流（pipe 中转） | 网关不实现 ZeroMQ 协议栈，但 v2 对该端口做纯字节透传，可承载 ZMTP（ZeroMQ） |
| 数据面（SRT） | `port_grant.srt_port` | TCP 字节流（pipe 中转） | SRT/UDP（GoSRT）+ pipe 中转 | v2 需要放通 UDP；v2 支持纯字节透传（不强制 `RLX1HELLO`） |

关键入口与参考实现：

- 控制面协议类型与载荷：[protocol.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/protocol.go)
- 控制面服务端注册/鉴权与消息推送：[hub.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/hub.go)
- 数据面 HELLO 握手、配对与中转（v1 TCP）：[server.go](file:///home/ubuntu/codes/go_work/relay_X/v1/relay/server.go)
- 数据面 HELLO 握手、配对与中转（v2 SRT/UDP）：[server.go](file:///home/ubuntu/codes/go_work/relay_X/v2/relay/server.go)
- 可直接复用的 Go 客户端参考实现：[test_client.go](file:///home/ubuntu/codes/go_work/relay_X/client_test/test_client.go)
- 后端整体说明与运维侧建议：[backend_dev_guide.md](file:///home/ubuntu/codes/go_work/relay_X/docs/backend_dev_guide.md)

## 目录

- [0. 快速开始（最小可用客户端）](#0-快速开始最小可用客户端)
- [1. 基本概念（FE/BE、控制面/数据面、端口状态）](#1-基本概念febe控制面数据面端口状态)
- [2. 控制面接入（TCP + JSON 流）](#2-控制面接入tcp--json-流)
- [3. 端口租用协议（acquire/grant/release）](#3-端口租用协议acquiregrantrelease)
- [4. 数据面接入（连接、HELLO、会话）](#4-数据面接入连接hello会话)
- [5. 可靠性（超时、重连、幂等与降级）](#5-可靠性超时重连幂等与降级)
- [6. 安全建议（现状边界与加固路径）](#6-安全建议现状边界与加固路径)
- [7. 排障手册（常见错误与定位）](#7-排障手册常见错误与定位)
- [附录 A：消息示例速查](#附录-a消息示例速查)

---

## 0. 快速开始（最小可用客户端）

### 0.1 你需要准备的参数

- `gateway_host`: 网关地址（IP/域名）
- `control_port`: 控制面端口（默认 5555，见 [configs/config.yaml](file:///home/ubuntu/codes/go_work/relay_X/configs/config.yaml)）
- `auth_secret`: 注册鉴权密钥（与网关 `.env.local.json` 的 `auth_secret` 严格相等）

### 0.2 最小流程（必须按顺序）

1) 连接控制面 `tcp://gateway_host:control_port`  
2) 第一条消息必须发送 `register`（否则会被断开）  
3) 等待 `auth` 返回成功  
4) 等待网关下发 `port_alloc` 与首个 `port_state`（FE 注册成功后会主动下发）  
5) 发送 `port_acquire` 申请端口  
6) 收到 `port_grant` 后连接数据端口（建议 BE 先连、FE 后连；v2 的 `zmq/srt` 端口不强制发送 HELLO）  
7) 开始按业务协议收发数据（网关只做二进制透传，不理解你的业务内容）  
8) 会话结束发送 `port_release` 回收租约

如果 BE 没有对某个端口做“预连接”，该端口状态会是 `empty`，FE 连接数据端口会被拒绝。FE 侧应该优先从 `ready_ports` 选择端口，或者直接依赖网关的分配策略（网关会尽量从 ready 池分配，详见 [port_manager.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/port_manager.go)）。

---

## 1. 基本概念（FE/BE、控制面/数据面、端口状态）

### 1.1 两类连接：控制面 vs 数据面

- 控制面：一条 TCP 长连接，发送/接收 JSON 消息（注册、鉴权、租用端口、心跳、状态推送、日志推送）
- 数据面：连接到被授予的端口，网关把 FE 和 BE 的两条数据连接配对后做双向中转（v1：`srt/zmq` 均为 TCP；v2：`zmq` 为 TCP，`srt` 为 SRT/UDP）

### 1.2 FE 与 BE 的职责分工（以当前实现为准）

- FE（业务侧接入方）：请求端口、按需建立数据面连接、结束时释放
- BE（后端服务/处理侧）：通常需要维持一定数量的“预连接”，把端口从 `empty` 拉到 `ready`，以减少 FE 等待

### 1.3 端口状态：empty / ready / busy

网关会通过 `port_state` 推送每类端口的总数、busy 数量，以及 ready/empty 的端口列表：

- `empty`：端口在监听，但没有 BE 预连接
- `ready`：端口在监听，且已有 BE 预连接等待配对
- `busy`：端口已配对进入会话，拒绝新连接

状态来源与统计逻辑见 [server.go](file:///home/ubuntu/codes/go_work/relay_X/v1/relay/server.go)。

---

## 2. 控制面接入（TCP + JSON 流）

### 2.1 传输与分帧

控制面是一条 TCP 连接上的 JSON 对象流：

- 发送端按消息写入 JSON（参考实现使用 `json.Encoder.Encode`，每条后附带换行）
- 接收端连续解析 JSON 对象（参考实现使用 `json.Decoder.Decode`）

你可以把它当作“JSON Lines over TCP”，每行一个 JSON 对象。

### 2.2 首包必须是 register

服务端硬要求首条消息为 `register`，否则直接断开，逻辑见 [hub.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/hub.go#L182-L199)。

`register` 载荷：

- `role`: `"FE"` 或 `"BE"`
- `secret`: 鉴权密钥（严格相等）
- `client_id`: 可选，建议接入方填入稳定标识，便于定位问题

### 2.3 消息类型速览（FE 必用）

消息类型与字段定义以 [protocol.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/protocol.go) 为准。FE 侧必须实现：

- `register`（请求）
- `auth`（响应）
- `port_alloc`（推送）
- `port_state`（推送）
- `port_acquire`（请求）
- `port_grant`（响应/推送）
- `port_release`（请求）

建议实现但可选：

- `ping`/`pong`（心跳与 RTT 估计）
- `log`（网关日志流化推送，当前 payload 通常为字符串，见 [stream.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/stream.go)）
- `telemetry`（遥测；当前实现主要给出 `current_speed`）

### 2.4 推荐的读写模型

避免“同步请求阻塞接收循环”。推荐结构：

- 单独的接收循环：持续读取控制面 JSON，把不同类型投递到 channel/队列
- 发送侧加互斥或单线程：确保同一连接上的写入不被并发打乱
- 建立请求-响应关联：`port_acquire.request_id` 与 `port_grant.request_id` 配对

Go 参考实现可直接对照 [test_client.go](file:///home/ubuntu/codes/go_work/relay_X/client_test/test_client.go#L179-L312) 的 `send`/`startListening` 模式。

### 2.5 Node.js（或其他语言）实现要点

#### 2.5.1 Node.js 最小控制面示例（JSON Lines over TCP）

下面示例仅展示“连接、register、读消息分发、发送 port_acquire”的骨架。生产使用时请补齐：重连、超时、request_id 关联、并发写保护。

```js
import net from "node:net"

function encodeLine(obj) {
  return Buffer.from(JSON.stringify(obj) + "\n", "utf8")
}

function connectControl({ host, port, secret, clientId }) {
  const sock = net.createConnection({ host, port })
  sock.setNoDelay(true)

  let buf = ""
  sock.on("data", (chunk) => {
    buf += chunk.toString("utf8")
    for (;;) {
      const i = buf.indexOf("\n")
      if (i < 0) break
      const line = buf.slice(0, i).trim()
      buf = buf.slice(i + 1)
      if (!line) continue
      const msg = JSON.parse(line)
      onMessage(msg)
    }
  })

  sock.on("connect", () => {
    sock.write(encodeLine({
      type: "register",
      payload: { role: "FE", secret, client_id: clientId }
    }))
  })

  function send(msg) {
    sock.write(encodeLine(msg))
  }

  function onMessage(msg) {
    if (msg.type === "auth") return
    if (msg.type === "port_alloc") return
    if (msg.type === "port_state") return
    if (msg.type === "port_grant") return
    if (msg.type === "log") return
  }

  return { sock, send }
}
```

#### 2.5.2 其他语言的关键点（避免踩坑）

- 分帧：推荐按“每条消息一个 JSON + `\n`”实现（便于抓包与调试）；接收端按行切分再 JSON 解析
- 写并发：同一 TCP 连接上的写入需要串行化，避免消息交错
- 接收循环：必须持续读控制面，否则会错过 `port_state` 与 `port_grant`

---

## 3. 端口租用协议（acquire/grant/release）

### 3.1 port_acquire：申请端口

`port_acquire` 载荷（见 [protocol.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/protocol.go#L55-L60)）：

- `request_id`: 建议必填；用于幂等与关联响应
- `need_zmq` / `need_srt`: 本次会话需要哪类端口
- `ttl_seconds`: 可选；用于表达“租约期望”。当前实现侧更偏向“对接方自我约束”，不要无限持有

实践建议：

- `request_id` 使用 UUID + unix_ms 拼接，便于定位（与后端建议一致）
- 若业务一次会话需要两类端口，建议一次性 `need_zmq=true` 且 `need_srt=true`，减少两次申请引入的竞态

### 3.2 port_grant：授予端口或返回错误

`port_grant` 载荷（见 [protocol.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/protocol.go#L62-L69)）：

- `session_id`: 必有；后续 `port_release` 依赖
- `zmq_port` / `srt_port`: 根据请求返回
- `expires_at`: 过期时间戳（单位以实现为准，参考实现中用于提示客户端不要长期占用）
- `error`: 非空表示失败

FE 侧处理规则：

- 若 `error` 非空：按可重试错误处理（通常需要等待 `port_state` 有足够 ready，再重试）
- 若端口为 0：表示该类端口未授予（与 `need_*` 相匹配）

### 3.3 port_release：释放租约

会话结束必须发送 `port_release`，载荷只需：

- `session_id`

建议在以下场景也释放：

- 数据面连接失败（无法建立或 HELLO 超时）
- 业务侧主动取消/超时
- 发生不可恢复错误，准备重新 `port_acquire`

---

## 4. 数据面接入（连接、HELLO、会话）

### 4.1 连接目标

`port_grant` 返回的端口属于网关数据面监听端口，连接目标为：

- `tcp://gateway_host:zmq_port`
- `srt_port`：
  - v1：`tcp://gateway_host:srt_port`
  - v2：`srt://gateway_host:srt_port`（SRT/UDP）

### 4.2 HELLO 握手（2 秒硬约束）

连接建立后，客户端必须在 2 秒内发送一行（以换行结尾）：

```text
RLX1HELLO {"role":"FE","client_id":"fe-01"}\n
```

服务端实现见 [server.go](file:///home/ubuntu/codes/go_work/relay_X/v1/relay/server.go#L349-L376)（v1）与 [server.go](file:///home/ubuntu/codes/go_work/relay_X/v2/relay/server.go#L387-L414)（v2）。

约束：

- `role` 只能是 `"FE"` 或 `"BE"`
- 必须是一行文本（换行结束），否则服务端会超时断开

### 4.3 会话建立与中转语义

当前实现的配对模型：

- 同一数据端口上，先到的 BE 连接被标记为 pending（端口进入 ready）
- FE 连接同一端口并通过 HELLO 后，网关把两条连接配对并进入中转（端口进入 busy）

因此 FE 侧最常见的失败原因是：

- 对空端口（empty）发起连接：没有 pending BE，会被拒绝或无法配对

### 4.4 数据透传与边界

网关对数据面内容完全透明：

- 只负责 FE↔BE 双向字节中转
- 不理解你的业务协议，不做拆包/粘包处理

业务协议需要自行定义“帧边界”（例如长度前缀、分隔符、固定包头等），并保证在 FE/BE 两端一致。

---

## 5. 可靠性（超时、重连、幂等与降级）

### 5.1 推荐超时

结合当前实现的硬限制与常见网络抖动，FE 侧建议：

- 控制面 Dial 超时：1s~3s
- 控制面读超时：按心跳周期 3 倍设置（例如 ping=1s，则读超时 3s）
- 数据面 Dial 超时：1s~3s
- 数据面 HELLO：连接成功后立刻发送，不要等待任何业务数据

### 5.2 重连策略

建议采用“指数退避 + 抖动”的重连策略：

- 初始 50ms~200ms
- 上限 2s~5s
- 叠加随机抖动，避免雪崩

重连后的关键点：

- 控制面重连成功后要重新 `register` 与等待 `auth`
- 旧的 `session_id` 不应复用，建议重新 `port_acquire`

### 5.3 幂等与 request_id

`port_acquire` 本质是“分配资源”的请求，网络抖动可能导致：

- 请求已到达但响应丢失
- 客户端重试导致重复分配

接入方建议策略：

- 为每次申请生成唯一 `request_id`
- 在本地记录 `request_id -> (状态, session_id, expires_at)`，并限制重试速率
- 若你需要更强的一致性，建议在业务侧加一层“会话编排器”做去重与回收

### 5.4 降级建议（在端口紧张时）

- 仅申请一种端口（`need_srt=false` 或 `need_zmq=false`），让业务回落到单通道
- 降低并发，等待 `port_state` 的 ready 数恢复
- 缩短业务会话时长，及时 `port_release`

---

## 6. 安全建议（现状边界与加固路径）

### 6.1 现状边界（必须明确）

- 控制面鉴权是 `auth_secret` 的严格相等比较（无签名/无 TLS）
- `relay_public_key` 当前仅加载，未用于签名/验签（见后端指导说明）
- 数据面是明文 TCP 中转（无加密、无完整性校验）

因此生产环境至少需要：

- 网关部署在可信内网或通过防火墙白名单限制来源
- 控制面与数据面做传输加密（在网关前置 TLS 终止或隧道）

### 6.2 推荐加固路径（不改变现有协议也能做）

- 控制面：在网关前置 TCP TLS 代理（stunnel、Nginx stream、Envoy 等），对外暴露 TLS 端口，对内仍是 TCP
- 数据面：同样使用 TCP TLS 代理，或在业务协议层增加加密与认证
- 密钥管理：`auth_secret` 不进代码仓库，使用 Secret/环境注入

---

## 7. 排障手册（常见错误与定位）

### 7.1 连接后立刻断开

常见原因：

- 首条消息不是 `register`
- `auth_secret` 不匹配
- FE/BE 连接数超过 `relay.max_fe/max_be`

定位入口：网关日志（也可能通过 `log` 消息推送给客户端）。

### 7.2 能拿到 port_grant，但数据面连接失败

常见原因：

- 防火墙未放通数据端口段
- 连接到了 0 端口（未检查 `zmq_port/srt_port`）
- 数据面 HELLO 未在 2 秒内发送或格式错误

### 7.3 数据面连接成功但无法配对/无数据

常见原因：

- 连接到 `empty` 端口：BE 未预连接
- 业务协议双方不一致（帧边界不同、编码不同）
- FE 提前关闭导致会话被回收

---

## 附录 A：消息示例速查

### A.1 register

```json
{"type":"register","payload":{"role":"FE","secret":"REPLACE_ME","client_id":"fe-01"}}
```

### A.2 port_acquire

```json
{"type":"port_acquire","payload":{"request_id":"<uuid>-<unix_ms>","need_zmq":true,"need_srt":true,"ttl_seconds":30}}
```

### A.3 port_grant

```json
{"type":"port_grant","payload":{"request_id":"<uuid>-<unix_ms>","session_id":"sess_xxx","zmq_port":30110,"srt_port":30010,"expires_at":1739577630000,"error":""}}
```

### A.4 port_release

```json
{"type":"port_release","payload":{"session_id":"sess_xxx"}}
```
