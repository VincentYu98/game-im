package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"game-im/configs"
)

// ─── Document Models ────────────────────────────────────

type MessageDoc struct {
	MsgID       int64     `bson:"msg_id"`
	ChannelID   string    `bson:"channel_id"`
	ChannelType int32     `bson:"channel_type"`
	SenderID    int64     `bson:"sender_id"`
	SenderName  string    `bson:"sender_name"`
	Content     string    `bson:"content"`
	MsgType     int32     `bson:"msg_type"`
	MsgParam    []string  `bson:"msg_param,omitempty"`
	SendTime    int64     `bson:"send_time"`
	CreatedAt   time.Time `bson:"created_at"`
}

type OfflineMessageDoc struct {
	UID       int64     `bson:"uid"`
	MsgID     int64     `bson:"msg_id"`
	ChannelID string    `bson:"channel_id"`
	Payload   []byte    `bson:"payload"`
	CreatedAt time.Time `bson:"created_at"`
}

type ChannelDoc struct {
	ChannelID   string    `bson:"_id"`
	ChannelType int32     `bson:"channel_type"`
	CreatedAt   time.Time `bson:"created_at"`
	LastActive  time.Time `bson:"last_active"`
}

// ─── MongoStore ─────────────────────────────────────────

type MongoStore struct {
	client   *mongo.Client
	db       *mongo.Database
	messages *mongo.Collection
	offline  *mongo.Collection
	channels *mongo.Collection
	logger   *slog.Logger
}

func NewMongoStore(cfg configs.MongoConfig, logger *slog.Logger) (*MongoStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(cfg.URI))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("mongo ping: %w", err)
	}

	db := client.Database(cfg.Database)
	logger.Info("mongodb connected", "uri", cfg.URI, "database", cfg.Database)

	s := &MongoStore{
		client:   client,
		db:       db,
		messages: db.Collection("im_messages"),
		offline:  db.Collection("im_offline_messages"),
		channels: db.Collection("im_channels"),
		logger:   logger,
	}

	if err := s.ensureIndexes(ctx); err != nil {
		logger.Warn("failed to ensure indexes", "err", err)
	}

	return s, nil
}

func (s *MongoStore) Close(ctx context.Context) error {
	return s.client.Disconnect(ctx)
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	// im_messages: unique index on (channel_id, msg_id)
	_, err := s.messages.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "channel_id", Value: 1}, {Key: "msg_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("messages index: %w", err)
	}

	// im_messages: index on sender_id
	_, err = s.messages.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "sender_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("messages sender index: %w", err)
	}

	// im_offline_messages: index on (uid, created_at)
	_, err = s.offline.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "uid", Value: 1}, {Key: "created_at", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("offline index: %w", err)
	}

	// im_offline_messages: TTL index on created_at (7 days)
	expireSeconds := int32(7 * 24 * 3600)
	_, err = s.offline.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(expireSeconds),
	})
	if err != nil {
		s.logger.Warn("offline TTL index may already exist", "err", err)
	}

	return nil
}

// ─── Message Operations ─────────────────────────────────

func (s *MongoStore) InsertMessage(ctx context.Context, doc *MessageDoc) error {
	_, err := s.messages.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return nil // idempotent: already inserted
	}
	return err
}

func (s *MongoStore) InsertMessages(ctx context.Context, docs []*MessageDoc) error {
	if len(docs) == 0 {
		return nil
	}
	items := make([]interface{}, len(docs))
	for i, d := range docs {
		items[i] = d
	}
	opts := options.InsertMany().SetOrdered(false) // continue on dup key
	_, err := s.messages.InsertMany(ctx, items, opts)
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return err
	}
	return nil
}

func (s *MongoStore) QueryMessages(ctx context.Context, channelID string, lastMsgID int64, limit int) ([]*MessageDoc, error) {
	filter := bson.M{
		"channel_id": channelID,
		"msg_id":     bson.M{"$gt": lastMsgID},
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "msg_id", Value: 1}}).
		SetLimit(int64(limit))

	cursor, err := s.messages.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []*MessageDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// ─── Offline Message Operations ─────────────────────────

func (s *MongoStore) InsertOfflineMessage(ctx context.Context, doc *OfflineMessageDoc) error {
	_, err := s.offline.InsertOne(ctx, doc)
	return err
}

func (s *MongoStore) GetOfflineMessages(ctx context.Context, uid int64, limit int) ([]*OfflineMessageDoc, error) {
	filter := bson.M{"uid": uid}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}}).
		SetLimit(int64(limit))

	cursor, err := s.offline.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []*OfflineMessageDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *MongoStore) DeleteOfflineMessages(ctx context.Context, uid int64) error {
	_, err := s.offline.DeleteMany(ctx, bson.M{"uid": uid})
	return err
}

// ─── Channel Operations ─────────────────────────────────

func (s *MongoStore) UpsertChannel(ctx context.Context, doc *ChannelDoc) error {
	filter := bson.M{"_id": doc.ChannelID}
	update := bson.M{"$set": doc}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.channels.UpdateOne(ctx, filter, update, opts)
	return err
}

func (s *MongoStore) GetChannel(ctx context.Context, channelID string) (*ChannelDoc, error) {
	var doc ChannelDoc
	err := s.channels.FindOne(ctx, bson.M{"_id": channelID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &doc, err
}

func (s *MongoStore) DeleteChannel(ctx context.Context, channelID string) error {
	_, err := s.channels.DeleteOne(ctx, bson.M{"_id": channelID})
	return err
}

func (s *MongoStore) UpdateChannelLastActive(ctx context.Context, channelID string) error {
	_, err := s.channels.UpdateOne(ctx,
		bson.M{"_id": channelID},
		bson.M{"$set": bson.M{"last_active": time.Now()}},
	)
	return err
}
