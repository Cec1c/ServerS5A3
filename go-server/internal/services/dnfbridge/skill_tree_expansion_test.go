package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestSkillTreeTypedMode0LockedAndOwnedWireContract(t *testing.T) {
	character := repo.CharacterRecord{CharacterID: "31", Job: "0", Level: 20, Stats: map[string]int64{"skill_tree_index": 0}}
	locked := buildCurrentSceneObjectEntryTail(character, true)
	equipEnd, ok := currentSceneObjectEquipSummaryEnd(locked, 6)
	if !ok {
		t.Fatal("装备尾块无法读取")
	}
	// 当前固定EXE 22c6458 -> 22bff00；紧随u16+u8及u16两个读取器之后。
	indexOffset := equipEnd + 96
	if indexOffset >= len(locked) || locked[indexOffset] != 0xff {
		t.Fatalf("未购买标记偏移=%d tail=%x", indexOffset, locked)
	}
	character.Stats["skill_tree_expanded"] = 1
	character.Stats["skill_tree_index"] = 1
	owned := buildCurrentSceneObjectEntryTail(character, true)
	if len(locked) != len(owned) || owned[indexOffset] != 1 {
		t.Fatalf("已购买页码: %x", owned)
	}
	owned[indexOffset] = 0xff
	if !bytes.Equal(locked, owned) {
		t.Fatal("解锁意外修改了其他角色字段")
	}
	stats := map[string]int64{}
	seedCreatedSubtype1Stats(stats)
	if stats["skill_tree_index"] != -1 {
		t.Fatalf("新建角色默认已解锁: %v", stats)
	}
}

func TestSkillTreeProjectionUsesTwoProfilesAndCurrentQuickSlots(t *testing.T) {
	record := repo.SkillRecord{Skills: map[int64]repo.SkillState{46: {Level: 3, Enabled: true}}, Points: repo.SkillPointState{RemainingSP: 60, RemainingTP: 8}, Layouts: map[int]repo.SkillLayout{0: {4: 46}, 1: {5: 46}}, Trees: repo.SkillTreeState{Expanded: true, Active: 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}, Points: repo.SkillPointState{RemainingSP: 100, RemainingTP: 10}}}}
	body, slots, err := buildCurrentSceneSkillInfoBody(record, record.Layouts[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	_, outer := consumeCurrentSkillInfoFields(t, body[4:])
	for i, want := range []uint64{100, 60} {
		vars, nested := consumeCurrentSkillInfoFields(t, outer[2][i])
		fields, _ := consumeCurrentSkillInfoFields(t, nested[3][0])
		wantLevel, wantSlot := uint64(1), uint64(4)
		if i == 1 {
			wantLevel, wantSlot = 3, 5
		}
		if vars[1][0] != want || fields[3][0] != wantLevel || fields[1][0] != wantSlot {
			t.Fatalf("第%d页错误: %+v %+v", i, vars, fields)
		}
	}
	if !reflect.DeepEqual(slots, []int{5}) || bytes.Equal(outer[2][0], outer[2][1]) {
		t.Fatalf("两页重复或快捷栏取错页: %v", slots)
	}
}

func TestSkillTreeProjectionSyncsOtherPageWhenCurrentLedgerIsUpToDate(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	record := repo.SkillRecord{CharacterID: "77", Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 200, RemainingSP: 180, SyncedLevel: 2}, Trees: repo.SkillTreeState{Expanded: true, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 40, SyncedLevel: 1}}}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	service := &Service{initialSPTable: map[int]int{1: 100, 2: 100}}
	next, changed, err := service.syncCurrentSceneSkillPointLedger(ctx, repos, repo.CharacterRecord{CharacterID: "77", Level: 2}, record)
	if err != nil || !changed || next.Points != record.Points || next.Trees.Other.Points.RemainingSP != 140 || next.Trees.Other.Points.SyncedLevel != 2 {
		t.Fatalf("升级后未同步另一套: %+v changed=%t err=%v", next, changed, err)
	}
}

