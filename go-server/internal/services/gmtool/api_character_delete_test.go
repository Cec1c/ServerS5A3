package gmtool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// C：删除角色（POST /api/characters/{id}/delete）
//
// 测试台直接用 api_mailbox_delete_test.go 的 gmtoolWriteHarness：里面已经有一个
// 角色（CharacterID "1" / AccountID "acc-1" / slot 0）与日志收集器。
//
// 默认档是**硬删除**（要 s.db），所以旧软删除用例统一用 newChardelSoftHarness
// 把开关置成 soft；硬删除用例自己注入假事务（chardelHardDB）。
// ---------------------------------------------------------------------------

// newChardelSoftHarness 打开软删除档（DNFBRIDGE_GM_CHARACTER_DELETE_MODE=soft）。
func newChardelSoftHarness(t *testing.T) *gmtoolWriteHarness {
	t.Helper()
	t.Setenv(chardelModeEnv, "soft")
	return newGmtoolWriteHarness(t, nil)
}

func (h *gmtoolWriteHarness) deleteCharacter(t *testing.T, panelID, confirm string) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/"+panelID+"/delete", map[string]string{"id": panelID},
		map[string]any{"confirmText": confirm}, h.server.handleCharacterDelete)
}

func (h *gmtoolWriteHarness) character(t *testing.T, characterID string) (dnfrepo.CharacterRecord, bool) {
	t.Helper()
	group, ok := h.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	record, found, err := group.Character.Load(context.Background(), characterID)
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	return record, found
}

func (h *gmtoolWriteHarness) roster(t *testing.T, accountID string) []dnfrepo.CharacterRecord {
	t.Helper()
	group, ok := h.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	records, err := group.Character.ListByAccount(context.Background(), accountID, dnfrepo.DefaultCharacterSlotLimit)
	if err != nil {
		t.Fatalf("list characters: %v", err)
	}
	return records
}

// 软删除：delete_flag=1 + 让出槽位；选人列表与角色名索引里都不再出现。
func TestChardelSoftDeletesAndFreesTheSlot(t *testing.T) {
	harness := newChardelSoftHarness(t)
	group, _ := harness.server.deps.Repos()
	before, found := harness.character(t, "1")
	if !found {
		t.Fatal("fixture 里必须有角色 1")
	}
	if err := dnfrepo.SaveCharacterFields(context.Background(), group.Character,
		dnfrepo.CharacterRecord{
			CharacterID: "1", AccountID: "acc-1", Slot: 3, Name: "tester", Job: "11", Level: 90,
			Stats: map[string]int64{},
		}, dnfrepo.CharacterFieldBase); err != nil {
		t.Fatalf("prep slot: %v", err)
	}

	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	requireOK(t, status, response)
	if response["name"] != "tester" || response["characterId"] != float64(1) {
		t.Fatalf("响应必须带前端读的 name/characterId: %+v", response)
	}
	if response["deleteMode"] != "soft" || response["deleteFlag"] != float64(chardelDeletedFlag) ||
		response["dataRetained"] != true || response["rowsDeleted"] != float64(0) {
		t.Fatalf("响应必须说清是软删除: %+v", response)
	}
	if response["previousSlot"] != float64(3) || response["tombstoneSlot"] != float64(-1) {
		t.Fatalf("槽位腾挪记录 = %+v", response)
	}
	if response["visibleInRoster"] != false {
		t.Fatalf("删除后不该还在选人列表里: %+v", response)
	}

	after, found := harness.character(t, "1")
	if !found {
		t.Fatal("软删除不该真的删行")
	}
	if after.Stats["delete_flag"] != chardelDeletedFlag {
		t.Fatalf("delete_flag = %d, want %d", after.Stats["delete_flag"], chardelDeletedFlag)
	}
	if after.Slot != -1 {
		t.Fatalf("墓碑槽位 = %d, want -1", after.Slot)
	}
	// 除了 slot 与 delete_flag，别的字段一个都不许动。
	if after.Name != before.Name || after.Job != before.Job || after.Level != before.Level ||
		after.AccountID != before.AccountID {
		t.Fatalf("删除动了别的字段:\nbefore=%+v\nafter =%+v", before, after)
	}
	if len(harness.roster(t, "acc-1")) != 0 {
		t.Fatalf("选人列表里仍有角色: %+v", harness.roster(t, "acc-1"))
	}
	if _, found, err := group.Character.FindIDByName(context.Background(), "tester"); err != nil || found {
		t.Fatalf("角色名应当立刻可被复用: found=%t err=%v", found, err)
	}
	fields := harness.eventFields(t, "gm-character-delete")
	if fields["delete_mode"] != "soft" || fields["rows_deleted"] != 0 ||
		fields["previous_slot"] != 3 || fields["visible_after"] != false {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 同一槽位删第二个角色：唯一键 (account_id, slot, delete_flag) 不允许两个墓碑
// 挤在同一槽位，所以墓碑必须各自让出到唯一的负槽位。
func TestChardelSecondDeleteInTheSameSlotSucceeds(t *testing.T) {
	harness := newChardelSoftHarness(t)
	group, _ := harness.server.deps.Repos()
	ctx := context.Background()

	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	requireOK(t, status, response)

	// 同槽位（slot 0）建第二个角色，模拟"复制靶子 → 删 → 再复制 → 再删"。
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "2", AccountID: "acc-1", Slot: 0, Name: "clone", Job: "11", Level: 90,
		Stats: map[string]int64{},
	}); err != nil {
		t.Fatalf("save clone: %v", err)
	}
	status, response = harness.deleteCharacter(t, "2", chardelConfirmText)
	requireOK(t, status, response)
	if response["tombstoneSlot"] != float64(-2) {
		t.Fatalf("第二个墓碑槽位 = %+v", response)
	}
	first, _ := harness.character(t, "1")
	second, _ := harness.character(t, "2")
	if first.Slot == second.Slot {
		t.Fatalf("两个墓碑挤在同一槽位: %d / %d", first.Slot, second.Slot)
	}
	if len(harness.roster(t, "acc-1")) != 0 {
		t.Fatalf("选人列表应清空: %+v", harness.roster(t, "acc-1"))
	}
}

