package main

import (
	"log"
	"net/http"
	"sync/atomic"
)

var ready int32

func setReady(r bool) {
	if r {
		atomic.StoreInt32(&ready, 1)
	} else {
		atomic.StoreInt32(&ready, 0)
	}
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

	log.Printf("health server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("health server: %v", err)
	}
}
