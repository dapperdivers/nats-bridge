package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

var ready int32

// bridgeRef holds a reference to the bridge for the HTTP publish endpoint.
var bridgeRef *Bridge

func setReady(r bool) {
	if r {
		atomic.StoreInt32(&ready, 1)
	} else {
		atomic.StoreInt32(&ready, 0)
	}
}

// PublishRequest is the JSON body for POST /publish.
type PublishRequest struct {
	Subject string          `json:"subject"`
	Data    json.RawMessage `json:"data"`
}

func startHealthServer(addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&ready) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
		}
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&ready) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
		}
	})

	// POST /publish — lets the local OpenClaw agent publish messages to NATS.
	// Only accessible from localhost (pod-internal).
	mux.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if atomic.LoadInt32(&ready) != 1 || bridgeRef == nil {
			http.Error(w, "bridge not ready", http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req PublishRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Subject == "" {
			http.Error(w, "subject is required", http.StatusBadRequest)
			return
		}
		if len(req.Data) == 0 {
			http.Error(w, "data is required", http.StatusBadRequest)
			return
		}

		// Publish via JetStream
		ctx := r.Context()
		ack, err := bridgeRef.js.Publish(ctx, req.Subject, []byte(req.Data))
		if err != nil {
			log.Printf("ERROR: /publish to %s: %v", req.Subject, err)
			http.Error(w, fmt.Sprintf("publish failed: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("published via /publish to %s (stream=%s seq=%d)", req.Subject, ack.Stream, ack.Sequence)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":       true,
			"stream":   ack.Stream,
			"sequence": ack.Sequence,
		})
	})

	// GET /info — returns bridge configuration for skill discovery.
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		if bridgeRef == nil {
			http.Error(w, "bridge not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"agentId":    bridgeRef.cfg.AgentID,
			"fleetId":    bridgeRef.cfg.FleetID,
			"ready":      atomic.LoadInt32(&ready) == 1,
			"resultPrefix": bridgeRef.cfg.ResultPrefix,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		})
	})

	log.Printf("health server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("health server: %v", err)
	}
}
