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

func feishuProfileKey(sender map[string]any) string {
	id, _ := sender["id"].(string)
	idType, _ := sender["id_type"].(string)
	return strings.TrimSpace(idType) + ":" + strings.TrimSpace(id)
}

func isFeishuUserIDType(value string) bool {
	return value == "open_id" || value == "user_id" || value == "union_id"
}

func enrichFeishuSender(ctx context.Context, token *string, stored *credential, raw map[string]any, cache map[string]map[string]any, appID, appSecret, redisURL, redisDB, credentialKey string) {
	sender, ok := raw["sender"].(map[string]any)
	if !ok || strings.TrimSpace(feishuProfileKey(sender)) == ":" {
		return
	}
	key := feishuProfileKey(sender)
	idType, _ := sender["id_type"].(string)
	if !isFeishuUserIDType(idType) {
		// App and bot identities are not contact records and cannot be resolved
		// through the Contact user API.
		cache[key] = nil
		return
	}
	profile, known := cache[key]
	if !known {
		id, _ := sender["id"].(string)
		query := url.Values{}
		query.Set("user_id_type", idType)
		var response struct {
			Data struct {
				User map[string]any `json:"user"`
			} `json:"data"`
		}
		if err := getWithRefresh(ctx, token, stored, "/contact/v3/users/"+url.PathEscape(id), query, &response, appID, appSecret, redisURL, redisDB, credentialKey); err != nil {
			// An unavailable cross-tenant profile must not stop message ingestion.
			// Keep the platform ID private while preserving the API diagnostic.
			fmt.Fprintf(os.Stderr, "feishu participant profile lookup failed for %s: %v\n", idType, err)
			cache[key] = nil
			return
		}
		profile = response.Data.User
		cache[key] = profile
	}
	if profile == nil {
		return
	}
	if name, ok := profile["name"].(string); ok && strings.TrimSpace(name) != "" {
		sender["name"] = strings.TrimSpace(name)
	}
	if avatar, ok := profile["avatar"]; ok {
		sender["avatar"] = avatar
	}
}

func refreshFeishuParticipantProfiles(ctx context.Context, pool *pgxpool.Pool, account string, token *string, stored *credential, cache map[string]map[string]any, appID, appSecret, redisURL, redisDB, credentialKey string) {
	rows, err := pool.Query(ctx, `SELECT p.external_participant_id,p.id_type FROM ingestion.participants p
        JOIN ingestion.source_accounts sa ON sa.id=p.source_account_id
        WHERE sa.platform='feishu' AND sa.external_account_id=$1
          AND (COALESCE(btrim(p.display_name),'')='' OR COALESCE(btrim(p.avatar_url),'')='')`, externalAccountID(account))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, idType string
		if rows.Scan(&id, &idType) != nil || id == "" {
			continue
		}
		raw := map[string]any{"sender": map[string]any{"id": id, "id_type": idType}}
		enrichFeishuSender(ctx, token, stored, raw, cache, appID, appSecret, redisURL, redisDB, credentialKey)
		sender := raw["sender"].(map[string]any)
		_ = updateFeishuParticipantProfile(ctx, pool, account, id, participantDisplayName(sender["name"]), participantAvatarURL(sender["avatar"]))
	}
}

