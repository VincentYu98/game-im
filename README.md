# Game IM

通用游戏服务器即时通讯系统，适用于 MMO、SLG、MOBA 等需要实时聊天的游戏服务端。

## 架构

```
Client (TCP + Protobuf)
    │
    ▼
Gateway Layer ──── ConnManager / Auth / Heartbeat / Dispatcher
    │
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
- **消息有序**：每 Channel 独立序列号（Redis INCR），客户端按 msgId 排序
- **频道类型**：世界频道 / 公会 / 活动 / 私聊 / 系统通知
- **内容审核**：可插拔 FilterChain，内置脏词过滤器
- **MQ 抽象**：`MessageBus` 接口，当前为进程内 LocalBus，可替换为 Kafka/RabbitMQ
- **Redis Pipeline 优化**：SendMsg 热路径 3 次 Redis 往返（预检查管道 + 限流 Lua + 序列号 + 后置管道）
- **异步 DB 写入**：消息先写 Redis 缓存（同步），MongoDB 批量异步写入

## 快速开始

### 依赖

- Go 1.24+
- Redis 7+
- MongoDB 7+
- protoc + protoc-gen-go

### 启动

```bash
# 启动 Redis 和 MongoDB
docker-compose up -d

# 生成 Protobuf 代码（已提交，仅修改 proto 后需要）
make proto

# 编译并运行
make build
make run
```

服务启动后监听 `TCP :9000`。

### 配置

编辑 `configs/config.yaml`：

```yaml
gateway:
  listen_addr: ":9000"
  max_connections: 100000
  heartbeat_timeout: 30s

redis:
  addr: "localhost:6379"

mongo:
  uri: "mongodb://localhost:27017"
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
| 4001 | S→C | PushNewMessage |

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

```bash
# 编译压测工具
make bench

# 同步模式（测延迟）
./bin/im-bench --conns 50 --msgs 100 --mode sync

# 管道模式（测吞吐）
./bin/im-bench --conns 50 --msgs 100 --mode pipeline
```

### 本地压测参考数据

| 模式 | 连接数 | 消息数/连接 | 吞吐量 | P50 | P99 |
|------|--------|------------|--------|-----|-----|
| sync | 50 | 100 | 5,384 msg/s | 9ms | 16ms |
| pipeline | 50 | 100 | 50,353 msg/s | 56ms | 94ms |

## 项目结构

```
game-im/
├── cmd/
│   ├── server/main.go       入口
│   └── bench/main.go        压测工具
├── api/proto/                Protobuf 定义
├── api/pb/                   生成的 Go 代码
├── internal/
│   ├── app/                  依赖注入、生命周期
│   ├── gateway/              TCP 接入层
│   ├── service/              业务服务
│   ├── delivery/             消息投递引擎
│   ├── bus/                  消息总线抽象
│   └── store/                Redis + MongoDB 存储
├── pkg/errs/                 错误码
├── pkg/util/                 工具函数
├── configs/                  配置
├── test/                     集成测试
└── scripts/                  数据库脚本
```

## 设计文档

详细设计见 [game-im-system-design.md](../game-im-system-design.md)。

## License

MIT