// 确认文本不对：拒绝且零写入（上游 :652-655）。
func TestChardelRefusesWrongConfirmText(t *testing.T) {
	harness := newChardelSoftHarness(t)
	before, _ := harness.character(t, "1")
	status, response := harness.deleteCharacter(t, "1", "delete")
	if message := requireFailure(t, status, response); !strings.Contains(message, "确认文本不正确") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	after, _ := harness.character(t, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%+v\nafter =%+v", before, after)
	}
	if fields := harness.eventFields(t, "gm-character-delete-rejected"); fields["reason"] != "confirm_text_mismatch" {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 已经删除过的角色：明确拒绝（不重复写、不改槽位）。
func TestChardelRefusesAlreadyDeletedCharacter(t *testing.T) {
	harness := newChardelSoftHarness(t)
	group, _ := harness.server.deps.Repos()
	record, _ := harness.character(t, "1")
	record = dnfrepo.CloneCharacter(record)
	if record.Stats == nil {
		record.Stats = map[string]int64{}
	}
	record.Stats["delete_flag"] = 1
	if err := group.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("prep: %v", err)
	}
	before, _ := harness.character(t, "1")
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "已经是删除状态") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	after, _ := harness.character(t, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 角色不存在：走既有的 characterRequest（"角色不存在"）。
func TestChardelMissingCharacterFails(t *testing.T) {
	harness := newChardelSoftHarness(t)
	status, response := harness.deleteCharacter(t, "42", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "角色不存在") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 在线守卫：**不看开关**（删除是不可逆操作，见 api_character_delete.go 的注释）。
func TestChardelRefusesOnlineCharacterRegardlessOfTheSwitch(t *testing.T) {
	harness := newChardelSoftHarness(t)
	harness.server.cfg.RequireOffline = false
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }
	before, _ := harness.character(t, "1")
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "正在游戏内在线") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	after, _ := harness.character(t, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%+v\nafter =%+v", before, after)
	}
	if fields := harness.eventFields(t, "gm-character-delete-rejected"); fields["reason"] != "character_online" {
		t.Fatalf("审计字段 = %+v", fields)
	}

	// 探针未注入：安全降级（放行 + 具名日志），与 online_guard.go 的既定口径一致。
	harness.server.deps.Online = nil
	status, response = harness.deleteCharacter(t, "1", chardelConfirmText)
	requireOK(t, status, response)
	if !harness.hasEvent("gm-character-delete-probe-missing") {
		t.Fatalf("探针缺失必须留日志: %v", harness.eventKinds())
	}
}

// 软删除保留全部关联行（这正是"不留孤儿行、不破坏登录"的做法）。
func TestChardelKeepsChildRows(t *testing.T) {
	harness := newChardelSoftHarness(t)
	group, _ := harness.server.deps.Repos()
	ctx := context.Background()
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]dnfrepo.ItemStack{"0:9": {ItemID: 7001, Count: 1}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	if err := group.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "1",
		States:      map[int64]dnfrepo.QuestState{1: {Status: "completed"}},
	}); err != nil {
		t.Fatalf("save quest: %v", err)
	}
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	requireOK(t, status, response)

	if _, found, err := group.Inventory.Load(ctx, "1"); err != nil || !found {
		t.Fatalf("背包行不该被删: found=%t err=%v", found, err)
	}
	if _, found, err := group.Quest.Load(ctx, "1"); err != nil || !found {
		t.Fatalf("任务行不该被删: found=%t err=%v", found, err)
	}
}

