package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住「稀有克隆装扮显示被克隆时装外观」这条链路的三个投影点与开关。
//
// 真档取证（2026-09-28 在 runtime/data/dnf/Script.pvf 上实跑，见
// TestRealScriptPVFClassifiesRareCloneAndOrdinaryAvatar）：
//   - 42601/42603/42605/42607/42609/42611/42613/42615（剑士 稀有克隆装扮
//     帽子/头发/脸部/胸部/上衣/裤子/腰带/鞋子）每件都是 `[equipment type]
//     [* avatar]` + `[item category] clear avatar`；
//   - 101580163 / 101590068 / 101600023（桐人皮肤 / 光环 / 武器装扮）是装扮但
//     **没有** clear avatar；
//   - 415500017（克隆）/ 415500058（普通）这一对与同族参考实现
//     90原神开源0.1.8 的真档用例一致；
//   - 27000 是普通武器，既不是装扮也不是克隆。
//
// 所以今天"克隆装扮不显示外观"的根因不是分类错误，而是：克隆件自己的模板是透明件，
// 而"我克隆的是哪一件"这个值从来没被记下来，也没被任何投影替换过。

// TestCurrentPVFDocumentHasRepeatedCloneAvatarCategory 钉住"必须遍历所有同名
// section"这一点：internal/modules/dnf/pvf 的 Section(name)（types.go:67-84）命中
// 第一个同名 section 就返回，真档里有 70/4000 份装备文档出现重复 `[item category]`，
// 只读第一段就会漏判。
func TestCurrentPVFDocumentHasRepeatedCloneAvatarCategory(t *testing.T) {
	doc, err := dnfpvf.Parse("clone.equ", "[item category]\n`other`\n[item category]\n`clear avatar`\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Text("item category"); !ok {
		t.Fatal("fixture lost its first item category section")
	}
	first, _ := doc.Text("item category")
	if normalizeCloneAvatarPVFText(first) == "clear avatar" {
		t.Fatalf("fixture no longer exercises the first-section limitation: first=%q", first)
	}
	if !currentPVFDocumentHasText(doc, "item category", currentCloneAvatarCategory) {
		t.Fatal("repeated clear-avatar category was not detected")
	}
}

// TestRealScriptPVFClassifiesRareCloneAndOrdinaryAvatar 在真档上钉住分类器。
// 需要 DNFBRIDGE_REAL_PVF_SMOKE 指向真实 Script.pvf。
func TestRealScriptPVFClassifiesRareCloneAndOrdinaryAvatar(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run real Script.pvf clone-avatar smoke")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive}

	// 剑士 8 件稀有克隆装扮（真档 character/swordman/avatar/**/a*_rareclonesw.equ）。
	for _, itemID := range []uint32{42601, 42603, 42605, 42607, 42609, 42611, 42613, 42615} {
		if isAvatar, isClone := service.currentClassifyAvatarItem(itemID); !isAvatar || !isClone {
			t.Fatalf("rare clone item %d classification avatar=%t clone=%t, want true/true", itemID, isAvatar, isClone)
		}
	}
	// 同族参考实现用的那一对，证明本档与 0.1.8 判据一致。
	if isAvatar, isClone := service.currentClassifyAvatarItem(415500017); !isAvatar || !isClone {
		t.Fatalf("0.1.8 rare clone 415500017 classification avatar=%t clone=%t", isAvatar, isClone)
	}
	// 普通装扮：是装扮、不是克隆。
	for _, itemID := range []uint32{415500058, 101580163, 101590068, 101600023} {
		if isAvatar, isClone := service.currentClassifyAvatarItem(itemID); !isAvatar || isClone {
			t.Fatalf("ordinary avatar %d classification avatar=%t clone=%t, want true/false", itemID, isAvatar, isClone)
		}
	}
	// 普通装备：既不是装扮也不是克隆。
	if isAvatar, isClone := service.currentClassifyAvatarItem(27000); isAvatar || isClone {
		t.Fatalf("ordinary weapon 27000 classification avatar=%t clone=%t, want false/false", isAvatar, isClone)
	}
}

// cloneAvatarProjectionFixture 造一件穿在 actor 外观槽 3（上衣）的装扮：
// 穿戴行 ItemID = 克隆件，Extra 里带着"要显示成哪一件"。
func cloneAvatarProjectionFixture(t *testing.T, extra map[string]string) dnfrepo.EquipmentRecord {
	t.Helper()
	merged := map[string]string{"current_exe_equipment_type": "3"}
	for key, value := range extra {
		merged[key] = value
	}
	entry := dnfrepo.EquipmentEntry{
		SlotIndex: 3,
		ItemID:    cloneAvatarProjectionCloneID,
		RawEntry:  make([]byte, currentItemListEntryWireSize),
		Extra:     merged,
	}
	return dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{"3": entry},
	}
}

const (
	cloneAvatarProjectionCloneID      int64  = 42609
	cloneAvatarProjectionAppearanceID uint32 = 101580163
)

// TestCloneAvatarAppearanceProjectsMode0RosterAndMode1 钉住修复档的三个下行投影：
// mode0 外观表、名册/场景对象装备摘要、mode1 装备创建行。
func TestCloneAvatarAppearanceProjectsMode0RosterAndMode1(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	record := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})

	mode0, err := buildCurrentActorMode0AppearanceSnapshotFromEquipment(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentActorMode0AppearanceGoldenItemID(t, mode0, 3); got != cloneAvatarProjectionAppearanceID {
		t.Fatalf("mode0 slot 3 item id = %d, want cloned appearance %d", got, cloneAvatarProjectionAppearanceID)
	}

	roster := equipmentRosterSummary(record)
	if len(roster) != 1 || roster[0].Slot != 3 || roster[0].ItemIDOrIcon != int64(cloneAvatarProjectionAppearanceID) {
		t.Fatalf("roster clone projection = %+v, want slot 3 item %d", roster, cloneAvatarProjectionAppearanceID)
	}

	rows := newCloneAvatarMode1Rows(t, record)
	if len(rows) != 1 {
		t.Fatalf("mode1 rows = %d, want 1", len(rows))
	}
	if rows[0].itemID != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("mode1 row item id = %d, want the clone itself %d", rows[0].itemID, cloneAvatarProjectionCloneID)
	}
	if rows[0].linkedItemID != cloneAvatarProjectionAppearanceID {
		t.Fatalf("mode1 row linkedItemID = %d, want cloned appearance %d", rows[0].linkedItemID, cloneAvatarProjectionAppearanceID)
	}
}

