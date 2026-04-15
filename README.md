# Game IM

通用游戏服务器即时通讯系统，适用于 MMO、SLG、MOBA 等需要实时聊天的游戏服务端。

## 架构

```
Client (TCP + Protobuf)
    │
    ▼
Gateway Layer ──── ConnManager / Auth / Heartbeat / Dispatcher
    │                          │
    │                   BroadcastRing (世界/系统频道)
    │                   Lock-free Ring Buffer
    │                   O(1) write, per-conn cursor read
    ▼
IM Core Service
    ├── MsgService      消息收发、序列号分配、幂等去重
    ├── ChannelService   频道生命周期、成员管理
    ├── PresenceService  在线路由表
    ├── BanService       禁言
    ├── ContentFilter    内容审核（脏词过滤）
    └── DeliveryEngine   消息路由（本地推送 / 离线暂存 / 广播）
    │
    ▼
Storage
    ├── Redis   在线状态、序列号、去重、缓存、限流
    └── MongoDB 消息历史、离线消息、频道元信息
```

**单进程部署**，模块通过 Go interface 解耦，可按需拆分为微服务。

## 特性

- **TCP + Protobuf** 二进制协议，4 字节长度前缀帧
- **消息可靠投递**：服务端 ACK + 客户端重传幂等 + 离线补偿拉取
- **消息有序**：每 Channel 独立序列号（SeqAllocator 内存预分配 + Redis INCRBY），客户端按 msgId 排序
- **频道类型**：世界频道 / 公会 / 活动 / 私聊 / 系统通知
- **内容审核**：可插拔 FilterChain，内置脏词过滤器
- **MQ 抽象**：`MessageBus` 接口，当前为进程内 LocalBus，可替换为 Kafka/RabbitMQ
- **单 Lua 脚本热路径**：SendMsg 只需 1 次 Redis RTT（dedup + rate limit），ban/availability 用本地 5s 缓存
- **异步 DB 写入**：消息先写 Redis 缓存（异步），MongoDB 批量异步写入，均不阻塞响应
- **Ring Buffer 广播**：世界/系统频道使用无锁环形缓冲区，写入 O(1)，每连接独立游标读取，消除 O(N) 遍历
- **优先级发送通道**：响应帧（respCh）优先于推送帧（pushCh），writeLoop 批量合并 TCP 写入
- **轻量广播通知**：世界频道推送 `WorldNotify{channel_id, latest_seq}`（~20 bytes），客户端按需 PullMsg

## 快速开始

### 依赖

- Go 1.24+
- Docker + Docker Compose（Redis、MongoDB、生产模式服务器）
- protoc + protoc-gen-go（仅修改 proto 时需要）

### 本地开发

```bash
# 启动 Redis 和 MongoDB
docker compose up -d redis mongo

# 编译并运行
make build
make run
```

服务启动后监听 `TCP :9000`。

### Docker 生产模式

```bash
# 启动全部服务（Redis + MongoDB + IM Server）
# 服务器运行在 Linux 容器中，somaxconn=65535
docker compose up -d

# 查看日志
docker compose logs im-server
```

### 配置

本地开发：`configs/config.yaml`
Docker 部署：`configs/config.docker.yaml`

```yaml
gateway:
  listen_addr: ":9000"
  max_connections: 200000
  heartbeat_timeout: 60s

redis:
  addr: "redis:6379"

mongo:
  uri: "mongodb://mongo:27017"
  database: "game_im"
```

支持环境变量覆盖，前缀 `IM_`。

## 协议

### 帧格式

```
[4 bytes: body_len (big-endian)] [body: protobuf Frame]

Frame {
  uint32 cmd_id   // 命令 ID
  uint32 seq      // 客户端序列号，响应回显
  bytes  payload  // 内部消息的序列化字节
}
```

### 命令 ID

| CmdId | 方向 | 消息类型 |
|-------|------|---------|
| 1001 | C→S | AuthReq |
| 1002 | S→C | AuthResp |
| 2001 | C→S | SendMsgReq |
| 2002 | S→C | SendMsgResp |
| 2003 | C→S | PullMsgReq |
| 2004 | S→C | PullMsgResp |
| 2005 | C→S | AckMsgReq |
| 3001 | C→S | HeartbeatReq |
| 3002 | S→C | HeartbeatResp |
| 4001 | S→C | PushNewMessage（公会/私聊推送）|
| 4002 | S→C | WorldNotify（世界/系统频道通知）|

## 测试

```bash
# 全量测试（需要 Redis + MongoDB 运行）
make test

# 仅单元测试（无外部依赖的包）
go test ./internal/bus/ ./internal/gateway/ ./internal/delivery/ ./internal/service/

# 仅集成测试
go test ./test/ -v
```

### 测试覆盖

| 包 | 测试数 | 类型 |
|---|--------|------|
| internal/store | 16 | Redis + MongoDB 集成 |
| internal/gateway | 22 | Codec / Conn / ConnManager 单元 |
| internal/bus | 6 | LocalBus 单元 |
| internal/delivery | 5 | DeliveryEngine mock 单元 |
| internal/service | 4 | ContentFilter 单元 |
| test/ | 7 | 端到端集成（TCP 客户端） |

## 压测

### 小规模延迟测试

```bash
# 编译压测工具
make bench

# 同步模式（测延迟）
./bin/im-bench --conns 50 --msgs 100 --mode sync

# 管道模式（测吞吐）
./bin/im-bench --conns 50 --msgs 100 --mode pipeline
```

#### 小规模参考数据（macOS 本地，单 Redis）

