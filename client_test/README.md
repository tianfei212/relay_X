# JOJO-Relay-X 网关测试程序

这是一个用于测试 JOJO-Relay-X 网关的客户端测试程序，用于验证网关与前端客户端之间的连接和通信。

## 功能特性

- 模拟前端客户端连接到网关
- 通过 5555 控制面申请数据端口（port_acquire -> port_grant）
- 在同一数据端口同时测试 TCP 与 UDP 的双向透传（依赖后端回显程序）

## 测试文件

- `test_client.go`: 主测试程序源码
  - 适配 V3：不区分 zmq/srt；同一端口同时跑 TCP+UDP
- `IMG_4127.MOV`: 历史测试素材，不随仓库分发（本地自备）

## 使用方法

### 前置条件（启动后端回显）

先启动后端模拟器（server_test），让网关的数据端口有回显端：

```bash
cd server_test
go run .
```

### 单次测试（前端）

```bash
# 在 client_test 目录下运行
go run test_client.go -gateway 127.0.0.1:5555
```

## 测试结果说明

- **成功**: 输出 `OK port=... session=...`，并且 TCP/UDP 均回显一致
- **失败**: 通常是后端回显未启动，或网关数据端口范围未配置/未启动

## 配置说明

V3 不做鉴权检查，但仍复用 `configs/config.yaml` 中的数据端口范围字段（`relay.zmq_*` 与 `relay.srt_*`，V3 会合并为同一端口池）。

## 测试覆盖

- 控制面：port_acquire / port_grant / port_release 基本流程
- 数据面：同端口 TCP 透传回显
- 数据面：同端口 UDP 透传回显