// TestCloneAvatarAppearanceRollbackRestoresRawItemIDs 钉住回退档：同一个夹具在
// DNFBRIDGE_CLONE_AVATAR_APPEARANCE=off 下三个投影都必须退回克隆件自己的 ItemID，
// 而且注入装备域的分类器必须是 nil（写入侧也关掉）。
func TestCloneAvatarAppearanceRollbackRestoresRawItemIDs(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOff)
	record := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})

	mode0, err := buildCurrentActorMode0AppearanceSnapshotFromEquipment(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentActorMode0AppearanceGoldenItemID(t, mode0, 3); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("rollback mode0 slot 3 item id = %d, want clone's own id %d", got, cloneAvatarProjectionCloneID)
	}

	roster := equipmentRosterSummary(record)
	if len(roster) != 1 || roster[0].ItemIDOrIcon != cloneAvatarProjectionCloneID {
		t.Fatalf("rollback roster projection = %+v, want clone's own id %d", roster, cloneAvatarProjectionCloneID)
	}

	rows := newCloneAvatarMode1Rows(t, record)
	if len(rows) != 1 || rows[0].itemID != uint32(cloneAvatarProjectionCloneID) || rows[0].linkedItemID != 0 {
		t.Fatalf("rollback mode1 rows = %+v, want clone id %d with no link", rows, cloneAvatarProjectionCloneID)
	}

	if classifier := (&Service{}).currentCloneAvatarClassifier(); classifier != nil {
		t.Fatal("rollback switch must not inject an avatar classifier into the equipment owner")
	}
}

// TestCloneAvatarAppearanceEnabledSwitchNeverLeavesClassifiedTemplateUnlinked 钉住
// 修复档下分类器确实被注入（写入侧打开），与上面那条回退断言互为反面。
func TestCloneAvatarAppearanceEnabledSwitchInjectsAvatarClassifier(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	if classifier := (&Service{}).currentCloneAvatarClassifier(); classifier == nil {
		t.Fatal("default switch must inject the PVF avatar classifier")
	}
}

// TestCloneAvatarAppearanceSwitchRejectsUnknownValue 钉住"非法值回退到修复档并报 bad"，
// 与启动日志告警分支（service.go 的 clone avatar appearance invalid）同一判据。
func TestCloneAvatarAppearanceSwitchRejectsUnknownValue(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, "maybe")
	enabled, raw, bad := cloneAvatarAppearanceSetting()
	if !enabled || !bad || raw != "maybe" {
		t.Fatalf("setting = enabled=%t raw=%q bad=%t, want true/maybe/true", enabled, raw, bad)
	}
}

// TestCloneAvatarLinkParticipatesInMode1CreateRowGate 钉住一个**真实的行为副作用**：
// mode1 的"这一行能不能当创建行发"判据 currentMode1EquipmentCreateRowHasVerifiedState
// （userinfo_current_equipment_rules.go:122）本身就把 linkedItemID != 0 当作"已验证状态"。
//
// 所以对一件 rest 状态为零、且 state112 还是 0xFFFFFFFF 哨兵的穿戴行：没有克隆来源时它
// 只能当更新行发；一旦带上克隆外观链接，它就升级成创建行 —— 正是客户端要创建"挂着链接
// 外观的角色装备对象"所需要的那一行。同族参考实现 0.1.8 未改动这条判据
// （该文件与 0.1.8 逐字节相同），只把 linkedItemID 填上，让既有判据自然生效。
func TestCloneAvatarLinkParticipatesInMode1CreateRowGate(t *testing.T) {
	newSentinelRecord := func() dnfrepo.EquipmentRecord {
		return cloneAvatarProjectionFixture(t, map[string]string{
			// 0xFFFFFFFF = 客户端行里"这一格还没有 mode1 状态"的哨兵值。
			"mode1_state_112": "4294967295",
		})
	}

	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOff)
	if create := currentMode1EquipmentCreateRows(newCloneAvatarMode1Rows(t, newSentinelRecord())); len(create) != 0 {
		t.Fatalf("rollback create rows = %d, want 0 (state112 sentinel keeps the row an update row)", len(create))
	}

	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	withLink := cloneAvatarProjectionFixture(t, map[string]string{
		"mode1_state_112":                     "4294967295",
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})
	create := currentMode1EquipmentCreateRows(newCloneAvatarMode1Rows(t, withLink))
	if len(create) != 1 || create[0].linkedItemID != cloneAvatarProjectionAppearanceID {
		t.Fatalf("fixed create rows = %+v, want exactly one row linking %d", create, cloneAvatarProjectionAppearanceID)
	}
}

func newCloneAvatarMode1Rows(t *testing.T, record dnfrepo.EquipmentRecord) []currentMode1EquipmentObjectRow {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Equipment.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	reader := csharpLegacyUserInfoReader{
		ctx:         context.Background(),
		characterID: record.CharacterID,
		service: &Service{repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		}},
	}
	return reader.currentMode1EquipmentObjectRows()
}
