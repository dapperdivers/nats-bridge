package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config holds all configuration for the nats-bridge sidecar.
type Config struct {
	NatsURL           string
	FleetID           string
	AgentID           string
	SubscribeTopics   []string
	OpenClawURL       string
	OpenClawToken     string
	HeartbeatInterval time.Duration
	ResultPrefix      string
}

func loadConfig() Config {
	c := Config{
		NatsURL:           envOr("NATS_URL", "nats://nats.roundtable.svc:4222"),
		FleetID:           envOr("FLEET_ID", "fleet-default"),
		AgentID:           envOr("AGENT_ID", "unknown"),
		OpenClawURL:       envOr("OPENCLAW_WEBHOOK_URL", "http://localhost:18789/v1/chat/completions"),
		OpenClawToken:     os.Getenv("OPENCLAW_TOKEN"),
		HeartbeatInterval: 60 * time.Second,
	}

	if topics := os.Getenv("SUBSCRIBE_TOPICS"); topics != "" {
		c.SubscribeTopics = strings.Split(topics, ",")
	} else {
		c.SubscribeTopics = []string{c.FleetID + ".tasks.>"}
	}

	if v := os.Getenv("HEARTBEAT_INTERVAL"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			c.HeartbeatInterval = time.Duration(s) * time.Second
		}
	}

	c.ResultPrefix = envOr("RESULT_PREFIX", c.FleetID+".results")

	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := loadConfig()

	log.Printf("nats-bridge starting agent=%s fleet=%s nats=%s", cfg.AgentID, cfg.FleetID, cfg.NatsURL)
	log.Printf("subscribing to: %v", cfg.SubscribeTopics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start health server
	go startHealthServer(":8080")

	// Start bridge
	b, err := NewBridge(cfg)
	if err != nil {
		log.Fatalf("failed to create bridge: %v", err)
	}
	defer b.Close()

	if err := b.Start(ctx); err != nil {
		log.Fatalf("failed to start bridge: %v", err)
	}

	setReady(true)
	log.Println("nats-bridge ready")

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	log.Println("shutting down...")
	setReady(false)
	cancel()

	// Give in-flight requests time to finish
	time.Sleep(2 * time.Second)
	log.Println("bye")
}
