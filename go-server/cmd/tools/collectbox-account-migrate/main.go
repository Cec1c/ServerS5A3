// collectbox-account-migrate previews or imports legacy account-level
// collectbox_state_v1 JSON into the account inventory's reserved 101 container.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	dnfcollectbox "longheng.io/server/internal/modules/dnf/collectbox"
)

const legacyMetadataKey = "collectbox_state_v1"

type migrationReport struct {
	Apply                 bool `json:"apply"`
	LegacyAccounts        int  `json:"legacy_accounts"`
	LegacyRows            int  `json:"legacy_rows"`
	InsertedRows          int  `json:"inserted_rows"`
	ExistingMatchingRows  int  `json:"existing_matching_rows"`
	ExtraRowsUpserted     int  `json:"extra_rows_upserted"`
	ConflictingTargetRows int  `json:"conflicting_target_rows"`
}

func main() {
	apply := flag.Bool("apply", false, "write the migration; without this flag the tool only previews")
	dsnEnv := flag.String("dsn-env", "DNF90_MYSQL_DSN", "environment variable containing the MySQL DSN")
	reportPath := flag.String("report", "", "optional JSON report path")
	flag.Parse()

	dsn := os.Getenv(*dsnEnv)
	if dsn == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", *dsnEnv)
		os.Exit(2)
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report, err := migrate(ctx, database, *apply)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

type accountLegacyRows struct {
	accountID string
	rows      []dnfcollectbox.LegacyRegistration
}

func migrate(ctx context.Context, database *sql.DB, apply bool) (migrationReport, error) {
	report := migrationReport{Apply: apply}
	if apply {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		defer tx.Rollback()
		if err := migrateTx(ctx, tx, &report); err != nil {
			return report, err
		}
		return report, tx.Commit()
	}
	if err := migrateTx(ctx, database, &report); err != nil {
		return report, err
	}
	return report, nil
}

type queryExecutor interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func migrateTx(ctx context.Context, executor queryExecutor, report *migrationReport) error {
	query := "SELECT account_id, entry_value FROM dnf_account_metadata WHERE entry_key = ? ORDER BY account_id"
	if report.Apply {
		query += " FOR UPDATE"
	}
	rows, err := executor.QueryContext(ctx, query, legacyMetadataKey)
	if err != nil {
		return fmt.Errorf("query legacy collectbox metadata: %w", err)
	}
	var accounts []accountLegacyRows
	for rows.Next() {
		var accountID, raw string
		if err := rows.Scan(&accountID, &raw); err != nil {
			rows.Close()
			return err
		}
		parsed, err := dnfcollectbox.ParseLegacyAccountState(raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("parse legacy collectbox metadata: %w", err)
		}
		accounts = append(accounts, accountLegacyRows{accountID: accountID, rows: parsed})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	report.LegacyAccounts = len(accounts)

	for _, account := range accounts {
		report.LegacyRows += len(account.rows)
		for _, row := range account.rows {
			var existingItemID int64
			lookup := "SELECT item_id FROM dnf_account_inventory_items WHERE account_id = ? AND entry_key = ?"
			if report.Apply {
				lookup += " FOR UPDATE"
			}
			err := executor.QueryRowContext(ctx, lookup, account.accountID, row.EntryKey).Scan(&existingItemID)
			switch {
			case err == nil:
				if existingItemID != int64(row.ItemID) {
					report.ConflictingTargetRows++
					return fmt.Errorf("target account inventory key %s already holds item %d, legacy item is %d",
						row.EntryKey, existingItemID, row.ItemID)
				}
				report.ExistingMatchingRows++
			case errors.Is(err, sql.ErrNoRows):
				if report.Apply {
					if _, err := executor.ExecContext(ctx,
						`INSERT INTO dnf_account_inventory_items
						 (account_id, entry_key, item_id, item_count, bind_flag, expire_at, raw_entry)
						 VALUES (?, ?, ?, 1, 0, NULL, NULL)`,
						account.accountID, row.EntryKey, row.ItemID,
					); err != nil {
						return fmt.Errorf("insert account collectbox item %s: %w", row.EntryKey, err)
					}
				}
				report.InsertedRows++
			default:
				return fmt.Errorf("inspect account collectbox item %s: %w", row.EntryKey, err)
			}

			if report.Apply {
				for key, value := range map[string]string{
					"collectbox_box_index":  fmt.Sprint(row.BoxIndex),
					"collectbox_slot_index": fmt.Sprint(row.SlotIndex),
					"source":                "legacy_collectbox_state_v1",
				} {
					if _, err := executor.ExecContext(ctx,
						`INSERT INTO dnf_account_inventory_item_extra
						 (account_id, entry_key, extra_key, extra_value)
						 VALUES (?, ?, ?, ?)
						 ON DUPLICATE KEY UPDATE extra_value = VALUES(extra_value)`,
						account.accountID, row.EntryKey, key, value,
					); err != nil {
						return fmt.Errorf("upsert collectbox extra %s/%s: %w", row.EntryKey, key, err)
					}
					report.ExtraRowsUpserted++
				}
			}
		}
	}
	return nil
}