// Reload credentials every polling round so OAuth refreshes made by Core (or
// another collector instance) take effect without restarting this process.
func reloadCredential(ctx context.Context, token *string, stored *credential, account, redisURL, redisDB, appID, appSecret string) {
	if redisURL == "" {
		return
	}
	key := "credential:feishu:" + account
	b, err := redisGet(ctx, redisURL, redisDB, key)
	if err != nil {
		return
	}
	var latest credential
	if json.Unmarshal(b, &latest) != nil || latest.AccessToken == "" {
		return
	}
	*stored = latest
	*token = latest.AccessToken
	if latest.ExpiresAt > 0 && latest.ExpiresAt < time.Now().Add(5*time.Minute).Unix() && latest.RefreshToken != "" && appID != "" && appSecret != "" {
		if fresh, e := refreshAccessToken(ctx, appID, appSecret, latest.RefreshToken, redisURL, redisDB, key); e == nil {
			*stored, *token = fresh, fresh.AccessToken
		}
	}
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
	// Account identity is resolved from the active logged-in connector below.
	// Keep --account only as an explicit diagnostic/compatibility override.
	account := flag.String("account", "", "source account id override")
	root := flag.String("data-dir", "data/collector", "local collector data directory")
	watch := flag.Bool("watch", false, "poll continuously")
	once := flag.Bool("once", false, "poll once and exit")
	interval := flag.Int("interval", intervalDefault, "poll interval in seconds")
	pageSize := flag.Int("page-size", pageDefault, "page size")
	workerEnabled := flag.Bool("worker-enabled", os.Getenv("FEISHU_WORKER_ENABLED") != "false", "run collector worker")
	mode := flag.String("mode", "collector", "collector or attachment-worker")
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
	if *account == "" && ((*mode == "collector" && *watch) || *mode == "attachment-worker") {
		superviseFeishuAccounts(stopCtx, pool, *mode, *root, *interval, *pageSize, *databaseURL, *redisURL, *redisDB, *appID, *appSecret)
		return
	}
	// Resolve the account from active OAuth connections rather than relying on
	// the legacy .env default. The newest active account with a Redis credential
	// represents the currently connected/login account and works without a
	// collector restart after a new OAuth binding.
	var rows interface {
		Next() bool
		Scan(...any) error
		Close()
	}
	if *account == "" {
		rows, _ = pool.Query(context.Background(), `SELECT external_account_id FROM ingestion.source_accounts WHERE platform='feishu' AND status='active' ORDER BY updated_at DESC NULLS LAST, id DESC`)
	}
	if rows != nil {
		for rows.Next() {
			var candidate string
			if rows.Scan(&candidate) != nil || candidate == "" {
				continue
			}
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			b, probeErr := redisGet(probeCtx, *redisURL, *redisDB, "credential:feishu:"+candidate)
			probeCancel()
			var candidateCredential credential
			if probeErr == nil && json.Unmarshal(b, &candidateCredential) == nil && candidateCredential.AccessToken != "" {
				*account = candidate
				break
			}
		}
		rows.Close()
	}
	if *account == "" {
		fmt.Fprintln(os.Stderr, "no active Feishu connector with a Redis credential was found")
		os.Exit(2)
	}
	*account = externalAccountID(*account)
	if *mode == "attachment-worker" {
		runFeishuAttachmentWorker(pool, *account, *redisURL, *redisDB, *appID, *appSecret)
		return
	}
	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 5*time.Second)
	reloadCredential(refreshCtx, token, &stored, *account, *redisURL, *redisDB, *appID, *appSecret)
	refreshCancel()
	if *token == "" {
		message := "no usable Feishu Redis credential was found for the active connector"
		accountHeartbeat(context.Background(), pool, *account, "error", 0, 1, &message)
		fmt.Fprintln(os.Stderr, "no usable Feishu Redis credential was found for the active connector")
		os.Exit(2)
	}
	profiles := map[string]map[string]any{}
	valueEvaluator := newMessageValueClient()
	accountHeartbeat(context.Background(), pool, *account, "running", 0, 0, nil)
	seenPath := filepath.Join(*root, "seen-feishu-"+*account+".json")
	seen := map[string]bool{}
	if b, err := os.ReadFile(seenPath); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	for {
		if stopCtx.Err() != nil {
			accountHeartbeat(context.Background(), pool, *account, "stopped", 0, 0, nil)
			return
		}
		roundCtx, roundCancel := context.WithTimeout(stopCtx, 5*time.Second)
		reloadCredential(roundCtx, token, &stored, *account, *redisURL, *redisDB, *appID, *appSecret)
		roundCancel()
		refreshFeishuParticipantProfiles(stopCtx, pool, *account, token, &stored, profiles, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account)
		var chats page
		q := url.Values{"page_size": {strconv.Itoa(*pageSize)}}
		if err := getWithRefresh(stopCtx, token, &stored, "/im/v1/chats", q, &chats, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account); err != nil {
			message := err.Error()
			accountHeartbeat(context.Background(), pool, *account, "error", 0, 1, &message)
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		listen, listenErr := loadListenConfig(stopCtx, pool, *account)
		if listenErr != nil || len(listen.Selected) == 0 {
			// Keep the selectable conversation directory fresh while the empty
			// whitelist pauses message ingestion.
			for _, chat := range chats.Data.Items {
				id, _ := chat["chat_id"].(string)
				name := chatDisplayName(chat)
				if id != "" {
					// Some Feishu tenants return only chat_id from the list
					// endpoint. Resolve the detail resource so the UI can show the
					// actual chat name and avatar.
					if name == "" {
						var detail struct {
							Data map[string]any `json:"data"`
						}
						if err := getWithRefresh(stopCtx, token, &stored, "/im/v1/chats/"+url.PathEscape(id), nil, &detail, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account); err == nil && detail.Data != nil {
							for k, v := range detail.Data {
								chat[k] = v
							}
							name = chatDisplayName(chat)
						}
					}
					_ = upsertConversationMetadata(stopCtx, pool, *account, id, name, chat)
				}
			}
			accountHeartbeat(context.Background(), pool, *account, "running", 0, 0, nil)
			if !*watch {
				return
			}
			time.Sleep(time.Duration(*interval) * time.Second)
			continue
		}
		for _, chat := range chats.Data.Items {
			id, _ := chat["chat_id"].(string)
			if id == "" {
				continue
			}
			name := chatDisplayName(chat)
			if name == "" {
				var detail struct {
					Data map[string]any `json:"data"`
				}
				if err := getWithRefresh(stopCtx, token, &stored, "/im/v1/chats/"+url.PathEscape(id), nil, &detail, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account); err == nil && detail.Data != nil {
					for k, v := range detail.Data {
						chat[k] = v
					}
					name = chatDisplayName(chat)
				}
			}
			_ = upsertConversationMetadata(stopCtx, pool, *account, id, name, chat)
			if !listen.Selected[id] {
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
					when, _ := occurredAt(raw)
					startAt := listen.HistoryFrom
					if startAt == nil {
						startAt = listen.UpdatedAt
					}
					if startAt != nil && when != nil && when.Before(*startAt) {
						continue
					}
					enrichFeishuSender(stopCtx, token, &stored, raw, profiles, *appID, *appSecret, *redisURL, *redisDB, "credential:feishu:"+*account)
					key := *account + ":" + mid
					if seen[key] {
						continue
					}
					evaluationRaw := make(map[string]any, len(raw)+4)
					for field, value := range raw {
						evaluationRaw[field] = value
					}
					evaluationRaw["chat_id"] = id
					for _, field := range []string{"chat_type", "chat_mode", "name", "chat_name"} {
						if value, exists := chat[field]; exists {
							evaluationRaw[field] = value
						}
					}
					evaluationCtx, evaluationCancel := context.WithTimeout(stopCtx, 15*time.Second)
					valuable := valueEvaluator.isValuable(evaluationCtx, "feishu", evaluationRaw)
					evaluationCancel()
					if !valuable {
						seen[key] = true
						checkpointCtx, checkpointCancel := context.WithTimeout(context.Background(), 10*time.Second)
						if err := saveCheckpoint(checkpointCtx, pool, *account, id, messages.Data.PageToken, mid, when); err != nil {
							fmt.Fprintln(os.Stderr, "checkpoint filtered message:", err)
						}
						checkpointCancel()
						continue
					}
					if err := appendEvent(*root, *account, event{"feishu", *account, mid, time.Now().UTC().Format(time.RFC3339), evaluationRaw}); err != nil {
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
					checkpointCtx, checkpointCancel := context.WithTimeout(context.Background(), 10*time.Second)
					if err := saveCheckpoint(checkpointCtx, pool, *account, id, messages.Data.PageToken, mid, when); err != nil {
						fmt.Fprintln(os.Stderr, "checkpoint:", err)
					}
					checkpointCancel()
				}
				// Advance the page cursor even when all messages on this page are
				// older than the configured history window.
				if messages.Data.PageToken != "" {
					checkpointCtx, checkpointCancel := context.WithTimeout(context.Background(), 10*time.Second)
					_ = saveCheckpoint(checkpointCtx, pool, *account, id, messages.Data.PageToken, "", nil)
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
		accountHeartbeat(context.Background(), pool, *account, "running", 1, 0, nil)
		select {
		case <-stopCtx.Done():
			return
		case <-time.After(time.Duration(*interval) * time.Second):
		}
	}
}

func chatDisplayName(chat map[string]any) string {
	for _, key := range []string{"name", "chat_name", "display_name", "nickname"} {
		if value, ok := chat[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
