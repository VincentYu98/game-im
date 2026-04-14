package configs

import "time"

type Config struct {
	App     AppConfig     `koanf:"app"`
	Gateway GatewayConfig `koanf:"gateway"`
	Redis   RedisConfig   `koanf:"redis"`
	Mongo   MongoConfig   `koanf:"mongo"`
	Bus     BusConfig     `koanf:"bus"`
	Log     LogConfig     `koanf:"log"`
}

type AppConfig struct {
	NodeID          string        `koanf:"node_id"`
	Env             string        `koanf:"env"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
}

type GatewayConfig struct {
	ListenAddr        string        `koanf:"listen_addr"`
	MaxConnections    int           `koanf:"max_connections"`
	MaxFrameSize      int           `koanf:"max_frame_size"`
	HeartbeatTimeout  time.Duration `koanf:"heartbeat_timeout"`
	HeartbeatInterval time.Duration `koanf:"heartbeat_interval"`
	SendChanSize      int           `koanf:"send_chan_size"`
}

type RedisConfig struct {
	Addr     string `koanf:"addr"`
	Password string `koanf:"password"`
	DB       int    `koanf:"db"`
	PoolSize int    `koanf:"pool_size"`
}

type MongoConfig struct {
	URI      string        `koanf:"uri"`
	Database string        `koanf:"database"`
	Timeout  time.Duration `koanf:"timeout"`
}

type BusConfig struct {
	Type string `koanf:"type"` // "local" | "kafka" | "rabbitmq"
}

type LogConfig struct {
	Level  string `koanf:"level"`  // "debug" | "info" | "warn" | "error"
	Format string `koanf:"format"` // "json" | "text"
}

func DefaultConfig() *Config {
	return &Config{
		App: AppConfig{
			Env:             "dev",
			ShutdownTimeout: 10 * time.Second,
		},
		Gateway: GatewayConfig{
			ListenAddr:        ":9000",
			MaxConnections:    100000,
			MaxFrameSize:      65536,
			HeartbeatTimeout:  30 * time.Second,
			HeartbeatInterval: 10 * time.Second,
			SendChanSize:      256,
		},
		Redis: RedisConfig{
			Addr:     "localhost:6379",
			DB:       0,
			PoolSize: 100,
		},
		Mongo: MongoConfig{
			URI:      "mongodb://localhost:27017",
			Database: "game_im",
			Timeout:  10 * time.Second,
		},
		Bus: BusConfig{
			Type: "local",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
	}
}
