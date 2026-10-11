// 本文件是 O-A（批量 INSERT）的语句流证据。
//
// 判据有两条，缺一不可：
//  1. 默认档（未配置 / =1）产出的语句流必须与改动前**逐字节相同**（同文本、同参数、
//     同顺序）—— 所以默认部署等于什么都没改；
//  2. 批量档只减少往返：行集合与行顺序必须与默认档完全一致。
//
// 反过来说：任何"默认档也批量了"或"批量档改了行顺序"的改动都会让本文件失败。
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// TestMySQLInventoryDefaultBatchStreamIsByteIdenticalToToday 钉住默认档的语句流。
// 期望值是**手写**的（不经过生产构造函数），因此它同时是"今天长什么样"的存档。
func TestMySQLInventoryDefaultBatchStreamIsByteIdenticalToToday(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "")
	ctx := context.Background()
	db := &fakeSQLDB{}
	group := newTestMySQLGroup(t, db)
	record := repository.InventoryRecord{
		CharacterID: "char-1",
		Slots: map[string]repository.ItemStack{
			"0:10": {ItemID: 1001, Count: 1, Bind: true, RawEntry: []byte{1, 2}},
			"0:9": {
				ItemID: 1002, Count: 3,
				Extra: map[string]string{"b_key": "second", "a_key": "first"},
			},
		},
	}
	if err := repository.SaveInventoryFields(ctx, group.Inventory, record, repository.InventoryFieldSlots); err != nil {
		t.Fatalf("SaveInventoryFields() error = %v", err)
	}

	items := "INSERT INTO `dnf_s1_w1`.`dnf_inventory_items` (`character_id`, collection_name, entry_key, item_id, item_count, bind_flag, expire_at, raw_entry) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	extras := "INSERT INTO `dnf_s1_w1`.`dnf_inventory_item_extra` (`character_id`, collection_name, entry_key, extra_key, extra_value) VALUES (?, ?, ?, ?, ?)"
	want := []fakeSQLCall{
		{Query: "INSERT INTO `dnf_s1_w1`.`dnf_inventories` (`character_id`, `updated_at`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `updated_at` = VALUES(`updated_at`)", Args: []any{"char-1"}},
		{Query: "DELETE FROM `dnf_s1_w1`.`dnf_inventory_item_extra` WHERE `character_id` = ? AND collection_name = ?", Args: []any{"char-1", "slots"}},
		{Query: "DELETE FROM `dnf_s1_w1`.`dnf_inventory_items` WHERE `character_id` = ? AND collection_name = ?", Args: []any{"char-1", "slots"}},
		// 按 entry_key 排序：先 "0:10"，再 "0:9"（字符串序）。
		{Query: items, Args: []any{"char-1", "slots", "0:10", int64(1001), int64(1), true, nil, []byte{1, 2}}},
		{Query: items, Args: []any{"char-1", "slots", "0:9", int64(1002), int64(3), false, nil, []byte(nil)}},
		// 同一行的 extra 紧跟该行，extra_key 排序：a_key 在 b_key 之前。
		{Query: extras, Args: []any{"char-1", "slots", "0:9", "a_key", "first"}},
		{Query: extras, Args: []any{"char-1", "slots", "0:9", "b_key", "second"}},
	}
	got := db.execs
	// 父表 upsert 的第二个参数是 now()（测试桩里固定），比对时归一化。
	got[0].Args = append(got[0].Args, nil)
	got[0].Args = got[0].Args[:1]
	if len(got) != len(want) {
		t.Fatalf("statement count = %d, want %d:\n%s", len(got), len(want), strings.Join(execQueries(db), "\n"))
	}
	for idx := range want {
		if got[idx].Query != want[idx].Query {
			t.Fatalf("statement %d query =\n%q\nwant\n%q", idx, got[idx].Query, want[idx].Query)
		}
		if len(idxNormalizeArgs(got[idx].Args)) != len(want[idx].Args) {
			t.Fatalf("statement %d args = %v (%d), want %v (%d)", idx, got[idx].Args, len(got[idx].Args), want[idx].Args, len(want[idx].Args))
		}
		if !reflect.DeepEqual(idxNormalizeArgs(got[idx].Args), want[idx].Args) {
			t.Fatalf("statement %d args = %#v, want %#v", idx, got[idx].Args, want[idx].Args)
		}
	}
}

