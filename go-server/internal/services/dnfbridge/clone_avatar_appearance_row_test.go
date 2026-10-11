package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住「稀有克隆装扮」在 **msg_id=2 的 NOTI2 外观记录**（厂商
// `Game/Characters/CharacterAppearanceEntry.cs`）上的两个字段，以及新开关
// DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID 的回退档。
//
// 厂商形状（FromBytes，CharacterAppearanceEntry.cs:68-80）：
//
//	slot@+0(u8)、DisplayItemId@+1(i32)、ExpansionLen@+5(i32)、ExpansionData@+9(4B)、
//	State@+13(u8)、LinkItemId@+14(i32)、EnchantValue@+18(u32)、Flag20@+22(u8)
//
// 取值规则（Noti2InventoryProjectionBuilder.cs:249-272）：
//
//	DisplayItemId = ResolveAppearanceDisplayItemId = ClearAvatarId（被克隆件）
//	LinkItemId    = ResolveAppearanceLinkItemId    = core.ItemId（克隆件自己）
//
// 本包两处载体都以这份记录下发：
//   - userinfo subtype0 外观表：`writeCSharpAppearanceEntry`（带 ExpansionLen=4 + 4 字节）
//   - 名册/场景对象 mode0 外观摘要：`writeCurrentSceneObjectEquipSummary`
//     （ExpansionLen 写 0，因此后续字段整体前移 4 字节：State@+9、LinkItemId@+10）

// newCloneAvatarAppearanceReader 造一个只用来读外观表的 userinfo reader。
func newCloneAvatarAppearanceReader(t *testing.T, record dnfrepo.EquipmentRecord) *csharpLegacyUserInfoReader {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Equipment.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return &csharpLegacyUserInfoReader{
		ctx:         context.Background(),
		characterID: record.CharacterID,
		service: &Service{repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		}},
	}
}

// TestCloneAvatarAppearanceRecordWritesDisplayAndLink 钉住 subtype0 外观表的 23 字节
// 记录 bytes → 两个字段都在厂商的偏移上：显示被克隆件、LinkItemId 写克隆件自己。
func TestCloneAvatarAppearanceRecordWritesDisplayAndLink(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	record := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})

	entries := newCloneAvatarAppearanceReader(t, record).currentSubtype0AppearanceEntries()
	if len(entries) != 1 {
		t.Fatalf("subtype0 appearance entries = %d, want 1", len(entries))
	}
	if entries[0].slot != 3 {
		t.Fatalf("appearance slot = %d, want 3", entries[0].slot)
	}
	if entries[0].displayItemID != cloneAvatarProjectionAppearanceID {
		t.Fatalf("appearance displayItemId = %d, want the cloned source %d",
			entries[0].displayItemID, cloneAvatarProjectionAppearanceID)
	}
	if entries[0].linkItemID != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("appearance linkItemID = %d, want the clone itself %d",
			entries[0].linkItemID, cloneAvatarProjectionCloneID)
	}

	var writer packetWriter
	writeCSharpAppearanceEntry(&writer, entries[0])
	row := writer.bytes()
	if len(row) != 23 {
		t.Fatalf("appearance row bytes = %d, want 23 (vendor CharacterAppearanceEntry size)", len(row))
	}
	if row[0] != 3 {
		t.Fatalf("appearance row slot = %d, want 3", row[0])
	}
	if got := binary.LittleEndian.Uint32(row[1:5]); got != cloneAvatarProjectionAppearanceID {
		t.Fatalf("appearance row DisplayItemId @+1 = %d, want the cloned source %d",
			got, cloneAvatarProjectionAppearanceID)
	}
	if got := binary.LittleEndian.Uint32(row[5:9]); got != 4 {
		t.Fatalf("appearance row ExpansionLen @+5 = %d, want 4", got)
	}
	if got := binary.LittleEndian.Uint32(row[14:18]); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("appearance row LinkItemId @+14 = %d, want the clone itself %d (vendor FromBytes offset)",
			got, cloneAvatarProjectionCloneID)
	}
	if got := binary.LittleEndian.Uint32(row[18:22]); got != 0 {
		t.Fatalf("appearance row EnchantValue @+18 = %d, want 0", got)
	}
}

// TestCloneAvatarRosterAppearanceRowWritesLinkItemID 钉住名册/场景对象 mode0 外观摘要
// （19 字节行，ExpansionLen=0）：LinkItemId 落在厂商偏移 +14 减去空扩展块 4 字节后的 +10。
func TestCloneAvatarRosterAppearanceRowWritesLinkItemID(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	record := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})

	rows := equipmentRosterSummary(record)
	if len(rows) != 1 {
		t.Fatalf("roster appearance rows = %d, want 1", len(rows))
	}
	if rows[0].ItemIDOrIcon != int64(cloneAvatarProjectionAppearanceID) {
		t.Fatalf("roster display item = %d, want the cloned source %d",
			rows[0].ItemIDOrIcon, cloneAvatarProjectionAppearanceID)
	}
	if rows[0].OptionalIDOrExpire != cloneAvatarProjectionCloneID {
		t.Fatalf("roster LinkItemId = %d, want the clone itself %d",
			rows[0].OptionalIDOrExpire, cloneAvatarProjectionCloneID)
	}

	var writer packetWriter
	writeCurrentSceneObjectEquipSummary(&writer, rows)
	body := writer.bytes()
	if len(body) != 1+currentSceneObjectEquipSummaryRowBytes {
		t.Fatalf("mode0 appearance body = %d bytes, want %d",
			len(body), 1+currentSceneObjectEquipSummaryRowBytes)
	}
	if body[0] != 1 {
		t.Fatalf("mode0 appearance row count = %d, want 1", body[0])
	}
	if body[1] != 3 {
		t.Fatalf("mode0 appearance row slot = %d, want 3", body[1])
	}
	if got := binary.LittleEndian.Uint32(body[2:6]); got != cloneAvatarProjectionAppearanceID {
		t.Fatalf("mode0 appearance row DisplayItemId @+1 = %d, want the cloned source %d",
			got, cloneAvatarProjectionAppearanceID)
	}
	if got := binary.LittleEndian.Uint32(body[6:10]); got != 0 {
		t.Fatalf("mode0 appearance row ExpansionLen = %d, want 0 (empty expansion block)", got)
	}
	if got := binary.LittleEndian.Uint32(body[11:15]); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("mode0 appearance row LinkItemId = %d, want the clone itself %d (vendor offset +14 shifted by the empty expansion block)",
			got, cloneAvatarProjectionCloneID)
	}
	if got := binary.LittleEndian.Uint32(body[15:19]); got != 0 {
		t.Fatalf("mode0 appearance row EnchantValue = %d, want 0", got)
	}
}