// 没有归属账号的行：拒绝（隐藏之后说不清属于谁）。
func TestChardelRefusesCharacterWithoutAccount(t *testing.T) {
	harness := newChardelSoftHarness(t)
	group, _ := harness.server.deps.Repos()
	if err := group.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1", AccountID: "", Slot: 0, Name: "tester", Job: "11", Level: 90,
		Stats: map[string]int64{},
	}); err != nil {
		t.Fatalf("prep: %v", err)
	}
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "缺少归属账号") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 墓碑槽位必须是唯一负值（函数级保证）。
func TestChardelTombstoneSlotIsUniqueAndNegative(t *testing.T) {
	first, err := chardelTombstoneSlot(1)
	if err != nil || first != -1 {
		t.Fatalf("chardelTombstoneSlot(1) = %d, %v", first, err)
	}
	second, err := chardelTombstoneSlot(2)
	if err != nil || second != -2 {
		t.Fatalf("chardelTombstoneSlot(2) = %d, %v", second, err)
	}
	if first == second {
		t.Fatal("不同角色的墓碑槽位必须不同，否则唯一键会撞")
	}
	if _, err := chardelTombstoneSlot(0); err == nil {
		t.Fatal("角色号 <= 0 必须拒绝")
	}
}

// ---------------------------------------------------------------------------
// 硬删除（默认档）：假事务 + 真计划/真谓词/真残留校验
// ---------------------------------------------------------------------------

// chardelFakeTable 是一张假表：列名 + 行（只存字符串值，够本文件的谓词用）。
type chardelFakeTable struct {
	columns []string
	rows    []map[string]string
}

// chardelFakeDB 实现 chardelHardDB：记录每一条语句，并按谓词真的删行/数行。
//
// 它只认本文件真的会生成的那三种谓词形状（`col = ?`、`scope = ? OR scope LIKE ?`、
// `child IN (SELECT child FROM parent WHERE character_id = ?)`）——也正因为如此，
// 它同时钉住了"服务端到底发了什么 SQL"。
type chardelFakeDB struct {
	tables map[string]*chardelFakeTable

	executed  []string
	counted   []string
	commits   int
	rollbacks int

	failExec map[string]error // 表名 → Exec 报错
	residual map[string]int64 // 表名 → COUNT 恒返回该值（模拟"删不干净"）
}

func newChardelFakeDB() *chardelFakeDB {
	return &chardelFakeDB{tables: map[string]*chardelFakeTable{}}
}

func (db *chardelFakeDB) addTable(name string, columns ...string) *chardelFakeTable {
	table := &chardelFakeTable{columns: columns}
	db.tables[name] = table
	return table
}

func (db *chardelFakeDB) addRow(table string, row map[string]string) {
	db.tables[table].rows = append(db.tables[table].rows, row)
}

func (db *chardelFakeDB) Begin(ctx context.Context) (chardelHardTx, error) {
	return &chardelFakeTx{db: db}, nil
}

type chardelFakeTx struct{ db *chardelFakeDB }

func (tx *chardelFakeTx) Names(ctx context.Context, query string, args ...any) ([][2]string, error) {
	out := make([][2]string, 0, 64)
	for name, table := range tx.db.tables {
		for _, column := range table.columns {
			out = append(out, [2]string{name, column})
		}
	}
	return out, nil
}

// parse 把 `DELETE FROM `t` WHERE <pred>` / `SELECT COUNT(*) FROM `t` WHERE <pred>` 拆开。
func chardelParseStatement(query string) (string, string) {
	const deletePrefix = "DELETE FROM "
	const countPrefix = "SELECT COUNT(*) FROM "
	rest := ""
	switch {
	case strings.HasPrefix(query, deletePrefix):
		rest = query[len(deletePrefix):]
	case strings.HasPrefix(query, countPrefix):
		rest = query[len(countPrefix):]
	}
	table := strings.TrimSpace(strings.SplitN(rest, " WHERE ", 2)[0])
	predicate := ""
	if parts := strings.SplitN(rest, " WHERE ", 2); len(parts) == 2 {
		predicate = strings.TrimSpace(parts[1])
	}
	return strings.Trim(table, "`"), predicate
}