// idxNormalizeArgs 去掉父表 upsert 的时间参数（它是 now()，与本次判据无关）。
func idxNormalizeArgs(args []any) []any {
	if len(args) == 2 {
		if _, isTime := args[1].(interface{ Unix() int64 }); isTime {
			return args[:1]
		}
	}
	return args
}

// TestMySQLInventoryDefaultBatchStreamKeepsOneRowPerStatement 用更大的容器钉住
// "默认档每条语句只写一行"（批量档下这条断言必须失败）。
func TestMySQLInventoryDefaultBatchStreamKeepsOneRowPerStatement(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "")
	ctx := context.Background()
	db := &fakeSQLDB{}
	group := newTestMySQLGroup(t, db)
	slots := map[string]repository.ItemStack{}
	for slot := 0; slot < 20; slot++ {
		slots[fmt.Sprintf("0:%d", slot)] = repository.ItemStack{
			ItemID: int64(1000 + slot),
			Count:  1,
			Extra:  map[string]string{"pvf_path": "x", "item_kind": "equipment"},
		}
	}
	if err := repository.SaveInventoryFields(
		ctx, group.Inventory, repository.InventoryRecord{CharacterID: "char-1", Slots: slots}, repository.InventoryFieldSlots,
	); err != nil {
		t.Fatalf("SaveInventoryFields() error = %v", err)
	}
	wantInserts := len(slots) * 3 // 1 行 item + 2 行 extra
	inserts := 0
	for _, call := range db.execs {
		if !strings.HasPrefix(call.Query, "INSERT INTO ") || strings.Contains(call.Query, "dnf_inventories") {
			continue
		}
		inserts++
		if strings.Contains(call.Query, "),(") {
			t.Fatalf("default batch statement has multiple value groups: %s", call.Query)
		}
		if got := strings.Count(call.Query, "VALUES ("); got != 1 {
			t.Fatalf("default batch statement has %d value groups: %s", got, call.Query)
		}
	}
	if inserts != wantInserts {
		t.Fatalf("insert statements = %d, want %d (one per row)", inserts, wantInserts)
	}
}

// TestMySQLInventoryBatchedInsertLandsSameRowsInSameOrder 钉住批量档的行集合与行顺序
// 与默认档逐一相同（只是语句更少）。
func TestMySQLInventoryBatchedInsertLandsSameRowsInSameOrder(t *testing.T) {
	record := repository.InventoryRecord{CharacterID: "char-1", Slots: map[string]repository.ItemStack{}}
	for slot := 0; slot < 12; slot++ {
		record.Slots[fmt.Sprintf("0:%d", slot)] = repository.ItemStack{
			ItemID:   int64(1000 + slot),
			Count:    2,
			Bind:     slot%2 == 0,
			RawEntry: []byte{byte(slot), 0xFF},
			Extra:    map[string]string{"pvf_path": "x", "item_kind": "equipment"},
		}
	}

	single := runInventorySave(t, record, "")
	batched := runInventorySave(t, record, "200")

	// 表内行顺序必须一致（跨表交错顺序无语义，见 rowInsertBatcher 的不变量）。
	if got, want := rowSequence(batched), rowSequence(single); !reflect.DeepEqual(got, want) {
		t.Fatalf("batched rows differ from single-row rows\n single=%v\nbatched=%v", want, got)
	}
	if len(batched) >= len(single) {
		t.Fatalf("batched statements = %d, single-row statements = %d; want fewer", len(batched), len(single))
	}
	// 12 行 item + 24 行 extra + 2 条 DELETE + 1 条父表 upsert = 39；批量 200 时
	// item 行一条、extra 行一条 ⇒ 1 + 2 + 1 + 1 = 5 条。
	if len(batched) != 5 {
		t.Fatalf("batched statements = %d, want 5:\n%s", len(batched), strings.Join(callQueries(batched), "\n"))
	}
	if len(single) != 39 {
		t.Fatalf("single-row statements = %d, want 39", len(single))
	}
}

