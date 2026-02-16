V4 端口唯一性验证报告

目标
- 任意数据端口（ZMQ/TCP 与 SRT/UDP）同一时刻严格只允许 1 个服务端会话 + 1 个客户端会话
- 禁止端口复用：禁止多服务端或多客户端在同端口并存
- 检测到冲突必须拒绝并输出冲突日志

实现口径
- OS 层占用检测：启动时对 ZMQ 端口做 TCP 监听探测、对 SRT 端口做 UDP 监听探测；失败即认为端口被占用并阻断启动
- 进程内一对一约束：
  - 每个端口仅允许进入 2 个角色连接（server + client）
  - 端口若已有 server pending，再次收到 server 连接直接拒绝
  - 端口若已有 client pending，再次收到 client 连接直接拒绝
  - 端口若已建立会话（Occupied），任何新连接直接拒绝
- 端口状态机：
  - 控制面配对成功后：Idle -> Reserved
  - 数据面两端连接到齐：Reserved -> Occupied
  - 会话结束或超时：Occupied/Reserved -> Idle

端口冲突日志范围
- 启动端口探测：tcp_port_available / udp_port_available / *_port_conflict
- 数据面端口冲突：
  - server_dup / client_dup / port_occupied / alloc_mismatch

自动化测试用例
- v4/relay/tcp_server_test.go: TestTCPPortUniqueness
  - 验证同一端口重复 server 连接被拒绝
  - 验证同一端口重复 client 连接被拒绝
  - 验证会话结束后端口状态回到 Idle
- v4/ports/pool_test.go
  - 验证端口从 Reserved -> Occupied -> Idle 的状态转换
  - 验证 Reserved 过期释放回 Idle

结论
- 通过“启动占用探测 + 端口池状态机 + 数据面角色去重”的组合约束，可在任意时序下阻止端口复用与角色重叠
- 若出现冲突，系统会拒绝重复绑定并输出可审计的冲突日志与上下文信息