| 连接数 | 模式 | P50 | P95 | P99 | 吞吐量 |
|--------|------|-----|-----|-----|--------|
| 1 | sync | 127µs | 188µs | 842µs | 13K msg/s |
| 10 | sync | 464µs | 866µs | 1.8ms | 17.6K msg/s |
| 20 | sync | 912µs | 1.3ms | 1.7ms | 21K msg/s |
| 30 | sync | 1.3ms | 1.8ms | 2.1ms | 23K msg/s |
| 50 | sync | 2.5ms | 3.5ms | 4.3ms | 19.8K msg/s |
| 100 | sync | 5.1ms | 8.3ms | 10.7ms | 18.9K msg/s |
| 100 | pipeline | - | - | - | 47K msg/s |

### 大规模负载测试

模拟真实游戏生态（二八定律）：90% 连接空闲心跳，10% 活跃发消息。

```bash
# 编译负载测试工具
go build -o bin/im-loadtest ./cmd/loadtest

# 10K 连接测试
./bin/im-loadtest --conns 10000 --conn-rate 1000 --active-pct 0.1 --qps 20000 --duration 10

# 30K 连接测试（建议使用 Docker 部署的服务器）
./bin/im-loadtest --conns 30000 --conn-rate 1500 --active-pct 0.1 --qps 20000 --duration 10
```

参数说明：
- `--conns`: 总连接数
- `--conn-rate`: 每秒建连数（建连风暴控制）
- `--active-pct`: 活跃发送者比例（0.1 = 10%）
- `--qps`: 目标总 QPS
- `--duration`: 发送持续时间（秒）

#### 大规模参考数据

**macOS 本地服务器** (somaxconn=128)

| 连接数 | 建连成功率 | 活跃发送者 | 实际 QPS | 广播延迟 | 消息成功率 |
|--------|-----------|-----------|---------|----------|-----------|
| 10,000 | 100% | 1,000 | 19,878 msg/s | 47ms | 100% |
| 30,000 | 80.5% | 2,992 | 16,226 msg/s | 130ms | 100% |

**Docker Linux 服务器** (somaxconn=65535)

| 连接数 | 建连成功率 | 活跃发送者 | 实际 QPS | 广播延迟 | 消息成功率 |
|--------|-----------|-----------|---------|----------|-----------|
| 10,000 | 100% | 1,000 | 15,121 msg/s | **1.18ms** | 100% |
| 30,000 | 98% | 2,939 | 17,597 msg/s | **3.5ms** | 100% |

> 广播延迟 = 1 条世界频道消息从发出到全部客户端收到 WorldNotify 的耗时

## 性能优化记录

### 优化路径

| 阶段 | 优化内容 | 50 连接 P50 | 吞吐量 |
|------|---------|------------|--------|
| v1 | 初始实现，4 次串行 Redis RTT | 23ms | 2,575 msg/s |
| v2 | Redis Pipeline 合并预检查 | 9ms | 5,384 msg/s |
| v3 | 单 Lua 脚本 + 异步 PostSend + 响应优先通道 | 3.1ms | 14K msg/s |
| v4 | SeqAllocator 内存预分配 + LocalCache ban/avail | 2.5ms | 19.8K msg/s |
| v5 | WorldNotify 轻量通知 + Ring Buffer 广播 | 2.5ms | 19.8K msg/s |

### 关键设计决策

**写扩散 vs 读扩散**

| 频道类型 | 策略 | 原因 |
|---------|------|------|
| 私聊 (2人) | 写扩散 PushToUID | O(1)，无开销 |
| 公会 (5-100人) | 写扩散 PushNewMessage | O(N) 但 N 小 |
| 世界频道 (全服) | 读扩散 WorldNotify + PullMsg | 避免 O(N) fan-out |

**Ring Buffer 广播**

世界/系统频道使用无锁环形缓冲区替代 `sync.Map.Range` + N 次 channel send：

```
Before: 每条消息 → PushToAllExcept → 遍历 30K 连接 → 30K 次 pushCh send
After:  每条消息 → ring.Put(data) [1 次原子写] → sync.Cond.Broadcast [唤醒消费者]
        每个连接 → ringConsumerLoop → 读 [cursor, writePos) → 自行推入 pushCh
```

**单 Lua 脚本热路径**

SendMsg 的 Redis 交互：
```
v1: GET dedup → HGETALL ban → GET avail → Lua rate → INCR seq → Pipeline cache+dedup = 4 RTT
v5: Lua(GET dedup + INCR rate + SET dedup) = 1 RTT
    ban/avail → 本地 5s 缓存 (0 RTT)
    seq → SeqAllocator 内存分配 (0 RTT, 每 1000 条 1 RTT 补充)
```

## 项目结构

```
game-im/
├── cmd/
│   ├── server/main.go       入口
│   ├── bench/main.go        小规模压测工具
│   └── loadtest/main.go     大规模负载测试工具
├── api/proto/                Protobuf 定义
├── api/pb/                   生成的 Go 代码
├── internal/
│   ├── app/                  依赖注入、生命周期
│   ├── gateway/              TCP 接入层（含 BroadcastRing）
│   ├── service/              业务服务（含 SeqAllocator、LocalCache）
│   ├── delivery/             消息投递引擎
│   ├── bus/                  消息总线抽象
│   └── store/                Redis + MongoDB 存储
├── pkg/errs/                 错误码
├── pkg/util/                 工具函数
├── configs/                  配置（本地 + Docker）
├── test/                     集成测试
├── scripts/                  数据库脚本
├── Dockerfile                多阶段构建
└── docker-compose.yml        Redis + MongoDB + IM Server
```

## 设计文档

详细设计见 [game-im-system-design.md](docs/game-im-system-design.md)。

## License

MIT