func runInventorySave(t *testing.T, record repository.InventoryRecord, batch string) []fakeSQLCall {
	t.Helper()
	t.Setenv(currentMysqlInsertBatchEnv, batch)
	db := &fakeSQLDB{}
	group := newTestMySQLGroup(t, db)
	if err := repository.SaveInventoryFields(
		context.Background(), group.Inventory, record, repository.InventoryFieldSlots,
	); err != nil {
		t.Fatalf("SaveInventoryFields() error = %v", err)
	}
	return db.execs
}

// rowSequence 把语句流压平成"表 -> 行参数序列"（跳过父表 upsert 与 DELETE）。
// 按表分开比较：跨表交错顺序不参与任何语义，表内行顺序才是判据。
func rowSequence(calls []fakeSQLCall) map[string][]string {
	out := map[string][]string{}
	for _, call := range calls {
		if !strings.HasPrefix(call.Query, "INSERT INTO ") || strings.Contains(call.Query, "dnf_inventories") {
			continue
		}
		table := insertStatementTable(call.Query)
		width := strings.Count(call.Query, "?") / valueGroupCount(call.Query)
		if width == 0 {
			continue
		}
		for start := 0; start+width <= len(call.Args); start += width {
			parts := make([]string, 0, width)
			for _, arg := range call.Args[start : start+width] {
				parts = append(parts, fmt.Sprintf("%v", arg))
			}
			out[table] = append(out[table], strings.Join(parts, "|"))
		}
	}
	return out
}

func valueGroupCount(query string) int {
	idx := strings.Index(query, ") VALUES ")
	if idx < 0 {
		return 1
	}
	return strings.Count(query[idx+len(") VALUES "):], "(")
}

func insertStatementTable(query string) string {
	rest := strings.TrimPrefix(query, "INSERT INTO ")
	if idx := strings.Index(rest, " ("); idx >= 0 {
		rest = rest[:idx]
	}
	return rowModelTableName(rest)
}

func callQueries(calls []fakeSQLCall) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.Query)
	}
	return out
}

// TestMySQLInsertBatchClampAndPlaceholderBudget 钉住批量上限：
// 行数硬钳 1000、占位符预算按列数折算、非法取值回落 1 并记事件。
func TestMySQLInsertBatchClampAndPlaceholderBudget(t *testing.T) {
	events := captureWriteEvents(t)
	t.Setenv(currentMysqlInsertBatchEnv, "5000")
	if got := configuredMysqlInsertBatch(); got != mysqlInsertBatchMaxRows {
		t.Fatalf("configuredMysqlInsertBatch(5000) = %d, want %d", got, mysqlInsertBatchMaxRows)
	}
	if !hasWriteEvent(*events, "insert_batch_clamped") {
		t.Fatalf("clamp was not reported: %+v", *events)
	}

	t.Setenv(currentMysqlInsertBatchEnv, "abc")
	if got := configuredMysqlInsertBatch(); got != mysqlInsertBatchDefault {
		t.Fatalf("configuredMysqlInsertBatch(abc) = %d, want %d", got, mysqlInsertBatchDefault)
	}
	if !hasWriteEvent(*events, "insert_batch_invalid") {
		t.Fatalf("invalid literal was not reported: %+v", *events)
	}

	t.Setenv(currentMysqlInsertBatchEnv, "200")
	if got := configuredMysqlInsertBatch(); got != 200 {
		t.Fatalf("configuredMysqlInsertBatch(200) = %d, want 200", got)
	}
	if got := mysqlInsertBatchRowsFor(8); got != 200 {
		t.Fatalf("mysqlInsertBatchRowsFor(8) = %d, want 200", got)
	}
	// 占位符预算：30000/7 = 4285 > 1000 ⇒ 仍受行数硬钳。
	t.Setenv(currentMysqlInsertBatchEnv, "1000")
	if got := mysqlInsertBatchRowsFor(7); got != 1000 {
		t.Fatalf("mysqlInsertBatchRowsFor(7 @1000) = %d, want 1000", got)
	}
	// 列数极宽时占位符预算才是约束：30000/40000 = 0 ⇒ 至少 1 行。
	if got := mysqlInsertBatchRowsFor(40000); got != 1 {
		t.Fatalf("mysqlInsertBatchRowsFor(40000) = %d, want 1", got)
	}
}

