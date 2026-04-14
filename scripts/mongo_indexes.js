// MongoDB index setup for game-im
// Run: mongosh game_im --file scripts/mongo_indexes.js

db = db.getSiblingDB("game_im");

// im_messages: primary query path for PullMsg
db.im_messages.createIndex(
  { channel_id: 1, msg_id: 1 },
  { unique: true, name: "idx_channel_msgid" }
);

// im_messages: user message history
db.im_messages.createIndex(
  { sender_id: 1 },
  { name: "idx_sender" }
);

// im_offline_messages: reconnect pull
db.im_offline_messages.createIndex(
  { uid: 1, created_at: 1 },
  { name: "idx_uid_created" }
);

// im_offline_messages: auto-expire after 7 days
db.im_offline_messages.createIndex(
  { created_at: 1 },
  { expireAfterSeconds: 604800, name: "idx_ttl_7d" }
);

print("indexes created successfully");
