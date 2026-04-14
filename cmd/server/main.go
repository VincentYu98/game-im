package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"game-im/configs"
	"game-im/internal/app"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("failed to create app: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := application.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("app start error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()

	if err := application.Stop(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func loadConfig(path string) (*configs.Config, error) {
	cfg := configs.DefaultConfig()

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		// Config file is optional; use defaults if not found.
		log.Printf("config file not loaded (%s), using defaults: %v", path, err)
		return cfg, nil
	}

	if err := k.Unmarshal("", cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
