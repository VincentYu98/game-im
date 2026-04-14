# 通用游戏服务器 IM 系统设计文档

> 适用场景：MMO、SLG、MOBA 等需要实时聊天的游戏服务端

---

## 目录

1. [系统目标与约束](#1-系统目标与约束)
2. [整体架构](#2-整体架构)
3. [模块详解](#3-模块详解)
4. [会话模型（Channel Model）](#4-会话模型channel-model)
5. [消息模型（Message Model）](#5-消息模型message-model)
6. [核心流程](#6-核心流程)
7. [存储设计](#7-存储设计)
8. [投递引擎（Delivery Engine）](#8-投递引擎delivery-engine)
9. [跨服/跨区设计](#9-跨服跨区设计)
10. [可靠性保障](#10-可靠性保障)
11. [扩展能力](#11-扩展能力)
12. [技术选型建议](#12-技术选型建议)

---

## 1. 系统目标与约束

### 1.1 核心目标

| 目标 | 说明 |
|------|------|
| 实时投递 | 在线用户端到端延迟 < 100ms |
| 可靠投递 | 消息不丢失，离线用户上线后可补偿 |
| 有序性 | 同一 Channel 内消息全局有序 |
| 幂等性 | 客户端重复提交同一消息只入库一次 |
| 水平扩展 | 连接层可横向扩容，不影响业务层 |

### 1.2 非目标（明确排除）

- 不做端到端加密（游戏场景审核优先于隐私）
- 不做已读回执（游戏 IM 通常不需要精确已读状态）
- 不做消息撤回的强一致（撤回走异步通知即可）

### 1.3 规模假设（参考）

```
在线用户:     10 万 ~ 100 万
Channel 数:   世界频道 × 1，公会 × N（每服数千），私聊 × N
消息 QPS:     峰值 5 万/s（世界频道广播压力最大）
消息大小:     平均 100 字节（文本），最大 1KB（含系统消息）
历史保留:     公会/私聊保留 7 天，世界频道只保留最近 100 条
```

---

## 2. 整体架构

```
┌──────────────────────────────────────────────────────────────────┐
│                          Client                                   │
│              TCP Long Connection / WebSocket                      │
└────────────────────────────┬─────────────────────────────────────┘
                             │
            ┌────────────────▼─────────────────┐
            │         Gateway Layer             │   ← 接入层
            │   ConnManager  |  Auth  |  Route  │
            │   (无状态，可多节点横向扩容)        │
            └────────────────┬─────────────────┘
                             │ Internal RPC（同步）
            ┌────────────────▼─────────────────┐
            │        IM Core Service            │   ← 业务层
            │                                   │
            │  ┌───────────┐  ┌──────────────┐  │
            │  │MsgService │  │ChannelService│  │
            │  └─────┬─────┘  └──────┬───────┘  │
            │        │               │           │
            │  ┌─────▼───────────────▼────────┐  │
            │  │      Delivery Engine          │  │
            │  │  在线RPC | 跨节点RPC | MQ离线 │  │
            │  └──────────┬────────────────────┘  │
            └─────────────┼──────────────────────┘
                          │
         ┌────────────────┼─────────────────┐
         │                │                 │
   ┌─────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
   │   Redis    │  │  DB (MySQL  │  │  MQ         │
   │            │  │  /MongoDB)  │  │ (Kafka/     │
   │ 在线路由表  │  │            │  │  RabbitMQ)  │
   │ 会话状态   │  │ 消息历史    │  │             │
   │ 序列号生成  │  │ 用户关系    │  │ 跨服转发    │
   │ 离线暂存   │  │ 频道元信息  │  │ 离线补偿    │
   └────────────┘  └─────────────┘  │ 广播扇出    │
                                     └─────────────┘
```

---

## 3. 模块详解

### 3.1 Gateway Layer（接入层）

**职责**：维护客户端长连接，不含业务逻辑。

```
子模块:
  ConnManager     管理所有 TCP/WS 连接
                  维护 uid → connId 映射（本节点内）
                  连接建立/断开时通知 PresenceService

  AuthHandler     连接握手时验证 Token
                  认证通过后绑定 uid，写在线路由表到 Redis

  HeartBeat       定时检测死连接（客户端心跳包 + 服务端超时踢出）

  Dispatcher      解包协议（Protobuf / FlatBuffers）
                  按 CmdId 路由到对应的 RPC 调用
```

**关键接口（对外 RPC）**：
```
// 业务层调用 Gateway，主动推消息给指定连接
PushToConn(connId, payload []byte) error
PushToUid(uid int64, payload []byte) error
```

**关键数据（写 Redis）**：
```
presence:{uid} = {
    gatewayNodeId: "gateway-1",
    connId:        "conn-abc123",
    status:        "online",
    expireAt:      unix_timestamp   // 心跳续期
}
TTL = 30s，客户端每 10s 发一次心跳自动续期
```

---

### 3.2 MsgService（消息服务）

**职责**：消息的唯一入口，负责序列号分配、幂等去重、持久化、触发投递。

```
核心接口:
  SendMsg(req SendMsgReq) (msgId int64, err error)
  PullMsg(channelId string, lastMsgId int64, limit int) ([]Message, error)
  AckMsg(uid int64, msgId int64)  // 可选，用于离线消息确认

SendMsg 内部流程:
  1. 参数校验（内容长度、频率限制、禁言检查）
  2. 内容审核（脏词过滤 / 风控接口）
  3. 分配 msgId：INCR Redis key {channel}:seq → 单调递增序列号
  4. 幂等检查：SET NX {clientMsgId} → 已存在则直接返回原 msgId
  5. 写消息到 DB（异步，降低主链路延迟）
  6. 写消息到 Redis 缓存（最近 N 条）
  7. 调用 DeliveryEngine 投递
  8. 返回 msgId 给调用方
```

**消息序列号设计**：
```
每个 Channel 独立维护序列号，使用 Redis INCR 保证原子性：
  key:  im:seq:{channelId}
  val:  自增整数，从 1 开始

客户端持久化 lastMsgId，上线后通过 PullMsg 拉取增量消息，
无需依赖 MQ 回放，避免乱序问题。
```

---

### 3.3 ChannelService（频道/会话服务）

**职责**：管理频道的生命周期和成员关系。

```
核心接口:
  CreateChannel(channelId, channelType, members []uid) error
  DeleteChannel(channelId string) error
  JoinChannel(uid, channelId string) error
  LeaveChannel(uid, channelId string) error
  GetMembers(channelId string) ([]uid, error)
  GetUserChannels(uid int64) ([]channelId, error)
```

**频道类型枚举**：
```
WORLD       世界频道      全服广播，不维护成员列表，不保历史
ALLIANCE    公会/联盟     成员明确，保留历史消息 N 天
ACTIVITY    活动频道      临时性，活动结束后自动销毁
PRIVATE     私聊          1:1，成员固定 2 人
SYSTEM      系统通知      服务端单向下发，客户端只读
```

---

### 3.4 PresenceService（在线状态服务）

**职责**：维护全局在线路由表，是 DeliveryEngine 的核心依赖。

```
核心接口:
  SetOnline(uid, gatewayNodeId, connId string) error
  SetOffline(uid int64) error
  GetPresence(uid int64) (Presence, error)
  BatchGetPresence(uids []int64) (map[int64]Presence, error)

Presence 结构:
  type Presence struct {
      Uid           int64
      GatewayNodeId string   // 在哪个 Gateway 节点
      ConnId        string
      Status        string   // online / offline / away
      LastSeen      int64    // unix ms
  }
```

**存储**：全量存 Redis（单 Key TTL），批量查询使用 Pipeline 降低 RTT。

---

### 3.5 BanService（禁言服务）

```
核心接口:
  Ban(uid int64, duration time.Duration, reason string) error
  Unban(uid int64) error
  IsBanned(uid int64) (bool, expireAt int64)

存储:
  Redis Hash:  im:ban:{uid} = {reason, expireAt}
  或 Redis ZSet: im:ban:set  score=expireAt，定期清理过期成员
```

---

## 4. 会话模型（Channel Model）

### 4.1 Channel ID 规范

```
world                               世界频道（全局唯一）
alliance:{allianceId}               联盟频道
activity:{activityType}:{groupId}   活动分组频道
private:{min(uid1,uid2)}:{max(...)} 私聊（两个 uid 排序后拼接）
system:global                       全服系统通知
```

### 4.2 Channel 状态（存 Redis）

```
im:channel:state:{channelId} = {
    channelId:    string
    channelType:  int
    members:      []int64       // 仅有成员的频道存，世界频道不存
    createdAt:    int64
    lastActiveAt: int64
}
TTL = 7 天（活跃频道持续续期）
```

### 4.3 用户频道索引（存 Redis）

```
im:user:channels:{uid} = HashMap<channelType, channelId>
用于用户上线时快速恢复所有订阅的频道
```

---

## 5. 消息模型（Message Model）

### 5.1 消息结构

```protobuf
message ImMessage {
    int64       msgId           = 1;  // 服务端分配，Channel 内单调递增
    string      channelId       = 2;
    ChannelType channelType     = 3;
    SenderInfo  sender          = 4;
    string      content         = 5;
    MsgType     msgType         = 6;  // TEXT / IMAGE / SYSTEM / EMOJI
    repeated string msgParam    = 7;  // 富文本参数（系统消息模板变量）
    int64       sendTime        = 8;  // 服务端接收时间 unix ms
    string      clientMsgId     = 9;  // 客户端生成，用于幂等去重
}

message SenderInfo {
    int64  senderId         = 1;
    string senderName       = 2;
    string senderHeadImage  = 3;
    // 游戏特有扩展字段（军衔、联盟标签等）
    int32  senderRank       = 4;
    string allianceName     = 5;
}
```

### 5.2 消息类型

```
TEXT        普通文本
IMAGE       图片（存 URL）
EMOJI       表情
SYSTEM      系统消息（模板 + 参数，客户端本地渲染）
LINK        超链接（活动入口）
```

---

## 6. 核心流程

### 6.1 发送消息流程

```
Client
  │── SendMsgReq(channelId, content, clientMsgId)
  │
  ▼
Gateway (本节点)
  │── 解包、鉴权
  │── RPC → MsgService.SendMsg()
  │
  ▼
MsgService
  ├── 校验（禁言、频率限制、内容审核）
  ├── 幂等检查 clientMsgId（Redis SET NX）
  ├── INCR 分配 msgId（Redis im:seq:{channelId}）
  ├── 异步写 DB
  ├── 更新 Redis 缓存（最近 N 条）
  ├── → DeliveryEngine.Deliver(msg, channelId)
  └── 返回 msgId
  │
  ▼
DeliveryEngine
  ├── 查 ChannelService.GetMembers()
  ├── BatchGetPresence(members)
  ├── for each member:
  │     ├── 在线 + 本节点  → 直接 Gateway.PushToConn()
  │     ├── 在线 + 远端节点 → RPC 到远端 Gateway
  │     └── 离线          → MQ 写离线队列 / 写 DB 离线表
  └── 世界频道: MQ Fan-out → 所有 Gateway 消费推送
  │
  ▼
Client 收到 PushNewMessage(msg)
```

### 6.2 上线拉取增量消息流程

```
Client 上线时携带各 Channel 的 lastMsgId
  │
  ▼
Gateway
  │── RPC → MsgService.PullMsg(channelId, lastMsgId, limit)
  │
  ▼
MsgService
  ├── 查 Redis 缓存（命中直接返回）
  └── 缓存 Miss → 查 DB（按 channelId + msgId > lastMsgId）
  │
  ▼
Client 按 msgId 顺序渲染补齐的消息
```

### 6.3 加入频道流程（以公会聊天为例）

```
玩家加入公会事件（来自游戏主服务）
  │── RPC → ChannelService.JoinChannel(uid, "alliance:{allianceId}")
  │
  ▼
ChannelService
  ├── 将 uid 写入 im:channel:state:{channelId}.members
  ├── 写 im:user:channels:{uid} 索引
  └── 通知 DeliveryEngine 更新路由缓存（可选）
```

### 6.4 禁言流程

```
GM 操作 / 自动风控触发
  │── RPC → BanService.Ban(uid, duration, reason)
  │
  ▼
BanService
  ├── 写 Redis im:ban:{uid}（带 TTL）
  └── （可选）下发系统消息通知当事人

SendMsg 前置检查:
  IsBanned(uid) → true → 返回错误码 USER_BANNED
```

---

## 7. 存储设计

### 7.1 Redis（热数据）

```
Key                               类型    TTL     用途
im:presence:{uid}                 Hash    30s     在线路由表
im:seq:{channelId}                String  永久    消息序列号计数器
im:dedup:{clientMsgId}            String  24h     客户端消息去重
im:channel:state:{channelId}      Hash    7d      频道状态+成员列表
im:user:channels:{uid}            Hash    7d      用户频道索引
im:msg:cache:{channelId}          List    1h      最近 N 条消息缓存
im:ban:{uid}                      Hash    动态TTL 禁言记录
im:ratelimit:{uid}                String  1s      发消息频率限制
```

### 7.2 DB（冷数据，MySQL 示例）

```sql
-- 消息历史表（按 channelId 分表 or 分库）
CREATE TABLE im_messages (
    id            BIGINT PRIMARY KEY AUTO_INCREMENT,
    msg_id        BIGINT NOT NULL,          -- Channel 内序列号
    channel_id    VARCHAR(128) NOT NULL,
    channel_type  TINYINT NOT NULL,
    sender_id     BIGINT NOT NULL,
    content       TEXT NOT NULL,
    msg_type      TINYINT NOT NULL DEFAULT 1,
    msg_param     JSON,
    send_time     BIGINT NOT NULL,          -- unix ms
    created_at    DATETIME DEFAULT NOW(),
    INDEX idx_channel_msgid (channel_id, msg_id),
    INDEX idx_sender (sender_id)
) ENGINE=InnoDB
  PARTITION BY HASH(channel_id) PARTITIONS 16;  -- 按 channelId 分区

-- 离线消息表
CREATE TABLE im_offline_messages (
    id            BIGINT PRIMARY KEY AUTO_INCREMENT,
    uid           BIGINT NOT NULL,
    msg_id        BIGINT NOT NULL,
    channel_id    VARCHAR(128) NOT NULL,
    payload       BLOB NOT NULL,            -- 序列化后的完整消息
    created_at    DATETIME DEFAULT NOW(),
    INDEX idx_uid (uid, created_at)
);

-- 频道元信息表
CREATE TABLE im_channels (
    channel_id    VARCHAR(128) PRIMARY KEY,
    channel_type  TINYINT NOT NULL,
    created_at    DATETIME DEFAULT NOW(),
    last_active   DATETIME,
    INDEX idx_type (channel_type)
);
```

### 7.3 存储分级策略

```
写入链路（主链路不阻塞）:
  消息到达 → 先写 Redis 缓存（同步）→ 异步写 DB

读取链路（三级降级）:
  1. Actor 内存（最新 N 条，< 1ms）
  2. Redis List 缓存（最近 1h，< 5ms）
  3. MySQL（全量历史，< 50ms）
```

---

## 8. 投递引擎（Delivery Engine）

### 8.1 投递路径决策

```
func Deliver(msg Message, channelId string):

    if channelType == WORLD:
        → MQ Publish to topic "im.broadcast.world"
        → 所有 Gateway 节点消费，推送给各自在线用户
        → 无需关心成员列表，世界频道不存成员
        return

    members = ChannelService.GetMembers(channelId)
    presenceMap = PresenceService.BatchGet(members)

    for uid, presence in presenceMap:
        switch:
        case online && presence.gatewayNodeId == localNode:
            Gateway.PushToConn(presence.connId, msg)      // 最快路径

        case online && presence.gatewayNodeId != localNode:
            RemoteGateway[presence.gatewayNodeId].PushToUid(uid, msg)  // 同服跨节点 RPC

        case crossServer:
            MQ.Publish("im.cross_server.{serverId}", msg) // 跨服 MQ

        case offline:
            OfflineQueue.Push(uid, msg)                   // 离线暂存
```

### 8.2 MQ Topic 规划

```
Topic                               生产者              消费者
im.broadcast.world                  MsgService          所有 Gateway 节点
im.broadcast.system                 后台管理服务         所有 Gateway 节点
im.cross_server.{serverId}          本服 DeliveryEngine  目标服 IM Service
im.offline.push                     DeliveryEngine      离线推送服务（APNs/FCM）
im.audit                            MsgService          审计服务（只写不读）
```

---

## 9. 跨服/跨区设计

### 9.1 跨服消息流

```
服务器 A（联盟成员分布）             服务器 B（另一部分联盟成员）
        │                                    │
   IM Service A                         IM Service B
        │                                    │
        │── MQ Publish ──────────────────────▶│
             Topic: im.cross_server.B         │
                                              │ 消费消息
                                              │── DeliveryEngine
                                              │── Push to local online members
```

### 9.2 跨服路由表

```
每个 IM Service 在启动时向中心路由注册:
  im:server:registry = {
      "server-1": { "host": "10.0.0.1", "rpcPort": 9001 },
      "server-2": { "host": "10.0.0.2", "rpcPort": 9001 },
      ...
  }

消息跨服时:
  1. 查 Channel 成员所在服务器列表
  2. 本服成员走本地路径
  3. 跨服成员按 serverId 聚合，一次性推送到目标服 MQ
```

---

## 10. 可靠性保障

### 10.1 消息不丢失

```
客户端发送:
  ├── 客户端生成唯一 clientMsgId（UUID 或 snowflake 本地生成）
  ├── 收到服务端 ACK（含 msgId）后才认为发送成功
  └── 超时未收到 ACK → 重发（服务端幂等保证不重复入库）

服务端写入:
  ├── Redis 写入成功 → 立即可投递
  ├── DB 写入异步，失败时写入补偿队列重试
  └── 消息序列号保证客户端可检测到是否有消息间隙
```

### 10.2 离线消息补偿

```
用户上线时:
  1. 携带各 Channel 的 lastMsgId 上报
  2. 服务端对比当前最新 msgId
  3. 存在间隙 → PullMsg 拉取增量
  4. 同时清理该用户的离线消息表记录

离线消息存储策略:
  - 联盟/私聊：写 DB 离线表，保留 7 天
  - 世界频道：不存离线（内容时效性短，客户端拉最近 N 条即可）
  - 系统通知：单独存储，保证必达（重要通知用邮箱补偿）
```

### 10.3 消息有序性

```
同一 Channel 内:
  - 服务端用 Redis INCR 分配 msgId，保证单调递增
  - 客户端按 msgId 排序渲染
  - MQ 消费者单 partition 顺序消费（Kafka 按 channelId 分区）

跨 Channel 不保证顺序（无需保证）
```

### 10.4 频率限制

```
每用户发消息频率上限（Redis 滑动窗口）:
  普通聊天:   1 条/秒
  系统消息:   不限（服务端触发）
  活动期间:   可动态调整上限

超频时返回错误码 RATE_LIMITED，客户端做本地提示
```

---

## 11. 扩展能力

### 11.1 消息内容审核扩展点

```
SendMsg 流程中，审核作为可插拔中间件:

type ContentFilter interface {
    Check(uid int64, content string) FilterResult
}

内置实现:
  - DirtyWordFilter    本地词库过滤
  - RiskAPIFilter      外部风控接口（异步，不阻塞主链路可选）

返回:
  type FilterResult struct {
      Pass    bool
      Reason  string   // 拒绝原因
      Replace string   // 替换后的内容（部分过滤场景）
  }
```

### 11.2 消息类型扩展

```
MsgType 预留扩展区间:
  1-99    基础类型（TEXT / IMAGE / EMOJI / SYSTEM）
  100-199 游戏内联消息（战报分享 / 建筑截图）
  200-299 活动消息（活动入口卡片）
  300-399 运营消息（公告 / 推送）

新增消息类型只需客户端和协议定义，服务端存储层透明传输。
```

### 11.3 频道动态开关

```
运营可在后台动态控制各频道开放状态:

im:channel:available:{channelType} = 0/1  (Redis)

MsgService.SendMsg 前检查:
  IsChannelAvailable(channelType) → false → 返回 CHANNEL_UNAVAILABLE
```

---

## 12. 技术选型建议

### 12.1 连接层

| 方案 | 适用场景 | 备注 |
|------|---------|------|
| TCP + 自定义协议 | PC 端游、原生客户端 | 延迟最低，需自行处理粘包 |
| WebSocket | H5、小程序、跨平台 | 基于 HTTP Upgrade，穿透性好 |
| QUIC | 移动网络频繁切换场景 | 实现复杂度高，弱网优势明显 |

### 12.2 消息队列

| 方案 | 适用规模 | 备注 |
|------|---------|------|
| Redis Pub/Sub | 单服，小规模（< 10 万在线） | 实现简单，消息不持久化 |
| RabbitMQ | 中等规模，需可靠投递 | 延迟低，运维相对简单 |
| Kafka / Pulsar | 大规模，跨服，需审计 | 吞吐最高，延迟略高，首选跨服场景 |

### 12.3 业务层框架

| 方案 | 优势 | 劣势 |
|------|------|------|
| Actor 模型（每个 Channel 一个 Actor） | 天然隔离，状态管理简单 | 大量 Channel 时内存占用高 |
| 纯无状态服务 + Redis 状态外置 | 易扩容，重启无损 | Redis 压力大，频繁序列化 |
| 混合：热 Channel 用 Actor，冷 Channel 状态外置 | 兼顾性能和扩展性 | 实现复杂度较高（推荐） |

### 12.4 存储

```
热数据（在线状态、最近消息）: Redis Cluster
消息历史（需查询）: MySQL（中等规模）/ TiDB（超大规模）
消息归档（只写）: MongoDB / ClickHouse（分析）
文件/图片: OSS（不经过 IM 服务，客户端直传 + URL 入消息体）
```

---

## 附录：错误码规范

```
0      SUCCESS
1001   USER_BANNED              用户被禁言
1002   CONTENT_ILLEGAL          内容违规
1003   RATE_LIMITED             发送频率超限
1004   CHANNEL_NOT_FOUND        频道不存在
1005   USER_NOT_IN_CHANNEL      用户不在频道中
1006   CHANNEL_UNAVAILABLE      频道已关闭
1007   MSG_TOO_LONG             消息超长
5000   SERVER_ERROR             服务内部错误
```

---

## 附录：关键指标监控点

```
连接层:
  - 当前在线连接数（按节点）
  - 连接建立/断开 QPS
  - 心跳超时断连次数

消息层:
  - SendMsg QPS（按频道类型）
  - 消息端到端延迟 P50/P99
  - 消息投递失败率
  - 离线消息积压量

存储层:
  - Redis 命中率
  - DB 写入延迟
  - MQ 消费延迟（lag）
```