// TestMySQLInventoryBatchedInsertSplitsAtTheHardClamp 钉住 1000 行硬钳真的生效：
// 1200 行必须分成 1000 + 200 两条多行 INSERT。
func TestMySQLInventoryBatchedInsertSplitsAtTheHardClamp(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "5000")
	record := repository.InventoryRecord{CharacterID: "char-1", Slots: map[string]repository.ItemStack{}}
	for slot := 0; slot < 1200; slot++ {
		record.Slots["0:"+strconv.Itoa(slot)] = repository.ItemStack{ItemID: int64(slot), Count: 1}
	}
	calls := runInventorySave(t, record, "5000")
	inserts := []fakeSQLCall{}
	for _, call := range calls {
		if strings.Contains(call.Query, "dnf_inventory_items") && strings.HasPrefix(call.Query, "INSERT INTO ") {
			inserts = append(inserts, call)
		}
	}
	if len(inserts) != 2 {
		t.Fatalf("batched item inserts = %d, want 2 (1000 + 200)", len(inserts))
	}
	firstRows := strings.Count(inserts[0].Query, "?") / 8
	secondRows := strings.Count(inserts[1].Query, "?") / 8
	if firstRows != 1000 || secondRows != 200 {
		t.Fatalf("row split = %d + %d, want 1000 + 200", firstRows, secondRows)
	}
}

// TestMySQLInsertBatcherFallsBackToRowByRowOnPacketTooLarge 钉住"单条语句过大"的兜底：
// 多行 INSERT 报 packet too large 时，本批必须逐行重放，行一行都不能少。
func TestMySQLInsertBatcherFallsBackToRowByRowOnPacketTooLarge(t *testing.T) {
	events := captureWriteEvents(t)
	t.Setenv(currentMysqlInsertBatchEnv, "4")
	database := &rejectingMultiRowDB{fakeSQLDB: &fakeSQLDB{}}
	shape := insertShape{
		table:        "dnf_inventory_items",
		prefix:       "INSERT INTO t (a, b) VALUES ",
		placeholders: "(?, ?)",
		columns:      2,
	}
	batcher := newRowInsertBatcher(context.Background(), database, "char-1", shape)
	for row := 0; row < 6; row++ {
		if err := batcher.add("key", row); err != nil {
			t.Fatalf("add(%d) error = %v", row, err)
		}
	}
	if err := batcher.flush(); err != nil {
		t.Fatalf("flush() error = %v", err)
	}
	if !hasWriteEvent(*events, "insert_batch_packet_too_large") {
		t.Fatalf("packet-too-large fallback was not reported: %+v", *events)
	}
	// 6 行 / 每批 4 行 ⇒ 第一条多行语句被拒，随后 4 行逐行重放；第 5、6 行又攒成
	// 一条多行语句并被拒，再逐行重放 2 行 ⇒ 共 1 + 4 + 1 + 2 = 8 条。
	if got := len(database.execs); got != 8 {
		t.Fatalf("executed statements = %d, want 8: %v", got, callQueries(database.execs))
	}
	replays := 0
	for _, call := range database.execs {
		if strings.Contains(call.Query, "),(") {
			continue
		}
		replays++
	}
	if replays != 6 {
		t.Fatalf("single-row statements = %d, want 6 (one per row)", replays)
	}
}

// rejectingMultiRowDB 让多行 INSERT 报 1153，单行 INSERT 正常通过。
type rejectingMultiRowDB struct {
	*fakeSQLDB
}

func (d *rejectingMultiRowDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "),(") {
		d.execs = append(d.execs, fakeSQLCall{Query: query, Args: append([]any(nil), args...)})
		return nil, fmt.Errorf("Error 1153: Got a packet bigger than 'max_allowed_packet' bytes")
	}
	return d.fakeSQLDB.ExecContext(ctx, query, args...)
}
