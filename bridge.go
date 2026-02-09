package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Bridge connects NATS JetStream to OpenClaw webhooks.
type Bridge struct {
	cfg    Config
	nc     *nats.Conn
	js     jetstream.JetStream
	client *http.Client
}

// Envelope is the standard message format on NATS.
type Envelope struct {
	ID        string          `json:"id"`
	Version   string          `json:"version"`
	FleetID   string          `json:"fleetId"`
	Type      string          `json:"type"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Timestamp string          `json:"timestamp"`
	Priority  string          `json:"priority"`
	TTL       int             `json:"ttl"`
	Payload   json.RawMessage `json:"payload"`
}

// TaskPayload is the inner payload of a task request.
type TaskPayload struct {
	Task           string          `json:"task"`
	Params         json.RawMessage `json:"params"`
	ResponseFormat string          `json:"responseFormat"`
}

// NewBridge creates a new bridge instance.
func NewBridge(cfg Config) (*Bridge, error) {
	opts := []nats.Option{
		nats.Name("nats-bridge-" + cfg.AgentID),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("NATS disconnected: %v", err)
			setReady(false)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Println("NATS reconnected")
			setReady(true)
		}),
	}

	nc, err := nats.Connect(cfg.NatsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	return &Bridge{
		cfg:    cfg,
		nc:     nc,
		js:     js,
		client: &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Start begins consuming messages and sending heartbeats.
func (b *Bridge) Start(ctx context.Context) error {
	// Create or get stream for this fleet's tasks
	streamName := strings.ReplaceAll(b.cfg.FleetID, "-", "_") + "_tasks"
	stream, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: b.cfg.SubscribeTopics,
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}

	// Create durable consumer
	consumerName := b.cfg.AgentID + "-bridge"
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubjects: b.cfg.SubscribeTopics,
	})
	if err != nil {
		return fmt.Errorf("create consumer: %w", err)
	}

	// Consume messages
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			for msg := range msgs.Messages() {
				b.handleMessage(ctx, msg)
			}
			if msgs.Error() != nil && ctx.Err() == nil {
				// transient error, continue
				continue
			}
		}
	}()

	// Heartbeat loop
	go b.heartbeatLoop(ctx)

	return nil
}

func (b *Bridge) handleMessage(ctx context.Context, msg jetstream.Msg) {
	log.Printf("received message on %s (%d bytes)", msg.Subject(), len(msg.Data()))

	var env Envelope
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		log.Printf("ERROR: unmarshal envelope: %v", err)
		_ = msg.Nak()
		return
	}

	// Forward to OpenClaw via webhook
	resp, err := b.forwardToOpenClaw(ctx, env)
	if err != nil {
		log.Printf("ERROR: forward to openclaw: %v", err)
		_ = msg.NakWithDelay(10 * time.Second)
		return
	}

	// Extract domain from subject: fleet-tim.tasks.security.briefing → security
	domain := extractDomain(msg.Subject(), b.cfg.FleetID)

	// Publish result
	resultSubject := fmt.Sprintf("%s.%s.%s", b.cfg.ResultPrefix, domain, env.ID)
	result := map[string]interface{}{
		"id":        env.ID,
		"fleetId":   env.FleetID,
		"type":      "task.result",
		"from":      b.cfg.AgentID,
		"to":        env.From,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   json.RawMessage(resp),
	}

	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("ERROR: marshal result: %v", err)
		_ = msg.Nak()
		return
	}

	if _, err := b.js.Publish(ctx, resultSubject, data); err != nil {
		log.Printf("ERROR: publish result to %s: %v", resultSubject, err)
		// Still ACK — we processed it, just couldn't publish result
	}

	if err := msg.Ack(); err != nil {
		log.Printf("ERROR: ack message: %v", err)
	}

	log.Printf("processed task %s → %s", env.ID, resultSubject)
}

func (b *Bridge) forwardToOpenClaw(ctx context.Context, env Envelope) ([]byte, error) {
	// Extract task from payload
	var taskPayload TaskPayload
	if err := json.Unmarshal(env.Payload, &taskPayload); err != nil {
		// If payload isn't structured, use raw as the message
		taskPayload.Task = string(env.Payload)
	}

	// Build webhook payload for /hooks/agent
	body := map[string]interface{}{
		"message":    taskPayload.Task,
		"name":       fmt.Sprintf("NATS/%s", env.From),
		"sessionKey": fmt.Sprintf("nats:%s:%s", env.From, env.ID),
		"deliver":    false, // Don't deliver to chat — we handle results via NATS
	}

	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", b.cfg.OpenClawURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.OpenClawToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.OpenClawToken)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// /hooks/agent returns 202 (accepted) for async runs
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openclaw returned %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	log.Printf("openclaw accepted task (HTTP %d)", resp.StatusCode)
	return respBody, nil
}

func (b *Bridge) heartbeatLoop(ctx context.Context) {
	subject := fmt.Sprintf("%s.heartbeat.%s", b.cfg.FleetID, b.cfg.AgentID)
	ticker := time.NewTicker(b.cfg.HeartbeatInterval)
	defer ticker.Stop()

	// Send initial heartbeat
	b.sendHeartbeat(subject)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendHeartbeat(subject)
		}
	}
}

func (b *Bridge) sendHeartbeat(subject string) {
	hb := map[string]interface{}{
		"agentId":   b.cfg.AgentID,
		"fleetId":   b.cfg.FleetID,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"status":    "alive",
	}
	data, _ := json.Marshal(hb)
	if err := b.nc.Publish(subject, data); err != nil {
		log.Printf("ERROR: heartbeat publish: %v", err)
	}
}

// Close cleans up NATS connection.
func (b *Bridge) Close() {
	b.nc.Close()
}

// extractDomain pulls the domain segment from a NATS subject.
// e.g. "fleet-tim.tasks.security.briefing" → "security"
func extractDomain(subject, fleetID string) string {
	prefix := fleetID + ".tasks."
	if strings.HasPrefix(subject, prefix) {
		rest := strings.TrimPrefix(subject, prefix)
		parts := strings.SplitN(rest, ".", 2)
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return "unknown"
}