func (tx *chardelFakeTx) match(table, predicate string, args []any, row map[string]string) bool {
	switch {
	case predicate == "`scope` = ? OR `scope` LIKE ?":
		scope := row["scope"]
		return scope == args[0].(string) || strings.HasPrefix(scope, strings.TrimSuffix(args[1].(string), "%"))
	case strings.HasSuffix(predicate, " IN (SELECT "):
		return false
	case strings.Contains(predicate, " IN (SELECT "):
		child := strings.Trim(strings.SplitN(predicate, " IN (SELECT ", 2)[0], "`")
		rest := strings.SplitN(predicate, " IN (SELECT ", 2)[1]
		fromIndex := strings.Index(rest, " FROM ")
		if fromIndex < 0 {
			return false
		}
		tail := strings.TrimSpace(rest[fromIndex+len(" FROM "):])
		parent := strings.Trim(strings.SplitN(tail, " ", 2)[0], "`")
		parentTable := tx.db.tables[parent]
		if parentTable == nil {
			return false
		}
		want := args[0].(string)
		for _, parentRow := range parentTable.rows {
			if parentRow["character_id"] == want && parentRow[child] == row[child] {
				return true
			}
		}
		return false
	default:
		column := strings.Trim(strings.SplitN(predicate, " = ?", 2)[0], "`")
		return row[column] == args[0].(string)
	}
}

func (tx *chardelFakeTx) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	table, predicate := chardelParseStatement(query)
	tx.db.executed = append(tx.db.executed, query)
	if err, failed := tx.db.failExec[table]; failed {
		return 0, err
	}
	rows := tx.db.tables[table].rows
	kept := make([]map[string]string, 0, len(rows))
	var deleted int64
	for _, row := range rows {
		if tx.match(table, predicate, args, row) {
			deleted++
			continue
		}
		kept = append(kept, row)
	}
	tx.db.tables[table].rows = kept
	return deleted, nil
}

func (tx *chardelFakeTx) Count(ctx context.Context, query string, args ...any) (int64, error) {
	table, predicate := chardelParseStatement(query)
	tx.db.counted = append(tx.db.counted, query)
	if override, forced := tx.db.residual[table]; forced {
		return override, nil
	}
	var count int64
	for _, row := range tx.db.tables[table].rows {
		if tx.match(table, predicate, args, row) {
			count++
		}
	}
	return count, nil
}

