# relay_X（V1.0）

这是一个包含“控制面 TCP+JSON + 数据面端口中转”的网关程序，用于在 FE（前端）与 BE（后端）之间进行端口协商与数据转发。

当前实现说明（非常重要）：

- 控制面：TCP 长连接 + JSON 消息（首条必须 `register`，并用 `auth_secret` 做严格相等鉴权）
- 数据面：按端口 TCP 中转（`srt/zmq` 目前仅作为“端口协议标签”，并非真实接入 libsrt / ZeroMQ）
- 内存：数据转发使用可复用 buffer pool + 会话 ring buffer 的方式降低拷贝与 GC 压力

代码版本按目录定为 **V1.0**（核心实现位于 [v1](file:///home/ubuntu/codes/go_work/relay_X/v1)）。

---

## 目录结构

- [cmd/relay-server](file:///home/ubuntu/codes/go_work/relay_X/cmd/relay-server)：网关启动入口
- [v1/control](file:///home/ubuntu/codes/go_work/relay_X/v1/control)：控制面协议与连接管理（Hub、鉴权、端口管理、熔断回收）
- [v1/relay](file:///home/ubuntu/codes/go_work/relay_X/v1/relay)：数据面端口监听、FE/BE 配对与数据 pipe
- [v1/config](file:///home/ubuntu/codes/go_work/relay_X/v1/config)：配置加载（固定读取 `configs/config.yaml` 与 `.env.local.json`）
- [configs/config.yaml](file:///home/ubuntu/codes/go_work/relay_X/configs/config.yaml)：静态配置
- [docs/backend_dev_guide.md](file:///home/ubuntu/codes/go_work/relay_X/docs/backend_dev_guide.md)：更完整的协议/时序/配置解释（建议必读）
- [client_test](file:///home/ubuntu/codes/go_work/relay_X/client_test)：FE 测试/压测程序
- [server_test](file:///home/ubuntu/codes/go_work/relay_X/server_test)：BE 模拟器（用于联调验证）

---

## 环境要求

- Go：以 [go.mod](file:///home/ubuntu/codes/go_work/relay_X/go.mod) 为准（当前声明 `go 1.25.0`）
- 端口：控制面 `control_port`（默认 5555）+ 数据面端口段（默认 2300-2380）

---

## 快速开始

### 1) 准备配置

1. 复制敏感配置模板：

```bash
cp .env.local.json.example .env.local.json
```

2. 按需修改：

- [configs/config.yaml](file:///home/ubuntu/codes/go_work/relay_X/configs/config.yaml)

`.env.local.json` 结构如下：

```json
{
  "relay_public_key": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----",
  "auth_secret": "dummy_secret"
}
```

其中 `auth_secret` 会被用于控制面注册鉴权（严格相等），见 [VerifyAuth](file:///home/ubuntu/codes/go_work/relay_X/v1/control/auth.go#L9-L16)。

### 2) 启动网关

```bash
go run ./cmd/relay-server
```

启动流程入口见 [main.go](file:///home/ubuntu/codes/go_work/relay_X/cmd/relay-server/main.go)：

- 初始化日志
- 加载配置（固定读取 `configs/config.yaml` 与 `.env.local.json`）
- 启动数据面监听（遍历端口段分别以 `zmq`/`srt` 标签启动）
- 启动控制面监听（默认 `:5555`）

---

## 核心机制概览

### 控制面：TCP + JSON

控制面消息统一为：

```json
{ "type": "xxx", "payload": { } }
```

关键要求：

- 首条消息必须是 `register`，否则服务端断开连接（逻辑在 [hub.go](file:///home/ubuntu/codes/go_work/relay_X/v1/control/hub.go)）
- `register.payload.secret` 必须与 `.env.local.json` 中 `auth_secret` 严格一致

常见消息类型（以实现为准）：

- `register` / `auth`
- `port_alloc`（注册后下发端口范围信息）
- `port_acquire` / `port_grant` / `port_release`
- `port_state_q` / `port_state`（端口状态查询/广播）
- `ping` / `pong`（心跳/时钟）
- `telemetry` / `log`

更完整说明与时序图见 [backend_dev_guide.md](file:///home/ubuntu/codes/go_work/relay_X/docs/backend_dev_guide.md)。

### 数据面：端口 TCP 中转（带协议标签）

数据面连接建立后，客户端必须在 2 秒内发送一行 HELLO：

```text
RLX1HELLO {"role":"BE","client_id":"be-01"}\n
```

角色只接受 `FE/BE`，用于在端口上完成 FE 与 BE 配对；随后进入双向透明转发。

端口状态含义（网关会广播 `port_state`）：

- `empty`：端口在监听，但尚无 BE 预连接
- `ready`：已有 BE 预连接，等待 FE 连接
- `busy`：已配对为会话，拒绝新连接

---

## 联调验证（推荐）

1. 启动网关：

```bash
go run ./cmd/relay-server
```

2. 启动 BE 模拟器（预连接数据端口）：

```bash
go run ./server_test
```

3. 启动 FE 测试客户端：

```bash
go run ./client_test/test_client.go
```

压测（示例）：

```bash
go run ./client_test/test_client.go load
```

---

## 测试

```bash
go test ./...
```

---

## 安全提示

- `.env.local.json` 可能包含敏感信息（如 `auth_secret`），请勿提交到仓库；仓库内提供 `.env.local.json.example` 作为模板。
- 当前实现未对控制面消息做签名/验签，生产环境建议在协议层补齐鉴权与完整性保护（可参考 [backend_dev_guide.md](file:///home/ubuntu/codes/go_work/relay_X/docs/backend_dev_guide.md) 中的建议）。

