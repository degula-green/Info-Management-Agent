package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type page struct {
	Data struct {
		Items     []map[string]any `json:"items"`
		HasMore   bool             `json:"has_more"`
		PageToken string           `json:"page_token"`
	} `json:"data"`
}
type event struct {
	Source          string         `json:"source"`
	SourceAccountID string         `json:"source_account_id"`
	SourceMessageID string         `json:"source_message_id"`
	CollectedAt     string         `json:"collected_at"`
	RawPayload      map[string]any `json:"raw_payload"`
}

type credential struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	OpenID          string `json:"open_id"`
	SourceAccountID string `json:"source_account_id"`
	Name            string `json:"name"`
	ExpiresAt       int64  `json:"expires_at"`
}

var errUnauthorized = errors.New("feishu unauthorized")

func refreshAccessToken(ctx context.Context, appID, appSecret, refresh, redisURL, redisDB, key string) (credential, error) {
	payload, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "app_id": appID, "app_secret": appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/authen/v1/refresh_access_token", strings.NewReader(string(payload)))
	if err != nil {
		return credential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return credential{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Code != 0 || body.Data.AccessToken == "" {
		return credential{}, fmt.Errorf("refresh token failed: %s", body.Msg)
	}
	c := credential{AccessToken: body.Data.AccessToken, RefreshToken: body.Data.RefreshToken, OpenID: key[strings.LastIndex(key, ":")+1:], ExpiresAt: time.Now().Add(time.Duration(body.Data.ExpiresIn) * time.Second).Unix()}
	if c.RefreshToken == "" {
		c.RefreshToken = refresh
	}
	b, _ := json.Marshal(c)
	if err := redisSet(ctx, redisURL, redisDB, key, b, 30*24*time.Hour); err != nil {
		return credential{}, err
	}
	return c, nil
}

func get(token, path string, query url.Values, out any) error {
	u := "https://open.feishu.cn/open-apis" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized {
			return errUnauthorized
		}
		return fmt.Errorf("feishu %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func getWithRefresh(ctx context.Context, token *string, stored *credential, path string, query url.Values, out any, appID, appSecret, redisURL, redisDB, key string) error {
	err := get(*token, path, query, out)
	if !errors.Is(err, errUnauthorized) || stored.RefreshToken == "" || appID == "" || appSecret == "" {
		return err
	}
	fresh, refreshErr := refreshAccessToken(ctx, appID, appSecret, stored.RefreshToken, redisURL, redisDB, key)
	if refreshErr != nil {
		return refreshErr
	}
	*stored, *token = fresh, fresh.AccessToken
	return get(*token, path, query, out)
}

func appendEvent(root, account string, ev event) error {
	day := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(root, "raw", "feishu", account, day+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(ev)
	_, err = f.Write(append(b, '\n'))
	return err
}

func main() {
	intervalDefault, _ := strconv.Atoi(os.Getenv("FEISHU_POLL_INTERVAL"))
	if intervalDefault <= 0 {
		intervalDefault = 60
	}
	pageDefault, _ := strconv.Atoi(os.Getenv("FEISHU_PAGE_SIZE"))
	if pageDefault <= 0 {
		pageDefault = 50
	}
	token := flag.String("token", os.Getenv("FEISHU_ACCESS_TOKEN"), "user access token")
	account := flag.String("account", os.Getenv("FEISHU_SOURCE_ACCOUNT_ID"), "source account id")
	root := flag.String("data-dir", "data/collector", "local collector data directory")
	watch := flag.Bool("watch", false, "poll continuously")
	once := flag.Bool("once", false, "poll once and exit")
	interval := flag.Int("interval", intervalDefault, "poll interval in seconds")
	pageSize := flag.Int("page-size", pageDefault, "page size")
	workerEnabled := flag.Bool("worker-enabled", os.Getenv("FEISHU_WORKER_ENABLED") != "false", "run collector worker")
	credentialFile := flag.String("credential-file", os.Getenv("FEISHU_CREDENTIAL_FILE"), "OAuth credential JSON file")
	databaseURL := flag.String("database-url", os.Getenv("COLLECTOR_DATABASE_URL"), "PostgreSQL connection URL")
	redisURL := flag.String("redis-url", os.Getenv("CORE_REDIS_URL"), "Redis connection URL")
	redisDB := flag.String("redis-database", os.Getenv("CORE_REDIS_DATABASE"), "Redis database number")
	appID := flag.String("feishu-app-id", os.Getenv("FEISHU_APP_ID"), "Feishu app id")
	appSecret := flag.String("feishu-app-secret", os.Getenv("FEISHU_APP_SECRET"), "Feishu app secret")
	flag.Parse()
	stored := credential{}
	if !*workerEnabled {
		return
	}
	if *redisURL == "" {
		loadCoreEnv(&redisURL, &redisDB)
	}
	if *once {
		*watch = false
	}
	if *credentialFile != "" {
		if b, err := os.ReadFile(*credentialFile); err == nil {
			var c credential
			if json.Unmarshal(b, &c) == nil {
				if *token == "" {
					*token = c.AccessToken
				}
				stored = c
				if *account == "" {
					*account = c.SourceAccountID
				}
			}
		}
	}
	if *account == "" {
		fmt.Fprintln(os.Stderr, "FEISHU_SOURCE_ACCOUNT_ID is required")
		os.Exit(2)
	}
	*account = externalAccountID(*account)
	if *redisURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if b, err := redisGet(ctx, *redisURL, *redisDB, "credential:feishu:"+*account); err == nil {
			var redisCredential credential
			if json.Unmarshal(b, &redisCredential) == nil {
				stored = redisCredential
				if *token == "" {
					*token = stored.AccessToken
				}
				if stored.ExpiresAt > 0 && stored.ExpiresAt < time.Now().Add(5*time.Minute).Unix() && stored.RefreshToken != "" && *appID != "" && *appSecret != "" {
					if fresh, e := refreshAccessToken(ctx, *appID, *appSecret, stored.RefreshToken, *redisURL, *redisDB, "credential:feishu:"+*account); e == nil {
						*token = fresh.AccessToken
					}
				}
			}
		}
		cancel()
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "FEISHU_ACCESS_TOKEN and FEISHU_SOURCE_ACCOUNT_ID are required")
		os.Exit(2)
	}
	if *databaseURL == "" {
		fmt.Fprintln(os.Stderr, "COLLECTOR_DATABASE_URL is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "database:", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		fmt.Fprintln(os.Stderr, "database ping:", err)
		os.Exit(1)
	}
	cancel()
	defer pool.Close()
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	heartbeat(context.Background(), pool, "feishu-collector", "running", 0, 0, nil)
	seenPath := filepath.Join(*root, "seen-feishu.json")
	seen := map[string]bool{}
	if b, err := os.ReadFile(seenPath); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	for {
		if stopCtx.Err() != nil {
			heartbeat(context.Background(), pool, "feishu-collector", "stopped", 0, 0, nil)
			return
		}
		var chats page
		q := url.Values{"page_size": {strconv.Itoa(*pageSize)}}
		if err := getWithRefresh(stopCtx, token, &stored, "/im/v1/chats", q, &chats, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, chat := range chats.Data.Items {
			id, _ := chat["chat_id"].(string)
			if id == "" {
				continue
			}
			pageToken, _ := loadCheckpoint(stopCtx, pool, *account, id)
			for {
				mq := url.Values{"container_id_type": {"chat"}, "container_id": {id}, "page_size": {strconv.Itoa(*pageSize)}}
				if pageToken != "" {
					mq.Set("page_token", pageToken)
				}
				var messages page
				if err := getWithRefresh(stopCtx, token, &stored, "/im/v1/messages", mq, &messages, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account); err != nil {
					fmt.Fprintln(os.Stderr, err)
					break
				}
				for _, raw := range messages.Data.Items {
					mid, _ := raw["message_id"].(string)
					if mid == "" {
						continue
					}
					key := *account + ":" + mid
					if seen[key] {
						continue
					}
					if err := appendEvent(*root, *account, event{"feishu", *account, mid, time.Now().UTC().Format(time.RFC3339), raw}); err != nil {
						fmt.Fprintln(os.Stderr, err)
						continue
					}
					persistCtx, persistCancel := context.WithTimeout(context.Background(), 15*time.Second)
					if err := persistMessage(persistCtx, pool, *account, raw); err != nil {
						persistCancel()
						fmt.Fprintln(os.Stderr, "database persist:", err)
						continue
					}
					persistCancel()
					seen[key] = true
					when, _ := occurredAt(raw)
					checkpointCtx, checkpointCancel := context.WithTimeout(context.Background(), 10*time.Second)
					if err := saveCheckpoint(checkpointCtx, pool, *account, id, messages.Data.PageToken, mid, when); err != nil {
						fmt.Fprintln(os.Stderr, "checkpoint:", err)
					}
					checkpointCancel()
				}
				if !messages.Data.HasMore || messages.Data.PageToken == "" {
					break
				}
				pageToken = messages.Data.PageToken
			}
		}
		b, _ := json.MarshalIndent(seen, "", "  ")
		_ = os.WriteFile(seenPath, b, 0o600)
		if !*watch {
			return
		}
		heartbeat(context.Background(), pool, "feishu-collector", "running", 1, 0, nil)
		select {
		case <-stopCtx.Done():
			return
		case <-time.After(time.Duration(*interval) * time.Second):
		}
	}
}