func (tx *chardelFakeTx) Commit() error   { tx.db.commits++; return nil }
func (tx *chardelFakeTx) Rollback() error { tx.db.rollbacks++; return nil }

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// chardelSeedSchema 建一份"与仓库 DDL 同形"的假库（表名带 dnf_ 前缀，与线上一致），
// 并给角色 "1" 在每张表里放一行；其它角色（"2"）也放一行，用来证明不会误删。
func chardelSeedSchema(t *testing.T) *chardelFakeDB {
	t.Helper()
	db := newChardelFakeDB()
	characterIDTables := []string{
		"dnf_characters", "dnf_character_stats", "dnf_character_locations",
		"dnf_character_rosters", "dnf_character_roster_equipment", "dnf_character_roster_lists",
		"dnf_inventories", "dnf_inventory_items", "dnf_inventory_item_extra",
		"dnf_equipments", "dnf_equipment_entries", "dnf_equipment_entry_extra",
		"dnf_pets", "dnf_pet_entries", "dnf_pet_entry_extra", "dnf_pet_clear_tokens",
		"dnf_pet_artifacts", "dnf_pet_artifact_extra", "dnf_quests", "dnf_quest_states",
		"dnf_quest_state_extra", "dnf_skills", "dnf_skill_states", "dnf_skill_layouts",
		"dnf_skill_cooldowns", "dnf_dungeon_permissions", "dnf_mailboxes", "dnf_mails",
		"dnf_mail_metadata", "dnf_mail_attachments", "dnf_mail_attachment_extra",
		"dnf_legacy_character_items",
	}
	for _, name := range characterIDTables {
		columns := []string{"character_id"}
		if name == "dnf_mails" {
			columns = append(columns, "sender_character_id", "recipient_character_id")
		}
		if name == "dnf_legacy_character_items" {
			columns = append(columns, "item_uid")
		}
		db.addTable(name, columns...)
		db.addRow(name, map[string]string{"character_id": "1"})
		db.addRow(name, map[string]string{"character_id": "2"})
	}
	// settings / setting_values：按 scope 键（含 skill_switch_inventory 等所有后缀）。
	for _, name := range []string{"dnf_settings", "dnf_setting_values"} {
		db.addTable(name, "scope")
		for _, scope := range []string{
			"character:1:container_state", "character:1:skill_commands",
			"character:1:character_option", "character:1:skill_switch_inventory",
			"character:1", "character:12:container_state", "2:character:1",
		} {
			db.addRow(name, map[string]string{"scope": scope})
		}
	}
	// legacy 明细表：只有 item_uid / audit_id，挂在父表上。
	db.addTable("dnf_legacy_character_item_extra", "item_uid")
	db.addRow("dnf_legacy_character_item_extra", map[string]string{"item_uid": "9001"}) // 角色 1 的
	db.addRow("dnf_legacy_character_item_extra", map[string]string{"item_uid": "9002"}) // 角色 2 的
	db.tables["dnf_legacy_character_items"].rows[0]["item_uid"] = "9001"
	db.tables["dnf_legacy_character_items"].rows[1]["item_uid"] = "9002"

	db.addTable("dnf_legacy_item_audit_log", "character_id", "audit_id")
	db.addRow("dnf_legacy_item_audit_log", map[string]string{"character_id": "1", "audit_id": "77"})
	db.addRow("dnf_legacy_item_audit_log", map[string]string{"character_id": "2", "audit_id": "78"})
	db.addTable("dnf_legacy_item_audit_payload_values", "audit_id")
	db.addRow("dnf_legacy_item_audit_payload_values", map[string]string{"audit_id": "77"})
	db.addRow("dnf_legacy_item_audit_payload_values", map[string]string{"audit_id": "78"})

	// 一张与本角色无关的表（也带 character_id）不应被误判：它只有别的角色的行。
	db.addTable("dnf_characters_backup", "character_id")
	db.addRow("dnf_characters_backup", map[string]string{"character_id": "2"})
	return db
}

// 默认档硬删除：逐表删、逐表复核、单事务提交，并如实回报逐表条数。
func TestChardelHardDeletesEveryCharacterScopedRow(t *testing.T) {
	db := chardelSeedSchema(t)
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	result, err := server.chardelHardDelete(context.Background(), db, "1")
	if err != nil {
		t.Fatalf("chardelHardDelete: %v", err)
	}
	if db.commits != 1 || db.rollbacks != 0 {
		t.Fatalf("commits=%d rollbacks=%d, want 1/0", db.commits, db.rollbacks)
	}
	if result.total() == 0 {
		t.Fatal("rowsDeleted 不该是 0")
	}
	// 每张表都真的删到了角色 1 的行，且角色 2 的行一行不少。
	for name, table := range db.tables {
		for _, row := range table.rows {
			if row["character_id"] == "1" {
				t.Fatalf("%s 里还剩角色 1 的行: %+v", name, row)
			}
		}
	}
	if len(db.tables["dnf_characters"].rows) != 1 || db.tables["dnf_characters"].rows[0]["character_id"] != "2" {
		t.Fatalf("角色 2 的行被误删: %+v", db.tables["dnf_characters"].rows)
	}
	if len(db.tables["dnf_characters_backup"].rows) != 1 {
		t.Fatalf("无关表被删空: %+v", db.tables["dnf_characters_backup"].rows)
	}
	// settings：只删 character:1 与 character:1:*，不碰 character:12: / 2:character:1。
	var scopes []string
	for _, row := range db.tables["dnf_settings"].rows {
		scopes = append(scopes, row["scope"])
	}
	sort.Strings(scopes)
	if strings.Join(scopes, ",") != "2:character:1,character:12:container_state" {
		t.Fatalf("settings 残留 = %v, want 只剩 2:character:1 与 character:12:container_state", scopes)
	}
	if !containsString(scopes, "character:12:container_state") {
		t.Fatalf("settings 残留 = %v（character:12:* 不能被当成 character:1:*）", scopes)
	}
	// setting_values 同谓词、同结果（skill_switch_inventory 这些值行就存在这里）。
	valuesScopes := []string{}
	for _, row := range db.tables["dnf_setting_values"].rows {
		valuesScopes = append(valuesScopes, row["scope"])
	}
	sort.Strings(valuesScopes)
	if strings.Join(valuesScopes, ",") != "2:character:1,character:12:container_state" {
		t.Fatalf("setting_values 残留 = %v", valuesScopes)
	}
	for _, gone := range []string{
		"character:1:container_state", "character:1:skill_commands",
		"character:1:character_option", "character:1:skill_switch_inventory", "character:1",
	} {
		if containsString(valuesScopes, gone) {
			t.Fatalf("scope %s 没被清掉: %v", gone, valuesScopes)
		}
	}
	// legacy 明细：角色 1 的明细删了，角色 2 的还在。
	if len(db.tables["dnf_legacy_character_item_extra"].rows) != 1 ||
		db.tables["dnf_legacy_character_item_extra"].rows[0]["item_uid"] != "9002" {
		t.Fatalf("legacy item extra 清理不对: %+v", db.tables["dnf_legacy_character_item_extra"].rows)
	}
	if len(db.tables["dnf_legacy_item_audit_payload_values"].rows) != 1 ||
		db.tables["dnf_legacy_item_audit_payload_values"].rows[0]["audit_id"] != "78" {
		t.Fatalf("legacy audit payload 清理不对: %+v", db.tables["dnf_legacy_item_audit_payload_values"].rows)
	}
	// 逐表复核必须每张表都跑过一遍（数量 = 计划长度）。
	if len(db.counted) != len(result.Deletes) {
		t.Fatalf("复核语句 %d 条, 计划 %d 张表", len(db.counted), len(result.Deletes))
	}
	// 主行最后删：计划里 dnf_characters 必须排在所有 character_id 表之后。
	last := result.Deletes[len(result.Deletes)-1]
	if last.Table != "dnf_characters" {
		t.Fatalf("最后一条删除 = %s, want dnf_characters（主行最后删）", last.Table)
	}
	// scope 谓词必须覆盖所有 character:1:* 后缀（含 skill_switch_inventory）。
	var scopePredicate string
	for _, item := range result.Deletes {
		if item.Key == "scope" && item.Table == "dnf_settings" {
			scopePredicate = item.Predicate
		}
	}
	if scopePredicate != "`scope` = ? OR `scope` LIKE ?" {
		t.Fatalf("scope 谓词 = %q", scopePredicate)
	}
}

