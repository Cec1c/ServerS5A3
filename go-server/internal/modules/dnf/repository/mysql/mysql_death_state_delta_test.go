package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 独立关系行夹具经database/sql运行生产事务、锁读、SQL与回滚；不接触玩家库。
// 不改共享行模型或全局表定义，避免影响其他并行回归。
type deathDeltaSQL struct {
	rows                    map[string]map[string]map[string]any
	before                  map[string]map[string]map[string]any
	calls                   []fakeSQLCall
	writes, failAt, commits int
	inTx                    bool
}
type deathDeltaConnector struct{ s *deathDeltaSQL }
type deathDeltaDriver struct{ s *deathDeltaSQL }
type deathDeltaConn struct{ s *deathDeltaSQL }
type deathDeltaTx struct{ s *deathDeltaSQL }

func (c deathDeltaConnector) Connect(context.Context) (driver.Conn, error) {
	return deathDeltaConn{c.s}, nil
}
func (c deathDeltaConnector) Driver() driver.Driver         { return deathDeltaDriver{c.s} }
func (d deathDeltaDriver) Open(string) (driver.Conn, error) { return deathDeltaConn{d.s}, nil }
func (c deathDeltaConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("不应预编译")
}
func (c deathDeltaConn) Close() error { return nil }
func (c deathDeltaConn) Begin() (driver.Tx, error) {
	c.s.before = deathDeltaClone(c.s.rows)
	c.s.inTx = true
	return deathDeltaTx{c.s}, nil
}
func (c deathDeltaConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (tx deathDeltaTx) Commit() error   { tx.s.commits++; tx.s.inTx = false; return nil }
func (tx deathDeltaTx) Rollback() error { tx.s.rows = tx.s.before; tx.s.inTx = false; return nil }
func deathDeltaClone(rows map[string]map[string]map[string]any) map[string]map[string]map[string]any {
	out := map[string]map[string]map[string]any{}
	for table, values := range rows {
		out[table] = map[string]map[string]any{}
		for key, row := range values {
			copy := map[string]any{}
			for col, value := range row {
				if raw, ok := value.([]byte); ok {
					value = append([]byte(nil), raw...)
				}
				copy[col] = value
			}
			out[table][key] = copy
		}
	}
	return out
}
func deathDeltaRowKey(row map[string]any) string {
	var values []string
	for _, key := range []string{"character_id", "account_id", "scope", "collection_name", "pet_key", "entry_key", "stat_key", "extra_key", "token"} {
		if value, ok := row[key]; ok {
			values = append(values, key+"="+rowModelValueText(value))
		}
	}
	return strings.Join(values, "|")
}
func deathDeltaPredicate(predicate string, args []any) (func(map[string]any) bool, error) {
	predicate = strings.ReplaceAll(predicate, "`", "")
	parts := strings.Split(predicate, " AND ")
	used := 0
	type condition struct {
		key    string
		value  any
		prefix string
		in     []any
	}
	var conditions []condition
	for _, part := range parts {
		if at := strings.Index(part, " IN ("); at > 0 && strings.HasSuffix(part, ")") {
			placeholders := strings.Split(part[at+5:len(part)-1], ",")
			if used+len(placeholders) > len(args) {
				return nil, fmt.Errorf("IN条件参数不足")
			}
			for _, placeholder := range placeholders {
				if strings.TrimSpace(placeholder) != "?" {
					return nil, fmt.Errorf("IN条件仅支持绑定参数")
				}
			}
			conditions = append(conditions, condition{key: part[:at], in: append([]any(nil), args[used:used+len(placeholders)]...)})
			used += len(placeholders)
			continue
		}
		if strings.HasPrefix(part, "LEFT(stat_key,") && strings.HasSuffix(part, ") <> ?") && used+2 <= len(args) {
			conditions = append(conditions, condition{prefix: fmt.Sprint(args[used+1])})
			used += 2
			continue
		}
		if !strings.HasSuffix(part, " = ?") || used >= len(args) {
			return nil, fmt.Errorf("未覆盖的SQL条件 %s", part)
		}
		conditions = append(conditions, condition{key: strings.TrimSuffix(part, " = ?"), value: args[used]})
		used++
	}
	if used != len(args) {
		return nil, fmt.Errorf("条件参数数目不符")
	}
	return func(row map[string]any) bool {
		for _, c := range conditions {
			if c.in != nil {
				matched := false
				for _, value := range c.in {
					matched = matched || rowModelSame(row[c.key], value)
				}
				if !matched {
					return false
				}
				continue
			}
			if c.prefix != "" {
				if strings.HasPrefix(fmt.Sprint(row["stat_key"]), c.prefix) {
					return false
				}
				continue
			}
			if !rowModelSame(row[c.key], c.value) {
				return false
			}
		}
		return true
	}, nil
}
func (c deathDeltaConn) ExecContext(_ context.Context, q string, named []driver.NamedValue) (driver.Result, error) {
	s := c.s
	args := cargoDeltaArgs(named)
	s.writes++
	s.calls = append(s.calls, fakeSQLCall{q, args})
	if s.failAt > 0 && s.writes == s.failAt {
		return nil, errors.New("注入写入失败")
	}
	if strings.HasPrefix(q, "INSERT INTO ") {
		rest := strings.TrimPrefix(q, "INSERT INTO ")
		open := strings.Index(rest, " (")
		if open < 0 {
			return nil, errors.New("写入缺少列清单")
		}
		table := rowModelTableName(rest[:open])
		rest = strings.TrimSpace(rest[open:])
		close := strings.Index(rest, ") VALUES ")
		cols := rowModelColumns(rest[1:close])
		width := len(cols)
		if width == 0 || len(args)%width != 0 {
			return nil, errors.New("写入列数不符")
		}
		if s.rows[table] == nil {
			s.rows[table] = map[string]map[string]any{}
		}
		for start := 0; start < len(args); start += width {
			row := map[string]any{}
			for i, col := range cols {
				row[col] = args[start+i]
			}
			key := deathDeltaRowKey(row)
			old := s.rows[table][key]
			if old != nil && !strings.Contains(q, " ON DUPLICATE KEY UPDATE ") {
				return nil, errors.New("主键重复")
			}
			if old != nil {
				// 永久升级事件/领取记录的旧SQL规则由夹具独立执行。
				if strings.Contains(q, "stat_value = IF(") {
					previous, _ := strconv.ParseInt(fmt.Sprint(old["stat_value"]), 10, 64)
					next, _ := strconv.ParseInt(fmt.Sprint(row["stat_value"]), 10, 64)
					if previous != 0 && (strings.HasPrefix(fmt.Sprint(row["stat_key"]), repository.LevelUpRewardStatPrefix) || previous < 0 || next >= 0) {
						row["stat_value"] = old["stat_value"]
					}
				}
				for col, value := range old {
					if _, exists := row[col]; !exists {
						row[col] = value
					}
				}
			}
			s.rows[table][key] = row
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "DELETE FROM ") {
		rest := strings.TrimPrefix(q, "DELETE FROM ")
		at := strings.Index(rest, " WHERE ")
		table := rowModelTableName(rest[:at])
		match, err := deathDeltaPredicate(rest[at+7:], args)
		if err != nil {
			return nil, err
		}
		for key, row := range s.rows[table] {
			if match(row) {
				delete(s.rows[table], key)
			}
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE ") {
		rest := strings.TrimPrefix(q, "UPDATE ")
		at := strings.Index(rest, " SET ")
		table := rowModelTableName(rest[:at])
		rest = rest[at+5:]
		where := strings.Index(rest, " WHERE ")
		set := strings.Split(rest[:where], ", ")
		match, err := deathDeltaPredicate(rest[where+7:], args[len(set):])
		if err != nil {
			return nil, err
		}
		for _, row := range s.rows[table] {
			if match(row) {
				for i, col := range set {
					if !strings.HasSuffix(col, " = ?") {
						return nil, errors.New("未覆盖的UPDATE")
					}
					row[strings.TrimSuffix(col, " = ?")] = args[i]
				}
			}
		}
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("未覆盖的SQL写入 %s", q)
}
func (c deathDeltaConn) QueryContext(_ context.Context, q string, named []driver.NamedValue) (driver.Rows, error) {
	args := cargoDeltaArgs(named)
	c.s.calls = append(c.s.calls, fakeSQLCall{q, args})
	parent := strings.HasPrefix(q, "SELECT account_id, state,") || strings.HasPrefix(q, "SELECT character_id, account_id, slot,") || strings.HasPrefix(q, "SELECT character_id, equipped_key,")
	if c.s.inTx && (strings.Contains(q, "replica") || parent && !strings.HasSuffix(q, " FOR UPDATE")) {
		return nil, fmt.Errorf("事务未锁定写库 %s", q)
	}
	q = strings.TrimSuffix(q, " FOR UPDATE")
	at := strings.Index(q, " FROM ")
	cols := rowModelColumns(q[7:at])
	table, predicate, order, err := rowModelSelectTail(q[at+6:])
	if err != nil {
		return nil, err
	}
	match, err := deathDeltaPredicate(predicate, args)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, row := range c.s.rows[table] {
		if match(row) {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, col := range order {
			if col == "token_order" {
				a, _ := strconv.Atoi(fmt.Sprint(rows[i][col]))
				b, _ := strconv.Atoi(fmt.Sprint(rows[j][col]))
				if a != b {
					return a < b
				}
				continue
			}
			a, b := rowModelValueText(rows[i][col]), rowModelValueText(rows[j][col])
			if a != b {
				return a < b
			}
		}
		return false
	})
	out := &pairSQLRows{columns: cols}
	for _, row := range rows {
		values := make([]driver.Value, len(cols))
		for i, col := range cols {
			values[i], err = driver.DefaultParameterConverter.ConvertValue(row[col])
			if err != nil {
				return nil, err
			}
		}
		out.rows = append(out.rows, values)
	}
	return out, nil
}
func newDeathDeltaSQL(t *testing.T) (*deathDeltaSQL, *sql.DB, mysqlRouter) {
	t.Helper()
	s := &deathDeltaSQL{rows: map[string]map[string]map[string]any{}}
	db := sql.OpenDB(deathDeltaConnector{s})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	router := mysqlRouter{db: SQLDBAdapter{db}, readDBs: []string{"replica"}, writeDBs: []string{"dnf_s1_w1"}, tablePrefix: "dnf", now: func() time.Time { return time.Unix(10000, 0).UTC() }}
	return s, db, router
}
func deathDeltaSnapshot(s *deathDeltaSQL) map[string][]string {
	return (&rowModel{tables: s.rows}).snapshot()
}
func seedDeathDeltaState(t *testing.T, router mysqlRouter) {
	t.Helper()
	g := newMySQLGroupFromRouter(router, false)
	ctx := context.Background()
	a := repository.AccountRecord{AccountID: "acct-77", HonorExp: 100, Metadata: map[string]string{"adventure_group_runtime": "初始"}}
	c := repository.CharacterRecord{CharacterID: "77", AccountID: a.AccountID, Level: 89, Name: "测试", Stats: map[string]int64{"exp": 100, repository.LevelUpEventKey(80): 1234}}
	for i := 0; i < 160; i++ {
		a.Metadata[fmt.Sprintf("账号无关项%d", i)] = "保留"
	}
	for i := 0; i < 80; i++ {
		c.Stats[fmt.Sprintf("角色无关项%d", i)] = int64(i)
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
}
func TestMySQLDeathSettlementStateDeltaMatchesFullSaveAndRollback(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"经验熟练度胶囊", "升级及永久事件", "删除普通键", "同事务二次保存", "无完整快照", "历史影子属性回退", "写入失败", "账号元数据失败", "错误角色"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots []map[string][]string
			var writes []int
			for _, optimized := range []bool{false, true} {
				s, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaState(t, router)
				if scenario == "历史影子属性回退" {
					table, _ := router.writeTable(mysqlCharacterStatsTable, "77")
					if _, err := router.db.ExecContext(ctx, "INSERT INTO "+table+" (character_id, stat_key, stat_value) VALUES (?, ?, ?)", "77", "exp", int64(100)); err != nil {
						t.Fatal(err)
					}
				}
				initial := deathDeltaSnapshot(s)
				s.calls = nil
				s.writes = 0
				s.commits = 0
				if scenario == "写入失败" {
					s.failAt = 2
				}
				if scenario == "账号元数据失败" {
					s.failAt = 5
				}
				err := (&mysqlCharacterSettlementUnitOfWork{router: router}).WithinCharacterSettlement(ctx, "77", func(tx repository.Group) error {
					ar := tx.Account.(*characterSettlementAccountRepository).accounts.(*mysqlAccountStore)
					cr := tx.Character.(*characterSettlementScopedCharacterRepository).base.(*mysqlCharStore)
					if !optimized {
						ar.trackMetadataChanges = false
						cr.trackStatChanges = false
					}
					a, ok, err := tx.Account.Load(ctx, "acct-77")
					if err != nil || !ok {
						return fmt.Errorf("账号读取 %w", err)
					}
					c, ok, err := tx.Character.Load(ctx, "77")
					if err != nil || !ok {
						return fmt.Errorf("角色读取 %w", err)
					}
					a.HonorExp += 38011
					a.Metadata["adventure_group_runtime"] = "累计38011"
					c.Stats["honor_expert_level"] = 73
					c.Stats["honor_expert_exp"] = 38011
					if scenario == "升级及永久事件" {
						c.Level = 90
						c.Stats[repository.LevelUpEventKey(90)] = 10000
						delete(c.Stats, repository.LevelUpEventKey(80))
					}
					if scenario == "删除普通键" {
						delete(c.Stats, "角色无关项1")
						delete(a.Metadata, "账号无关项1")
					}
					if scenario == "无完整快照" {
						ar.loadedSnapshot = nil
						cr.loadedSnapshot = nil
					}
					if scenario == "错误角色" {
						c.CharacterID = "其他角色"
					}
					if err := repository.SaveCharacterFields(ctx, tx.Character, c, repository.CharacterFieldBase, repository.CharacterFieldStats); err != nil {
						return err
					}
					if err := tx.Account.Save(ctx, a); err != nil {
						return err
					}
					if scenario == "同事务二次保存" {
						a.Metadata["adventure_group_runtime"] = "累计76022"
						return tx.Account.Save(ctx, a)
					}
					return nil
				})
				failed := scenario == "写入失败" || scenario == "账号元数据失败" || scenario == "错误角色"
				if failed {
					if (scenario == "写入失败" || scenario == "账号元数据失败") && s.writes != s.failAt {
						t.Fatal("没有到达写入失败注入点", err, s.writes)
					}
					if err == nil || s.commits != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) {
						t.Fatal("错误出口未完整回滚", err)
					}
				} else if err != nil || s.commits != 1 {
					t.Fatal("结算失败", err, s.commits)
				}
				snapshots = append(snapshots, deathDeltaSnapshot(s))
				writes = append(writes, s.writes)
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("账号、角色或永久事件关系行不等价")
			}
			if scenario == "经验熟练度胶囊" && !(writes[1] <= 8 && writes[0] > 240) {
				t.Fatal("仍重写完整状态", writes)
			}
			t.Logf("%s：写语句%d→%d，最终关系行一致", scenario, writes[0], writes[1])
		})
	}
}

func seedDeathDeltaPet(t *testing.T, router mysqlRouter) {
	t.Helper()
	g := newMySQLGroupFromRouter(router, false)
	entry := repository.PetEntry{PetKey: "37", CreatureKey: 37, ItemID: 63000, SourceListType: 3, SourceSlotIndex: 26, Level: 1, Exp: 1, Satiety: 100, SatietyMicros: 100000000,
		Name: "测试宠物", RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"原始字段": "保留"}, AppliedClearTokens: map[string]bool{}}
	for i := 0; i < 128; i++ {
		key := fmt.Sprintf("清房%03d", i)
		entry.AppliedClearTokenOrder = append(entry.AppliedClearTokenOrder, key)
		entry.AppliedClearTokens[key] = true
	}
	record := repository.PetRecord{CharacterID: "77", EquippedKey: "37", Entries: map[string]repository.PetEntry{"37": entry, "其他宠物": {PetKey: "其他宠物", CreatureKey: 99, ItemID: 63001, Extra: map[string]string{"保留": "其他实例"}}},
		Artifacts: map[string]repository.ItemStack{"0": {ItemID: 70000, Count: 1, Extra: map[string]string{"保留": "神器实例"}}}}
	if err := g.Pet.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}
