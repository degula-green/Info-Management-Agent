package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type supervisedProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// superviseFeishuAccounts runs one isolated child worker per active source
// account. Reconciliation lets OAuth binds and unbinds take effect without
// restarting the service and prevents one account failure from stopping all
// other tenants.
func superviseFeishuAccounts(ctx context.Context, pool *pgxpool.Pool, mode, root string, interval, pageSize int, databaseURL, redisURL, redisDB, appID, appSecret string) {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve collector executable:", err)
		return
	}
	workers := map[string]*supervisedProcess{}
	reconcile := func() {
		rows, err := pool.Query(ctx, `SELECT sa.external_account_id FROM ingestion.source_accounts sa
            JOIN ingestion.collector_bindings b ON b.source_account_id=sa.id AND b.collector_type='feishu'
            WHERE sa.platform='feishu' AND sa.status='active' AND b.enabled=true ORDER BY sa.id`)
		if err != nil {
			fmt.Fprintln(os.Stderr, "list active Feishu accounts:", err)
			return
		}
		active := map[string]bool{}
		for rows.Next() {
			var account string
			if rows.Scan(&account) == nil && account != "" {
				active[externalAccountID(account)] = true
			}
		}
		rows.Close()
		for account, worker := range workers {
			select {
			case <-worker.done:
				delete(workers, account)
			default:
			}
			if !active[account] {
				worker.cancel()
				delete(workers, account)
			}
		}
		for account := range active {
			if _, ok := workers[account]; ok {
				continue
			}
			workerCtx, cancel := context.WithCancel(ctx)
			args := []string{"--account", account, "--mode", mode, "--data-dir", root,
				"--interval", strconv.Itoa(interval), "--page-size", strconv.Itoa(pageSize)}
			if mode == "collector" {
				args = append(args, "--watch")
			}
			cmd := exec.CommandContext(workerCtx, executable, args...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			cmd.Env = append(os.Environ(),
				"COLLECTOR_DATABASE_URL="+databaseURL,
				"CORE_REDIS_URL="+redisURL,
				"CORE_REDIS_DATABASE="+redisDB,
				"FEISHU_APP_ID="+appID,
				"FEISHU_APP_SECRET="+appSecret,
			)
			done := make(chan struct{})
			workers[account] = &supervisedProcess{cancel: cancel, done: done}
			go func(account string) {
				defer close(done)
				if err := cmd.Run(); err != nil && workerCtx.Err() == nil {
					fmt.Fprintf(os.Stderr, "Feishu %s worker %s exited: %v\n", mode, account, err)
				}
			}(account)
		}
	}

	reconcile()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, worker := range workers {
				worker.cancel()
			}
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