// 残留行 ⇒ 整体回滚 + 具名报出表名（不提交、不假装成功）。
func TestChardelHardRollsBackOnResidualRows(t *testing.T) {
	db := chardelSeedSchema(t)
	db.residual = map[string]int64{"dnf_inventory_items": 3} // 模拟某张表删不干净
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	_, err := server.chardelHardDelete(context.Background(), db, "1")
	if err == nil {
		t.Fatal("有残留时必须失败")
	}
	if !errors.Is(err, chardelErrResidualRows) || !strings.Contains(err.Error(), "dnf_inventory_items") {
		t.Fatalf("错误 = %v, want 残留且点名 dnf_inventory_items", err)
	}
	if db.commits != 0 || db.rollbacks != 1 {
		t.Fatalf("commits=%d rollbacks=%d, want 0/1", db.commits, db.rollbacks)
	}
	if chardelHardReasonFromError(err) != "residual_rows" {
		t.Fatalf("reason = %s", chardelHardReasonFromError(err))
	}
}

// 主行删到 0 行（例如 GM 的 DSN 指向了另一个库）⇒ 整体回滚 + 具名失败。
func TestChardelHardRollsBackWhenCharacterRowMissing(t *testing.T) {
	db := chardelSeedSchema(t)
	// 只清掉 dnf_characters 里角色 1 的行：模拟"这张表在别的库/没有该角色"。
	kept := make([]map[string]string, 0, 1)
	for _, row := range db.tables["dnf_characters"].rows {
		if row["character_id"] != "1" {
			kept = append(kept, row)
		}
	}
	db.tables["dnf_characters"].rows = kept
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	_, err := server.chardelHardDelete(context.Background(), db, "1")
	if err == nil || !errors.Is(err, chardelErrCharacterRowMissing) {
		t.Fatalf("错误 = %v, want chardelErrCharacterRowMissing", err)
	}
	if db.commits != 0 || db.rollbacks != 1 {
		t.Fatalf("commits=%d rollbacks=%d, want 0/1", db.commits, db.rollbacks)
	}
	if chardelHardReasonFromError(err) != "character_row_missing" {
		t.Fatalf("reason = %s", chardelHardReasonFromError(err))
	}
}