func TestMySQLPetRoomClearDeltaMatchesFullSaveAndRollback(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "200")
	ctx := context.Background()
	for _, scenario := range []string{"只结算饱食度", "清房及128回执窗口", "宠物实例变化回退", "神器变化回退", "无完整快照", "同事务二次保存", "写入失败", "回执批量写失败"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots []map[string][]string
			var writes []int
			for _, optimized := range []bool{false, true} {
				s, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaPet(t, router)
				initial := deathDeltaSnapshot(s)
				s.calls = nil
				s.writes = 0
				s.commits = 0
				if scenario == "写入失败" {
					s.failAt = 3
				}
				if scenario == "回执批量写失败" {
					s.failAt = 4
				}
				err := (&mysqlCharacterPetUnitOfWork{router: router}).WithinCharacterPets(ctx, "77", func(_ repository.InventoryRepository, _ repository.EquipmentRepository, pets repository.PetRepository) error {
					store := pets.(*characterPetScopedPetRepository).base.(*mysqlPetStore)
					if !optimized {
						store.trackGrowthChanges = false
					}
					p, ok, err := pets.Load(ctx, "77")
					if err != nil || !ok {
						return fmt.Errorf("宠物读取 %w", err)
					}
					entry := p.Entries["37"]
					entry.Satiety--
					entry.SatietyMicros -= 1000000
					if scenario != "只结算饱食度" {
						entry.Exp++
						delete(entry.AppliedClearTokens, entry.AppliedClearTokenOrder[0])
						entry.AppliedClearTokenOrder = append(entry.AppliedClearTokenOrder[1:], "本轮新回执")
						entry.AppliedClearTokens["本轮新回执"] = true
					}
					if scenario == "宠物实例变化回退" {
						entry.RawEntry[0] ^= 4
						entry.ItemID++
					}
					p.Entries["37"] = entry
					if scenario == "神器变化回退" {
						item := p.Artifacts["0"]
						item.Extra["保留"] = "新神器实例"
						p.Artifacts["0"] = item
					}
					if scenario == "无完整快照" {
						store.loadedSnapshot = nil
					}
					if err := repository.SavePetFields(ctx, pets, p, repository.PetFieldEntries); err != nil {
						return err
					}
					if scenario == "同事务二次保存" {
						entry.Exp++
						p.Entries["37"] = entry
						return repository.SavePetFields(ctx, pets, p, repository.PetFieldEntries)
					}
					return nil
				})
				if scenario == "写入失败" || scenario == "回执批量写失败" {
					if s.writes != s.failAt {
						t.Fatal("没有到达宠物写入失败注入点", err, s.writes)
					}
					if err == nil || s.commits != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) {
						t.Fatal("清房失败没有回滚全部关系行", err)
					}
				} else if err != nil || s.commits != 1 {
					t.Fatal("宠物保存失败", err, s.commits)
				}
				snapshots = append(snapshots, deathDeltaSnapshot(s))
				writes = append(writes, s.writes)
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("宠物、回执顺序、实例或神器关系行不等价")
			}
			if scenario == "只结算饱食度" && writes[1] != 2 {
				t.Fatal("饱食度仍重写回执或实例", writes)
			}
			if scenario == "清房及128回执窗口" && !(writes[1] == 4 && writes[0] > 130) {
				t.Fatal("历史回执仍逐行重写", writes)
			}
			t.Logf("%s：写语句%d→%d，最终关系行一致", scenario, writes[0], writes[1])
		})
	}
}

