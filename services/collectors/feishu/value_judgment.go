package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type messageValueClient struct {
	endpoint string
	client   *http.Client
}

type messageValueResponse struct {
	Valuable *bool `json:"valuable"`
}

func newMessageValueClient() *messageValueClient {
	timeout, err := strconv.ParseFloat(os.Getenv("MESSAGE_VALUE_TIMEOUT_SECONDS"), 64)
	if err != nil || timeout <= 0 {
		timeout = 10
	}
	endpoint := os.Getenv("MESSAGE_VALUE_EVALUATOR_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8000/evaluate/message"
	}
	return &messageValueClient{endpoint: endpoint, client: &http.Client{Timeout: time.Duration(timeout * float64(time.Second))}}
}

func (c *messageValueClient) isValuable(ctx context.Context, source string, raw map[string]any) bool {
	if c == nil || c.endpoint == "" {
		return true
	}
	payload, err := json.Marshal(map[string]any{"source": source, "message": raw})
	if err != nil {
		return true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return true
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "message value evaluator unavailable; message will be stored:", err)
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Fprintln(os.Stderr, "message value evaluator unavailable; message will be stored:", resp.Status, strings.TrimSpace(string(body)))
		return true
	}
	var result messageValueResponse
	if json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result) != nil || result.Valuable == nil {
		fmt.Fprintln(os.Stderr, "message value evaluator returned an invalid response; message will be stored")
		return true
	}
	return *result.Valuable
}
