package main

import (
	"context"
	"log"
	"time"

	"info-agent/core/internal/config"
	"info-agent/core/internal/database"
	"info-agent/core/internal/redisstore"
	"info-agent/core/internal/server"
)

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database initialization failed: %v", err)
	}
	defer pool.Close()
	redisClient := redisstore.New(cfg.RedisURL)
	app := server.New(pool, cfg, redisClient)
	log.Printf("core service listening on :%s", cfg.HTTPPort)
	if err := app.Run(":" + cfg.HTTPPort); err != nil {
		log.Fatal(err)
	}
}
