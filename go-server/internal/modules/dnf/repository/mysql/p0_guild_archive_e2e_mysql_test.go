package mysql_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/mysql"
)

type p0E2EMySQLState struct {
	mu          sync.Mutex
	queries     []string
	valueWrites []string
	commits     int
	rollbacks   int
	primary     map[string]map[string]string
	replica     map[string]map[string]string
}

type p0E2EMySQLDriver struct{ state *p0E2EMySQLState }

func (d *p0E2EMySQLDriver) Open(string) (driver.Conn, error) {
	return &p0E2EMySQLConn{state: d.state}, nil
}

type p0E2EMySQLConn struct{ state *p0E2EMySQLState }

type p0E2EMySQLTx struct{ state *p0E2EMySQLState }

func (t *p0E2EMySQLTx) Commit() error {
	t.state.mu.Lock()
	t.state.commits++
	t.state.mu.Unlock()
	return nil
}

func (t *p0E2EMySQLTx) Rollback() error {
	t.state.mu.Lock()
	t.state.rollbacks++
	t.state.mu.Unlock()
	return nil
}

func (c *p0E2EMySQLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *p0E2EMySQLConn) Close() error                        { return nil }
func (c *p0E2EMySQLConn) Begin() (driver.Tx, error)           { return &p0E2EMySQLTx{state: c.state}, nil }
func (c *p0E2EMySQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &p0E2EMySQLTx{state: c.state}, nil
}

func (c *p0E2EMySQLConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.Contains(query, "`dnf_setting_values`") {
		c.state.valueWrites = append(c.state.valueWrites, query)
	}
	if strings.Contains(query, "`dnf_write_primary`.`dnf_settings`") && len(args) >= 1 {
		scope, _ := args[0].Value.(string)
		if _, ok := c.state.primary[scope]; !ok {
			c.state.primary[scope] = map[string]string{}
		}
	} else if strings.HasPrefix(query, "DELETE FROM `dnf_write_primary`.`dnf_setting_values`") && len(args) >= 1 {
		scope, _ := args[0].Value.(string)
		c.state.primary[scope] = map[string]string{}
	} else if strings.HasPrefix(query, "INSERT INTO `dnf_write_primary`.`dnf_setting_values`") && len(args) == 3 {
		scope, _ := args[0].Value.(string)
		k, _ := args[1].Value.(string)
		v, _ := args[2].Value.(string)
		if c.state.primary[scope] == nil {
			c.state.primary[scope] = map[string]string{}
		}
		c.state.primary[scope][k] = v
	}
	return driver.RowsAffected(1), nil
}