// 任意一条 DELETE 报错 ⇒ 整体回滚 + 具名失败。
func TestChardelHardRollsBackOnExecFailure(t *testing.T) {
	db := chardelSeedSchema(t)
	db.failExec = map[string]error{"dnf_equipment_entries": errors.New("boom")}
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	_, err := server.chardelHardDelete(context.Background(), db, "1")
	if err == nil || !errors.Is(err, chardelErrExecFailed) ||
		!strings.Contains(err.Error(), "dnf_equipment_entries") {
		t.Fatalf("错误 = %v", err)
	}
	if db.commits != 0 || db.rollbacks != 1 {
		t.Fatalf("commits=%d rollbacks=%d, want 0/1", db.commits, db.rollbacks)
	}
}

// 删完之后：同一槽位、同一角色名可以立刻再建一个（唯一键不再被占）。
//
// 说明：单测里"角色仓储"是内存实现、"SQL"是假事务，生产里两者是**同一个 MySQL**。
// 所以这里分两步如实钉：
//  1. 硬删除的 SQL 计划里 dnf_characters 的主行被删掉且复核为 0（唯一键
//     uk_dnf_characters_account_slot_active 的载体行没了）；
//  2. 把内存仓储里的同一行也去掉（模拟"就是那一行"）后，CreateCharacter 用同一槽位 +
//     同一角色名能成功 —— 这正是线上会发生的事。
func TestChardelHardFreesSlotAndNameForReuse(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	group, _ := harness.server.deps.Repos()
	ctx := context.Background()

	db := chardelSeedSchema(t)
	result, err := harness.server.chardelHardDelete(ctx, db, "1")
	if err != nil {
		t.Fatalf("chardelHardDelete: %v", err)
	}
	foundCharacterDelete := false
	for _, item := range result.Deletes {
		if item.Table == "dnf_characters" && item.Key == "character_id" &&
			item.Predicate == "`character_id` = ?" {
			foundCharacterDelete = true
		}
	}
	if !foundCharacterDelete {
		t.Fatal("计划里缺 dnf_characters 主行删除")
	}

	// 唯一的"假"一步：内存仓储与假事务是两套存储；生产里是同一行。
	deleter, ok := group.Character.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		t.Skip("内存角色仓储没有 Delete，跳过'同槽位复用'的第二步")
	}
	if err := deleter.Delete(ctx, "1"); err != nil {
		t.Fatalf("delete memory character: %v", err)
	}
	if err := dnfrepo.CreateCharacter(ctx, group.Character, dnfrepo.CharacterRecord{
		CharacterID: "9", AccountID: "acc-1", Slot: 0, Name: "tester", Job: "11", Level: 90,
		Stats: map[string]int64{},
	}); err != nil {
		t.Fatalf("同槽位/同名重建失败（唯一键没被释放）: %v", err)
	}
}

// 开关解析：默认硬删除；只有 soft / legacy 退回软删除。
func TestChardelHardModeSwitch(t *testing.T) {
	for _, testCase := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "hard", want: true},
		{value: "HARD", want: true},
		{value: "on", want: true},
		{value: "true", want: true},
		{value: "yes", want: true},
		{value: "unknown", want: true},
		{value: "soft", want: false},
		{value: "SOFT", want: false},
		{value: " legacy ", want: false},
	} {
		if got := chardelHardModeFromEnv(testCase.value); got != testCase.want {
			t.Fatalf("chardelHardModeFromEnv(%q) = %t, want %t", testCase.value, got, testCase.want)
		}
	}
}