// TestCloneAvatarRowClearIDRollbackLeavesNonCloneBytesUntouched 钉住回退档与"非克隆行不变"：
//   - 关掉 DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID：显示件仍是被克隆件，LinkItemId 回到 0；
//   - 关掉 DNFBRIDGE_CLONE_AVATAR_APPEARANCE：两个字段一起回到改动前（显示克隆件本身、link 0）；
//   - 没有克隆键的普通装扮行：LinkItemId 恒 0（与改动前逐字节相同）。
func TestCloneAvatarRowClearIDRollbackLeavesNonCloneBytesUntouched(t *testing.T) {
	cloneRecord := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})
	ordinaryRecord := cloneAvatarProjectionFixture(t, nil)

	// 新开关回退档：只有 LinkItemId 那一个 u32 回到 0。
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOff)
	entries := newCloneAvatarAppearanceReader(t, cloneRecord).currentSubtype0AppearanceEntries()
	if len(entries) != 1 || entries[0].displayItemID != cloneAvatarProjectionAppearanceID || entries[0].linkItemID != 0 {
		t.Fatalf("row-clear-id rollback appearance = %+v, want display %d with link 0",
			entries, cloneAvatarProjectionAppearanceID)
	}
	rows := equipmentRosterSummary(cloneRecord)
	if len(rows) != 1 || rows[0].ItemIDOrIcon != int64(cloneAvatarProjectionAppearanceID) || rows[0].OptionalIDOrExpire != 0 {
		t.Fatalf("row-clear-id rollback roster = %+v, want display %d with link 0",
			rows, cloneAvatarProjectionAppearanceID)
	}

	// 老开关回退档：整个功能逐字节回到改动前。
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOff)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	entries = newCloneAvatarAppearanceReader(t, cloneRecord).currentSubtype0AppearanceEntries()
	if len(entries) != 1 ||
		entries[0].displayItemID != uint32(cloneAvatarProjectionCloneID) || entries[0].linkItemID != 0 {
		t.Fatalf("appearance rollback = %+v, want the clone's own id %d with link 0",
			entries, cloneAvatarProjectionCloneID)
	}
	rows = equipmentRosterSummary(cloneRecord)
	if len(rows) != 1 ||
		rows[0].ItemIDOrIcon != cloneAvatarProjectionCloneID || rows[0].OptionalIDOrExpire != 0 {
		t.Fatalf("roster rollback = %+v, want the clone's own id %d with link 0",
			rows, cloneAvatarProjectionCloneID)
	}

	// 非克隆行：无论开关如何，LinkItemId 都是 0。
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	ordinaryEntries := newCloneAvatarAppearanceReader(t, ordinaryRecord).currentSubtype0AppearanceEntries()
	if len(ordinaryEntries) != 1 ||
		ordinaryEntries[0].linkItemID != 0 ||
		ordinaryEntries[0].displayItemID != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("ordinary avatar appearance = %+v, want unchanged row with link 0", ordinaryEntries)
	}
	ordinaryRows := equipmentRosterSummary(ordinaryRecord)
	if len(ordinaryRows) != 1 ||
		ordinaryRows[0].OptionalIDOrExpire != 0 ||
		ordinaryRows[0].ItemIDOrIcon != cloneAvatarProjectionCloneID {
		t.Fatalf("ordinary avatar roster = %+v, want unchanged row with link 0", ordinaryRows)
	}
}

// TestCloneAvatarRowClearIDSwitchRejectsUnknownValue 钉住"非法值回退到修复档并报 bad"，
// 与启动日志告警分支（service.go 的 clone avatar row clear id invalid）同一判据。
func TestCloneAvatarRowClearIDSwitchRejectsUnknownValue(t *testing.T) {
	t.Setenv(cloneAvatarRowClearIDEnv, "maybe")
	enabled, raw, bad := cloneAvatarRowClearIDSetting()
	if !enabled || !bad || raw != "maybe" {
		t.Fatalf("setting = enabled=%t raw=%q bad=%t, want true/maybe/true", enabled, raw, bad)
	}
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceLegacy)
	if enabled, raw, bad = cloneAvatarRowClearIDSetting(); enabled || bad || raw != cloneAvatarAppearanceLegacy {
		t.Fatalf("legacy setting = enabled=%t raw=%q bad=%t, want false/legacy/false", enabled, raw, bad)
	}
}