func TestMySQLDeathStateDeltaInvalidatesFailedAndPartialSnapshots(t *testing.T) {
	s, _, router := newDeathDeltaSQL(t)
	seedDeathDeltaState(t, router)
	ctx := context.Background()
	err := (&mysqlCharacterSettlementUnitOfWork{router: router}).WithinCharacterSettlement(ctx, "77", func(tx repository.Group) error {
		store := tx.Character.(*characterSettlementScopedCharacterRepository).base.(*mysqlCharStore)
		record, _, err := tx.Character.Load(ctx, "77")
		if err != nil {
			return err
		}
		if store.loadedSnapshot == nil {
			t.Fatal("事务未登记锁读快照")
		}
		record.Stats["角色无关项1"] = 9999
		if store.loadedSnapshot.Stats["角色无关项1"] == 9999 {
			t.Fatal("快照与调用方共享map")
		}
		if _, found, err := store.Load(ctx, "不存在的角色"); err != nil || found || store.loadedSnapshot != nil {
			t.Fatal("失败读取保留过期快照", found, err)
		}
		if _, _, err := tx.Character.Load(ctx, "77"); err != nil {
			return err
		}
		if err := repository.SaveCharacterFields(ctx, tx.Character, record, repository.CharacterFieldBase); err != nil {
			return err
		}
		if store.loadedSnapshot != nil {
			t.Fatal("局部保存后仍复用旧完整快照")
		}
		return repository.SaveCharacterFields(ctx, tx.Character, record, repository.CharacterFieldStats)
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.commits < 1 {
		t.Fatal("事务未提交")
	}
}
