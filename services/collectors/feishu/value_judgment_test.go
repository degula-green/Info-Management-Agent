package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMessageValueClientSkipsDirectMessages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valuable":false}`))
	}))
	defer server.Close()
	client := &messageValueClient{endpoint: server.URL, client: server.Client()}
	if !client.isValuable(context.Background(), "feishu", map[string]any{"chat_id": "ou_direct"}) {
		t.Fatal("direct messages must be retained")
	}
	if calls != 0 {
		t.Fatalf("expected no evaluator call for direct message, got %d", calls)
	}
}

func TestMessageValueClientSkipsFeishuP2PMessages(t *testing.T) {
	client := &messageValueClient{endpoint: "http://127.0.0.1:1/evaluate/message", client: http.DefaultClient}
	if !client.isValuable(context.Background(), "feishu", map[string]any{
		"chat_id":   "oc_p2p",
		"chat_type": "p2p",
	}) {
		t.Fatal("Feishu p2p messages must be retained without evaluator calls")
	}
}

func TestMessageValueClientFiltersExplicitFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valuable":false}`))
	}))
	defer server.Close()
	client := &messageValueClient{endpoint: server.URL, client: server.Client()}
	if client.isValuable(context.Background(), "feishu", map[string]any{"chat_id": "oc_group", "msg_type": "text"}) {
		t.Fatal("explicit valuable=false must filter the message")
	}
}

func TestMessageValueClientFailsOpen(t *testing.T) {
	client := &messageValueClient{endpoint: "http://127.0.0.1:1/evaluate/message", client: http.DefaultClient}
	if !client.isValuable(context.Background(), "feishu", map[string]any{"chat_id": "oc_group"}) {
		t.Fatal("unavailable evaluator must retain the message")
	}
}
