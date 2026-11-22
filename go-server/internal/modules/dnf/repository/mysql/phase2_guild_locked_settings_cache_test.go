package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

type phase2SettingsCacheDriverState struct {
	mu            sync.Mutex
	queries       []string
	settingExists bool
	settingValues map[string]string
	updatedAt     time.Time

	onSettingsQuery func(query string)
	onSettingsExec  func(query string)
}

type phase2SettingsCacheDriver struct {
	state *phase2SettingsCacheDriverState
}

func (d *phase2SettingsCacheDriver) Open(string) (driver.Conn, error) {
	return &phase2SettingsCacheConn{state: d.state}, nil
}

type phase2SettingsCacheConn struct {
	state *phase2SettingsCacheDriverState
}

func (c *phase2SettingsCacheConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused")
}
func (c *phase2SettingsCacheConn) Close() error              { return nil }
func (c *phase2SettingsCacheConn) Begin() (driver.Tx, error) { return &guildSettingsTestTx{}, nil }
func (c *phase2SettingsCacheConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &guildSettingsTestTx{}, nil
}

func (c *phase2SettingsCacheConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	hook := c.state.onSettingsExec
	c.state.mu.Unlock()
	if hook != nil {
		hook(query)
	}

	c.state.mu.Lock()
	c.state.queries = append(c.state.queries, query)
	if strings.HasPrefix(query, "INSERT INTO `dnf_local`.`dnf_settings`") {
		c.state.settingExists = true
	} else if strings.HasPrefix(query, "DELETE FROM `dnf_local`.`dnf_setting_values`") {
		c.state.settingValues = map[string]string{}
	} else if strings.HasPrefix(query, "INSERT INTO `dnf_local`.`dnf_setting_values`") && len(args) == 3 {
		if c.state.settingValues == nil {
			c.state.settingValues = map[string]string{}
		}
		k, _ := args[1].Value.(string)
		v, _ := args[2].Value.(string)
		c.state.settingValues[k] = v
	}
	c.state.mu.Unlock()
	return driver.RowsAffected(1), nil
}

func (c *phase2SettingsCacheConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	hook := c.state.onSettingsQuery
	c.state.queries = append(c.state.queries, query)
	exists := c.state.settingExists
	values := make(map[string]string, len(c.state.settingValues))
	for k, v := range c.state.settingValues {
		values[k] = v
	}
	updatedAt := c.state.updatedAt
	c.state.mu.Unlock()
	// 先捕获查询快照再执行阻塞屏障，确保 Load-before-Save 测试真实拿到旧值并在 Save 之后返回。
	if hook != nil {
		hook(query)
	}

	switch {
	case strings.Contains(query, "FROM `dnf_local`.`dnf_settings`"):
		if !exists {
			return &pairSQLRows{columns: []string{"scope", "updated_at"}}, nil
		}
		return &pairSQLRows{
			columns: []string{"scope", "updated_at"},
			rows:    [][]driver.Value{{repository.GuildRegistryScope, updatedAt}},
		}, nil
	case strings.Contains(query, "FROM `dnf_local`.`dnf_setting_values`"):
		var rows [][]driver.Value
		for k, v := range values {
			rows = append(rows, []driver.Value{k, v})
		}
		return &pairSQLRows{
			columns: []string{"entry_key", "entry_value"},
			rows:    rows,
		}, nil
	case strings.Contains(query, "SELECT character_id, account_id, delete_flag FROM `dnf_local`.`dnf_characters`"):
		return &pairSQLRows{
			columns: []string{"character_id", "account_id", "delete_flag"},
			rows:    [][]driver.Value{{"42", "acc-1", int64(0)}},
		}, nil
	default:
		return &pairSQLRows{columns: []string{"v"}}, nil
	}
}

var (
	phase2SettingsDriverSeqMu sync.Mutex
	phase2SettingsDriverSeq   int
)