type p0E2EMySQLRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *p0E2EMySQLRows) Columns() []string { return r.cols }
func (r *p0E2EMySQLRows) Close() error      { return nil }
func (r *p0E2EMySQLRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func (c *p0E2EMySQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)

	store := c.state.replica
	if strings.Contains(query, "`dnf_write_primary`") {
		store = c.state.primary
	}
	scope := ""
	if len(args) > 0 {
		scope, _ = args[0].Value.(string)
	}
	vals, exists := store[scope]
	switch {
	case strings.Contains(query, "SHA2("):
		raw := vals["state"]
		if !exists || raw == "" {
			return &p0E2EMySQLRows{cols: []string{"digest"}}, nil
		}
		sum := sha256.Sum256([]byte(raw))
		return &p0E2EMySQLRows{
			cols: []string{"digest"},
			rows: [][]driver.Value{{hex.EncodeToString(sum[:])}},
		}, nil
	case strings.Contains(query, ".`dnf_settings`"):
		if !exists {
			return &p0E2EMySQLRows{cols: []string{"scope", "updated_at"}}, nil
		}
		return &p0E2EMySQLRows{
			cols: []string{"scope", "updated_at"},
			rows: [][]driver.Value{{scope, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}},
		}, nil
	case strings.Contains(query, ".`dnf_setting_values`"):
		rows := make([][]driver.Value, 0, len(vals))
		for k, v := range vals {
			rows = append(rows, []driver.Value{k, v})
		}
		return &p0E2EMySQLRows{cols: []string{"entry_key", "entry_value"}, rows: rows}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

var p0E2EMySQLDriverSeq atomic.Uint64

func openP0E2EMySQLGroup(t *testing.T, state *p0E2EMySQLState, now time.Time) repository.Group {
	t.Helper()
	driverName := fmt.Sprintf("p0_e2e_mysql_archive_%d_%d", time.Now().UnixNano(), p0E2EMySQLDriverSeq.Add(1))
	sql.Register(driverName, &p0E2EMySQLDriver{state: state})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	group, err := mysql.NewMySQLGroupFromDB(db, mysql.MySQLGroupOptions{
		DatabasePlan: repository.DatabasePlan{
			ReadDatabases:  []string{"dnf_read_replica"},
			WriteDatabases: []string{"dnf_write_primary"},
		},
		TablePrefix: "dnf",
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return group
}

// TestP0MySQLArchiveExpiredHistoryEndToEndConflictUnderReplicaLag 将 MySQL 读写分离路由夹具
// 与 guild.ArchiveExpiredHistory 真实业务载荷比对串联起来：
// 读库无任何归档记录、写库已有同键不同载荷的 receipt 或 hongbao 归档时，必须从写库 FOR UPDATE
// 读到既有归档并按业务规则报 ErrInvalidRegistry / ErrHongbaoState，且零覆盖、事务完整回滚。
func TestP0MySQLArchiveExpiredHistoryEndToEndConflictUnderReplicaLag(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	owner := repository.GuildOwner{AccountID: "a1", CharacterID: "1"}

	t.Run("receipt_conflict_detected_from_primary_without_overwrite", func(t *testing.T) {
		oldAt := now.Add(-48 * time.Hour)
		reqID := fmt.Sprintf("%064x", 0xabc)
		reg := guild.NewRegistry()
		if _, err := reg.Create(repository.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长"}, "归档公会", oldAt); err != nil {
			t.Fatal(err)
		}
		reg.ContractClaims = map[string]guild.ContractClaimReceipt{
			reqID: {
				Owner:   owner,
				GuildID: 1,
				ItemID:  490001385,
				Count:   1,
				At:      oldAt,
				DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
				Slots:   []int16{88},
			},
		}
		regRaw, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		conflictReceipt := guild.ArchivedReceipt{
			Version:   1,
			Kind:      guild.ArchivedReceiptKindContractClaim,
			RequestID: reqID,
			ContractClaim: &guild.ContractClaimReceipt{
				Owner:   owner,
				GuildID: 1,
				ItemID:  490001385,
				Count:   1,
				At:      oldAt,
				DayEnd:  itemexpiration.NextDailyDeleteAt(oldAt),
				Slots:   []int16{77}, // 与主表中的 88 冲突
			},
		}
		conflictRaw, err := json.Marshal(conflictReceipt)
		if err != nil {
			t.Fatal(err)
		}
		receiptScope := repository.GuildReceiptArchiveScope(reqID)
		state := &p0E2EMySQLState{
			primary: map[string]map[string]string{
				repository.GuildRegistryScope: {"state": string(regRaw)},
				receiptScope:                  {"receipt": string(conflictRaw)},
			},
			// 读库为空模拟复制延迟。
			replica: map[string]map[string]string{},
		}
		group := openP0E2EMySQLGroup(t, state, now)
		archiver := group.Guild.(repository.GuildArchiveUnitOfWork)

		if _, err := guild.ArchiveExpiredHistory(context.Background(), archiver, now); !errors.Is(err, guild.ErrInvalidRegistry) {
			t.Fatalf("expected guild.ErrInvalidRegistry on primary receipt conflict, got %v", err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.valueWrites) != 0 {
			t.Fatalf("conflicting receipt archive must not execute any dnf_setting_values writes, got %v", state.valueWrites)
		}
		if state.primary[receiptScope]["receipt"] != string(conflictRaw) {
			t.Fatal("primary receipt archive was modified")
		}
		if state.primary[repository.GuildRegistryScope]["state"] != string(regRaw) {
			t.Fatal("primary guild registry was modified")
		}
		sawPrimaryForUpdate := false
		for _, q := range state.queries {
			if strings.Contains(q, "`dnf_read_replica`") {
				t.Fatalf("ArchiveExpiredHistory unexpectedly hit read replica: %s", q)
			}
			if strings.Contains(q, "`dnf_write_primary`.`dnf_setting_values`") && strings.Contains(q, "FOR UPDATE") {
				sawPrimaryForUpdate = true
			}
		}
		if !sawPrimaryForUpdate {
			t.Fatalf("expected primary FOR UPDATE query on dnf_setting_values, queries=%v", state.queries)
		}
	})

	t.Run("hongbao_conflict_detected_from_primary_without_overwrite", func(t *testing.T) {
		createdAt := now.Add(-96 * time.Hour)
		settledAt := createdAt.Add(24 * time.Hour)
		reg := guild.NewRegistry()
		if _, err := reg.Create(repository.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长"}, "归档公会", createdAt.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		product := guild.HongbaoProduct{
			ItemID:           490003266,
			CommodityID:      103941,
			Price:            5000,
			ContributionRate: 80,
		}
		hb := guild.HongbaoRecord{
			ID:         1,
			GuildID:    1,
			Sender:     owner,
			SenderName: "会长",
			JoinedAt:   createdAt.Add(-24 * time.Hour),
			Product:    product,
			CreatedAt:  createdAt,
			ExpiresAt:  createdAt.Add(24 * time.Hour),
			Shares:     []uint32{1000, 1000, 1000, 1000, 1000},
			Settlement: &guild.HongbaoSettlementResult{
				Sender:          owner,
				RecordID:        1,
				RefundTokenCera: 5000,
				MailIDs:         []string{"m1"},
			},
			SettledAt: settledAt,
		}
		reg.Hongbao = &guild.HongbaoState{
			NextID:  2,
			Records: map[uint32]guild.HongbaoRecord{1: hb},
		}
		regRaw, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		conflictHB := hb
		conflictHB.SenderName = "写库既有不同归档"
		conflictRaw, err := json.Marshal(struct {
			Version int                 `json:"version"`
			Record  guild.HongbaoRecord `json:"record"`
		}{Version: 1, Record: conflictHB})
		if err != nil {
			t.Fatal(err)
		}
		hbScope := repository.GuildHongbaoArchiveScope(1)
		beforePrimary := map[string]map[string]string{
			repository.GuildRegistryScope: {"state": string(regRaw)},
			hbScope:                       {"hongbao": string(conflictRaw)},
		}
		state := &p0E2EMySQLState{
			primary: map[string]map[string]string{
				repository.GuildRegistryScope: {"state": string(regRaw)},
				hbScope:                       {"hongbao": string(conflictRaw)},
			},
			replica: map[string]map[string]string{},
		}
		group := openP0E2EMySQLGroup(t, state, now)
		archiver := group.Guild.(repository.GuildArchiveUnitOfWork)

		if _, err := guild.ArchiveExpiredHistory(context.Background(), archiver, now); !errors.Is(err, guild.ErrHongbaoState) {
			t.Fatalf("expected guild.ErrHongbaoState on primary hongbao conflict, got %v", err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.valueWrites) != 0 {
			t.Fatalf("conflicting hongbao archive must not execute any dnf_setting_values writes, got %v", state.valueWrites)
		}
		if !reflect.DeepEqual(state.primary, beforePrimary) {
			t.Fatal("primary hongbao archive or guild registry was modified")
		}
		for _, q := range state.queries {
			if strings.Contains(q, "`dnf_read_replica`") {
				t.Fatalf("ArchiveExpiredHistory unexpectedly hit read replica: %s", q)
			}
		}
	})
}