// 默认档（硬删除）在 s.db 未就绪时明确失败，且**零写入**（不静默退化成软删除）。
func TestChardelHardWithoutDatabaseFailsClearly(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil) // s.db == nil
	before, _ := harness.character(t, "1")
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "GM 数据库句柄未就绪") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	after, _ := harness.character(t, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("失败路径必须零写入:\nbefore=%+v\nafter =%+v", before, after)
	}
	if fields := harness.eventFields(t, "gm-character-delete-failed"); fields["delete_mode"] != "hard" ||
		fields["reason"] != "database_unavailable" {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// `*_characters_backup` 这种"多后缀"表**不能**冒充角色主行：
// 就算它里面有这个角色的行、主表里没有，也必须 fail-closed（而不是被它蒙混成"删了 1 行"）。
func TestChardelHardCharacterTablePatternIsStrict(t *testing.T) {
	for _, testCase := range []struct {
		table string
		want  bool
	}{
		{table: "characters", want: true},
		{table: "dnf_characters", want: true},
		{table: "legacy_characters", want: true},
		{table: "dnf_characters_backup", want: false},
		{table: "dnf_character_stats", want: false},
		{table: "dnf_character_rosters", want: false},
	} {
		if got := chardelIsCharacterTable(testCase.table); got != testCase.want {
			t.Fatalf("chardelIsCharacterTable(%q) = %t, want %t", testCase.table, got, testCase.want)
		}
	}

	db := chardelSeedSchema(t)
	// 把角色 1 的主行"搬"到 backup 表里：主表没有、backup 有。
	kept := make([]map[string]string, 0, 1)
	for _, row := range db.tables["dnf_characters"].rows {
		if row["character_id"] != "1" {
			kept = append(kept, row)
		}
	}
	db.tables["dnf_characters"].rows = kept
	db.addRow("dnf_characters_backup", map[string]string{"character_id": "1"})
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	_, err := server.chardelHardDelete(context.Background(), db, "1")
	if err == nil || !errors.Is(err, chardelErrCharacterRowMissing) {
		t.Fatalf("错误 = %v, want chardelErrCharacterRowMissing（backup 表不算主行）", err)
	}
	if db.commits != 0 || db.rollbacks != 1 {
		t.Fatalf("commits=%d rollbacks=%d, want 0/1", db.commits, db.rollbacks)
	}
}

// 硬删除也要走在线守卫（在线 ⇒ 拒绝）。
func TestChardelHardRefusesOnlineCharacter(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }
	status, response := harness.deleteCharacter(t, "1", chardelConfirmText)
	if message := requireFailure(t, status, response); !strings.Contains(message, "正在游戏内在线") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 响应契约：deleteMode=hard + 逐表条数 + rowsDeleted 合计 + verified。
func TestChardelHardResponseReportsPerTableCounts(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	db := chardelSeedSchema(t)
	character, found := harness.character(t, "1")
	if !found {
		t.Fatal("fixture 里必须有角色 1")
	}
	recorder := httptest.NewRecorder()
	harness.server.chardelApplyHardWith(recorder, context.Background(), 1, character, db)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	requireOK(t, recorder.Code, response)
	if response["deleteMode"] != "hard" || response["verified"] != true {
		t.Fatalf("响应 = %+v", response)
	}
	if response["name"] != "tester" || response["characterId"] != float64(1) {
		t.Fatalf("响应必须带前端读的 name/characterId: %+v", response)
	}
	tables, _ := response["tables"].([]any)
	if len(tables) == 0 || response["tablesScanned"] != float64(len(tables)) {
		t.Fatalf("tables = %v / tablesScanned = %v", len(tables), response["tablesScanned"])
	}
	var sum float64
	seenCharacterRow := false
	for _, entry := range tables {
		row, _ := entry.(map[string]any)
		count, _ := row["deleted"].(float64)
		sum += count
		if row["table"] == "dnf_characters" {
			seenCharacterRow = true
			if count != 1 {
				t.Fatalf("主行条数 = %v, want 1", row["deleted"])
			}
		}
	}
	if !seenCharacterRow {
		t.Fatalf("tables 里必须有 dnf_characters: %v", tables)
	}
	if response["rowsDeleted"] != sum || sum <= 0 {
		t.Fatalf("rowsDeleted = %v, 逐表合计 = %v", response["rowsDeleted"], sum)
	}
	fields := harness.eventFields(t, "gm-character-delete")
	if fields["delete_mode"] != "hard" || fields["verified"] != true ||
		fields["rows_deleted"] != int64(sum) || fields["tables"] != len(tables) {
		t.Fatalf("审计字段 = %+v", fields)
	}

	// 软删除留下的墓碑行也必须能被硬删除清掉（否则默认档一上线就有一批清不掉的残留）。
	harness2 := newGmtoolWriteHarness(t, nil)
	tombstone, _ := harness2.character(t, "1")
	tombstone = dnfrepo.CloneCharacter(tombstone)
	if tombstone.Stats == nil {
		tombstone.Stats = map[string]int64{}
	}
	tombstone.Stats["delete_flag"] = chardelDeletedFlag
	tombstone.Slot = -1
	recorder = httptest.NewRecorder()
	harness2.server.chardelApplyHardWith(recorder, context.Background(), 1, tombstone, chardelSeedSchema(t))
	response = map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	requireOK(t, recorder.Code, response)
	if response["deleteMode"] != "hard" {
		t.Fatalf("墓碑行删除响应 = %+v", response)
	}
}
