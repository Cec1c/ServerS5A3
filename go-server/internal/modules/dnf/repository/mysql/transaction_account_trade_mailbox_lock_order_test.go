package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLAccountTradeMailboxLocksAccountBeforeParticipantsAndRollsBack(t *testing.T) {
	callbackErr := errors.New("injected account-trade-mailbox callback failure")
	tests := []struct {
		name              string
		mailSQLScenario   string
		callback          func(context.Context, context.CancelFunc, r.AccountRepository) error
		wantErr           error
		wantCommitErr     bool
		wantWriteErr      bool
		wantPanic         bool
		wantCallback      bool
		wantCharacterLock bool
		wantCommits       int
		wantRollbacks     int
	}{
		{name: "success", wantCallback: true, wantCharacterLock: true, wantCommits: 1},
		{
			name:            "account_missing",
			mailSQLScenario: "account_missing",
			wantErr:         r.ErrMailboxAssetTransactionUnavailable,
			wantRollbacks:   1,
		},
		{
			name:              "foreign_mailbox_owner",
			mailSQLScenario:   "foreign_owner",
			wantErr:           r.ErrAccountCharacterMailboxOwnership,
			wantCharacterLock: true,
			wantRollbacks:     1,
		},
		{
			name:              "callback_failure",
			callback:          func(context.Context, context.CancelFunc, r.AccountRepository) error { return callbackErr },
			wantErr:           callbackErr,
			wantCallback:      true,
			wantCharacterLock: true,
			wantRollbacks:     1,
		},
		{
			name:            "write_failure",
			mailSQLScenario: "write_failure",
			callback: func(ctx context.Context, _ context.CancelFunc, accounts r.AccountRepository) error {
				account, found, err := accounts.Load(ctx, "a")
				if err != nil {
					return err
				}
				if !found {
					return errors.New("account disappeared inside transaction")
				}
				account.Metadata = map[string]string{"account_token_cera": "100"}
				return accounts.Save(ctx, account)
			},
			wantWriteErr:      true,
			wantCallback:      true,
			wantCharacterLock: true,
			wantRollbacks:     1,
		},
		{
			name:              "commit_failure",
			mailSQLScenario:   "commit_failure",
			wantCommitErr:     true,
			wantCallback:      true,
			wantCharacterLock: true,
			wantCommits:       1,
		},
		{
			name:              "panic_rolls_back",
			callback:          func(context.Context, context.CancelFunc, r.AccountRepository) error { panic("injected panic") },
			wantPanic:         true,
			wantCallback:      true,
			wantCharacterLock: true,
			wantRollbacks:     1,
		},
		{
			name:              "cancel_after_callback_rolls_back",
			callback:          func(_ context.Context, cancel context.CancelFunc, _ r.AccountRepository) error { cancel(); return nil },
			wantErr:           context.Canceled,
			wantCallback:      true,
			wantCharacterLock: true,
			wantRollbacks:     1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := &accountMailSQLState{scenario: tc.mailSQLScenario}
			db := sql.OpenDB(accountMailSQLConnector{state})
			defer db.Close()
			u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{
				db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"},
				writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now,
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callbackRan := false
			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = u.WithinAccountTradeMailbox(ctx, "a", []string{"19"}, "19", func(accounts r.AccountRepository, _ r.CharacterRepository, _ r.InventoryRepository, _ r.MailboxRepository) error {
					callbackRan = true
					if tc.callback == nil {
						return nil
					}
					return tc.callback(ctx, cancel, accounts)
				})
			}()

			if state.begins != 1 {
				t.Fatalf("physical transactions=%d, want 1", state.begins)
			}
			if len(state.queries) == 0 {
				t.Fatal("transaction issued no locked read")
			}
			if first := state.queries[0]; !strings.HasPrefix(first, "SELECT account_id,") || !strings.HasSuffix(first, " FOR UPDATE") {
				t.Fatalf("first locked read is not the account: %s", first)
			}
			characterIndex := -1
			for i, query := range state.queries {
				if strings.Contains(query, "readonly") || !strings.HasSuffix(query, " FOR UPDATE") {
					t.Fatalf("query escaped write transaction or lock: %s", query)
				}
				if characterIndex < 0 && strings.HasPrefix(query, "SELECT character_id, account_id,") {
					characterIndex = i
				}
			}
			if tc.wantCharacterLock != (characterIndex > 0) {
				t.Fatalf("character lock index=%d, want lock=%t queries=%v", characterIndex, tc.wantCharacterLock, state.queries)
			}
			if callbackRan != tc.wantCallback {
				t.Fatalf("callback ran=%t, want %t", callbackRan, tc.wantCallback)
			}
			if panicked != tc.wantPanic {
				t.Fatalf("panic=%t, want %t", panicked, tc.wantPanic)
			}
			if tc.wantPanic {
				if err != nil {
					t.Fatalf("panic path returned error too: %v", err)
				}
			} else if tc.wantCommitErr {
				if err == nil || !strings.Contains(err.Error(), "commit failed") {
					t.Fatalf("commit error=%v, want injected commit failure", err)
				}
			} else if tc.wantWriteErr {
				if err == nil || !strings.Contains(err.Error(), "write failed") {
					t.Fatalf("write error=%v, want injected write failure", err)
				}
			} else if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error=%v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if state.commits != tc.wantCommits || state.rollbacks != tc.wantRollbacks {
				t.Fatalf("commit/rollback=%d/%d, want %d/%d", state.commits, state.rollbacks, tc.wantCommits, tc.wantRollbacks)
			}
		})
	}
}