func openPhase2SettingsCacheTestDB(t *testing.T, state *phase2SettingsCacheDriverState) *sql.DB {
	t.Helper()
	phase2SettingsDriverSeqMu.Lock()
	phase2SettingsDriverSeq++
	name := fmt.Sprintf("phase2_guild_settings_cache_%d", phase2SettingsDriverSeq)
	phase2SettingsDriverSeqMu.Unlock()
	sql.Register(name, &phase2SettingsCacheDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPhase2WithinGuildReusesLockedSettingsReadAndInvalidatesOnSave(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	state := &phase2SettingsCacheDriverState{
		settingExists: true,
		settingValues: map[string]string{"state": `{"version":1,"next_id":2}`},
		updatedAt:     now,
	}
	rawDB := openPhase2SettingsCacheTestDB(t, state)
	uow := &mysqlGuildUnitOfWork{
		router: mysqlRouter{
			db:          SQLDBAdapter{DB: rawDB},
			readDBs:     []string{"dnf_local"},
			writeDBs:    []string{"dnf_local"},
			tablePrefix: "dnf",
			now:         func() time.Time { return now },
		},
	}

	err := uow.WithinGuildPresence(context.Background(), repository.GuildOwner{
		CharacterID: "42",
		AccountID:   "acc-1",
	}, func(tx repository.Group) error {
		// 第一次 Load 应命中事务内已加锁缓存，不再重复查 dnf_setting_values。
		rec1, found, loadErr := tx.Settings.Load(context.Background(), repository.GuildRegistryScope)
		if loadErr != nil || !found {
			t.Fatalf("first Load failed: found=%v err=%v", found, loadErr)
		}
		if rec1.Values["state"] != `{"version":1,"next_id":2}` {
			t.Fatalf("unexpected state: %q", rec1.Values["state"])
		}
		// 修改返回值不能污染缓存。
		rec1.Values["state"] = "mutated-local"

		rec2, found, loadErr := tx.Settings.Load(context.Background(), repository.GuildRegistryScope)
		if loadErr != nil || !found || rec2.Values["state"] != `{"version":1,"next_id":2}` {
			t.Fatalf("cached Load was mutated or missed: found=%v val=%q err=%v", found, rec2.Values["state"], loadErr)
		}

		// 取消的 ctx 即使命中缓存也必须返回错误。
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, cErr := tx.Settings.Load(cancelled, repository.GuildRegistryScope); !errors.Is(cErr, context.Canceled) {
			t.Fatalf("expected context.Canceled on cached Load, got %v", cErr)
		}

		// Save 后必须失效缓存，后续 Load 读取最新数据库值。
		rec2.Values["state"] = `{"version":1,"next_id":3}`
		if saveErr := tx.Settings.Save(context.Background(), rec2); saveErr != nil {
			t.Fatalf("Save failed: %v", saveErr)
		}
		rec3, found, loadErr := tx.Settings.Load(context.Background(), repository.GuildRegistryScope)
		if loadErr != nil || !found || rec3.Values["state"] != `{"version":1,"next_id":3}` {
			t.Fatalf("post-save Load did not observe updated value: found=%v val=%q err=%v", found, rec3.Values["state"], loadErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinGuildPresence failed: %v", err)
	}

	valuesSelects := 0
	for _, q := range state.queries {
		if strings.Contains(q, "SELECT entry_key, entry_value FROM `dnf_local`.`dnf_setting_values`") {
			valuesSelects++
		}
	}
	if valuesSelects != 2 {
		t.Fatalf("expected exactly 2 dnf_setting_values SELECTs (pre-lock + post-save), got %d; queries=%v", valuesSelects, state.queries)
	}
}

func TestPhase2NonLockingSettingsStoreNeverCachesReads(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	state := &phase2SettingsCacheDriverState{
		settingExists: true,
		settingValues: map[string]string{"state": "v1"},
		updatedAt:     now,
	}
	rawDB := openPhase2SettingsCacheTestDB(t, state)
	store := &mysqlSettingsStore{
		mysqlStoreBase: mysqlStoreBase{
			router: mysqlRouter{
				db:          SQLDBAdapter{DB: rawDB},
				readDBs:     []string{"dnf_local"},
				writeDBs:    []string{"dnf_local"},
				tablePrefix: "dnf",
				lockReads:   false,
				now:         func() time.Time { return now },
			},
		},
		cacheLockedReads: true,
	}
	for i := 0; i < 2; i++ {
		if _, found, err := store.Load(context.Background(), repository.GuildRegistryScope); err != nil || !found {
			t.Fatalf("Load %d failed: found=%v err=%v", i, found, err)
		}
	}
	valuesSelects := 0
	for _, q := range state.queries {
		if strings.Contains(q, "SELECT entry_key, entry_value FROM `dnf_local`.`dnf_setting_values`") {
			valuesSelects++
		}
	}
	if valuesSelects != 2 {
		t.Fatalf("non-locking store must query every time, got %d", valuesSelects)
	}
}

func TestPhase2LockedSettingsCacheInterleavingBarriers(t *testing.T) {
	t.Run("load_before_save_cannot_repopulate_stale_value", func(t *testing.T) {
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		loadEntered := make(chan struct{})
		releaseLoad := make(chan struct{})
		var loadOnce sync.Once

		state := &phase2SettingsCacheDriverState{
			settingExists: true,
			settingValues: map[string]string{"state": "v1"},
			updatedAt:     now,
			onSettingsQuery: func(query string) {
				if strings.Contains(query, "FROM `dnf_local`.`dnf_setting_values`") {
					loadOnce.Do(func() {
						close(loadEntered)
						<-releaseLoad
					})
				}
			},
		}
		rawDB := openPhase2SettingsCacheTestDB(t, state)
		rawDB.SetMaxOpenConns(4)
		store := &mysqlSettingsStore{
			mysqlStoreBase: mysqlStoreBase{
				router: mysqlRouter{
					db:          SQLDBAdapter{DB: rawDB},
					readDBs:     []string{"dnf_local"},
					writeDBs:    []string{"dnf_local"},
					tablePrefix: "dnf",
					lockReads:   true,
					now:         func() time.Time { return now },
				},
			},
			cacheLockedReads: true,
		}

		type loadResult struct {
			rec   repository.SettingsRecord
			found bool
			err   error
		}
		loadDone := make(chan loadResult, 1)
		go func() {
			rec, found, err := store.Load(context.Background(), repository.GuildRegistryScope)
			loadDone <- loadResult{rec: rec, found: found, err: err}
		}()
		<-loadEntered

		// 在旧 Load 尚未回填前完成 Save(v2)。
		if err := store.Save(context.Background(), repository.SettingsRecord{
			Scope:  repository.GuildRegistryScope,
			Values: map[string]string{"state": "v2"},
		}); err != nil {
			t.Fatalf("Save(v2) failed: %v", err)
		}
		// 释放已拿到旧值 v1 的在途 Load，确认其真实返回 v1 且没有污染缓存。
		close(releaseLoad)
		inFlight := <-loadDone
		if inFlight.err != nil || !inFlight.found || inFlight.rec.Values["state"] != "v1" {
			t.Fatalf("expected in-flight Load to observe pre-save v1, got found=%v val=%q err=%v", inFlight.found, inFlight.rec.Values["state"], inFlight.err)
		}

		// 后续 Load 绝不能读到被旧 Load 污染的缓存。
		rec, found, err := store.Load(context.Background(), repository.GuildRegistryScope)
		if err != nil || !found || rec.Values["state"] != "v2" {
			t.Fatalf("post-interleaving Load returned stale value: found=%v val=%q err=%v", found, rec.Values["state"], err)
		}
	})

	for _, initiallyExists := range []bool{true, false} {
		name := "load_during_save_positive_cache"
		if !initiallyExists {
			name = "load_during_save_negative_cache"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			saveEntered := make(chan struct{})
			releaseSave := make(chan struct{})
			var saveOnce sync.Once

			initValues := map[string]string{}
			if initiallyExists {
				initValues["state"] = "v1"
			}
			state := &phase2SettingsCacheDriverState{
				settingExists: initiallyExists,
				settingValues: initValues,
				updatedAt:     now,
				onSettingsExec: func(query string) {
					if strings.HasPrefix(query, "INSERT INTO `dnf_local`.`dnf_settings`") {
						saveOnce.Do(func() {
							close(saveEntered)
							<-releaseSave
						})
					}
				},
			}
			rawDB := openPhase2SettingsCacheTestDB(t, state)
			rawDB.SetMaxOpenConns(4)
			store := &mysqlSettingsStore{
				mysqlStoreBase: mysqlStoreBase{
					router: mysqlRouter{
						db:          SQLDBAdapter{DB: rawDB},
						readDBs:     []string{"dnf_local"},
						writeDBs:    []string{"dnf_local"},
						tablePrefix: "dnf",
						lockReads:   true,
						now:         func() time.Time { return now },
					},
				},
				cacheLockedReads: true,
			}

			saveDone := make(chan error, 1)
			go func() {
				saveDone <- store.Save(context.Background(), repository.SettingsRecord{
					Scope:  repository.GuildRegistryScope,
					Values: map[string]string{"state": "v2"},
				})
			}()
			<-saveEntered

			// Save 已进入但尚未写入数据库：并发 Load 读到旧值（或 not-found），不得写入缓存。
			midRec, midFound, midErr := store.Load(context.Background(), repository.GuildRegistryScope)
			if midErr != nil {
				t.Fatalf("mid-save Load failed: %v", midErr)
			}
			if initiallyExists && (!midFound || midRec.Values["state"] != "v1") {
				t.Fatalf("expected mid-save Load to see v1, got found=%v val=%q", midFound, midRec.Values["state"])
			}
			if !initiallyExists && midFound {
				t.Fatalf("expected mid-save Load to see not-found, got found=true val=%q", midRec.Values["state"])
			}

			close(releaseSave)
			if err := <-saveDone; err != nil {
				t.Fatalf("Save(v2) failed: %v", err)
			}

			// Save 完成后，后续 Load 必须观察到 v2，不得命中写入期间的旧值或负缓存。
			rec, found, err := store.Load(context.Background(), repository.GuildRegistryScope)
			if err != nil || !found || rec.Values["state"] != "v2" {
				t.Fatalf("post-save Load returned stale or negative cached result: found=%v val=%q err=%v", found, rec.Values["state"], err)
			}
		})
	}
}