func TestRealPVFSkillTreeExpansionPurchaseImmediateAndDuplicateSafe(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF路径后执行技能扩展券验证")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	var product currentCeraShopProduct
	for _, p := range catalog.products {
		if p.ItemID == 821 {
			product = p
			break
		}
	}
	if product.CommodityID == 0 {
		t.Fatal("真实商城缺技能类型扩展券")
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	seedCeraShopAccountCera(t, ctx, repos, "audit", int64(product.CeraPrice)+10000)
	character := repo.CharacterRecord{CharacterID: "77", AccountID: "audit", Job: "0", Level: 20, Stats: map[string]int64{"grow_type": 0, "skill_tree_index": 0}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{accountID: "audit", pvfPath: path, gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, ceraShopCatalog: catalog, pvfItemCatalog: catalog.items, premiumCatalog: mustCurrentCeraShopPremiumCatalog(), repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	rules, err := service.currentSkillTreeExpansionRules(ctx, repos, 77)
	if err != nil {
		t.Fatal(err)
	}
	// 原页手工分配过的等级和余点必须逐字保留；只在隔离内存仓储准备样例。
	first := repo.SkillRecord{CharacterID: "77", Skills: map[int64]repo.SkillState{46: {Level: 2, Enabled: true}}, Points: *rules.PointBaseline, Layouts: map[int]repo.SkillLayout{0: {4: 46}}}
	first.Points.RemainingSP -= 20
	if err := repos.Skill.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	beforeBalance := loadCeraShopAccountCera(t, ctx, repos, "audit")
	conn := &bufferConn{}
	session := &gameSession{conn: conn, accountID: "audit", selectedCharacterID: 77, townActorOwnerChannel: 42}
	request := currentCeraShopPurchaseRequest{Items: []currentCeraShopPurchaseRequestItem{{CommodityID: product.CommodityID}}}
	result, err := service.commitCurrentCeraShopPurchase(ctx, session, catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Skill.Load(ctx, "77")
	bag, _, _ := repos.Inventory.Load(ctx, "77")
	if !result.SkillTreeExpanded || len(result.Updates) != 0 || len(bag.Slots) != 0 || !after.Trees.Expanded || !reflect.DeepEqual(after.Skills, first.Skills) || after.Points != first.Points || after.Trees.Other.Points.RemainingSP != after.Points.TotalSP || after.Trees.Other.Skills[46].Level != rules.InitialLevels[46] || result.CeraAfter != beforeBalance-int64(product.CeraPrice) {
		t.Fatalf("真实扩展券购买: result=%+v skill=%+v bag=%+v", result, after, bag)
	}
	if _, err := service.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err == nil {
		t.Fatal("重复购买未拒绝")
	}
	if balance := loadCeraShopAccountCera(t, ctx, repos, "audit"); balance != result.CeraAfter {
		t.Fatalf("重复购买扣费: %d", balance)
	}
	// 当前客户端用typed mode0接收资格，再接收op19，不发送86旧角色整包。
	if err := service.currentSendSkillTreeExpansionRefresh(ctx, session); err != nil {
		t.Fatal(err)
	}
	mode0, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	_, tailStart, ok := currentSceneObjectLevelForLog(mode0.Body)
	if !ok {
		t.Fatal("购买后没有当前EXE角色包")
	}
	equipEnd, ok := currentSceneObjectEquipSummaryEnd(mode0.Body[tailStart:], 6)
	if !ok || mode0.Body[tailStart+equipEnd+96] != 0 {
		t.Fatal("购买后未投影解锁0")
	}
	skillPacket, _ := splitGameServerUpperPacket(t, rest)
	if skillPacket.Header.MsgID != 19 {
		t.Fatalf("角色重绑后未刷新技能: %+v", skillPacket.Header)
	}
	owner, err := skillcmd.NewOwner(repos, rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyTreeSwitch(ctx, skillcmd.Command{AccountID: "audit", SelectedCharacterID: 77, SkillTree: 0}); err != nil {
		t.Fatal(err)
	}
	cold, _, _ := repos.Skill.Load(ctx, "77")
	if repo.SkillTreeWireIndex(cold) != 1 || cold.Skills[46].Level != rules.InitialLevels[46] || cold.Trees.Other.Skills[46].Level != 2 {
		t.Fatalf("重读切换结果: %+v", cold)
	}
	t.Logf("当前PVF商品=%d item=821 price=%d，购买即时解锁、不入包，首套保留、第二套初始技能数=%d，重读切换成功", product.CommodityID, product.CeraPrice, len(after.Trees.Other.Skills))
}
