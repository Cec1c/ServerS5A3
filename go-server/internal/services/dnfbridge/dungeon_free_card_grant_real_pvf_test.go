package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The existing completed-settlement fixture accepts a real monster death on
// the production combat path. This test then lets the production freeze choose
// and retain the free reward, commits that plan through card selection, and
// verifies a replay cannot mint another bundle.
func TestRealPVFDungeonFreeCardGrantFreezesProductionPlanAndDeliversOnce(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service, runtime, session, connection, _ := prepareCompletedSettlementRuntime(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("结算夹具仓储不可用")
	}
	catalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service.dungeonMonsterTable = catalog

	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	character, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取夹具角色：found=%t err=%v", found, err)
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.AccountID = "account-1"
	character.Level = 90
	character.Stats["exp"] = 90
	character.Stats["gold"] = 100
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 90},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	runtime.Character = character
	runtime.entryCharacterLevel = 90
	runtime.Dungeon.Metadata.BasisLevel.Set = true
	runtime.Dungeon.Metadata.BasisLevel.Value = 90
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
	runtime.Request.Difficulty = 4
	runtime.DropOwner = newRuntimeDungeonDropOwner()
	runtime.settlementPlayResultReceived = true
	runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 99
	if runtime.settlementCardRewardState != nil {
		t.Fatal("夹具已预先冻结翻牌计划")
	}
	kills := runtime.currentCardKillCounts()
	if kills != (currentDungeonCardKillCounts{Boss: 1}) {
		t.Fatalf("完整结算夹具的真实死亡计数=%+v want Boss:1", kills)
	}
	inputs, err := loadCurrentDungeonFreeCardGoldInputs(catalog.source, runtime)
	if err != nil {
		t.Fatal(err)
	}
	references, err := currentDungeonGoldReferences(catalog)
	if err != nil {
		t.Fatal(err)
	}
	scene, sceneOK := runtime.Session.Scene()
	if !sceneOK {
		t.Fatal("completed fixture has no scene")
	}
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	baseGold, err := inputs.amount(newCurrentDungeonDropLCG(visit.DropRNG), references[90])
	if err != nil {
		t.Fatal(err)
	}

	if err := service.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
		t.Fatal(err)
	}
	state := runtime.settlementCardRewardState
	if state == nil {
		t.Fatal("生产者没有冻结免费翻牌计划")
	}
	state.mu.Lock()
	frozen := cloneDungeonCardRewardPlan(state.plan)
	state.mu.Unlock()
	if frozen.Source != "current_pvf_clear_reward_generator_free_and_paid_rows" || frozen.CharacterID != "99" {
		t.Fatalf("冻结内容不是生产计划：source=%q character=%q", frozen.Source, frozen.CharacterID)
	}
	free := frozen.Sides[dungeonCardSideFree]
	if free.Cost != 0 {
		t.Fatalf("免费翻牌费用=%d want=0", free.Cost)
	}
	if free.Gold <= 0 {
		t.Fatalf("真实死亡和等级90没有产生非零免费金币：%+v kills=%+v", free, kills)
	}
	if len(free.Items) == 0 {
		baseGold += 100
	}
	if free.Gold != baseGold {
		t.Fatalf("final reward=%d want original resolved gold %d", free.Gold, baseGold)
	}
	if len(free.Items) != 1 || free.Items[0].ItemID <= 0 || free.Items[0].Count <= 0 {
		t.Fatalf("真实免费翻牌未保持单件合法奖励：%+v", free.Items)
	}

	// A second freeze must be a pure state hit. Change every producer input
	// available here, including making the archive unreadable, and ensure the
	// exact generated plan and its free row remain unchanged.
	runtime.Character.Level = 84
	runtime.entryCharacterLevel = 84
	runtime.Dungeon.Metadata.BasisLevel.Value = 84
	runtime.Request.Difficulty = 0
	runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 0
	runtime.Room.mu.Lock()
	runtime.Room.cardGoldKills.Normal++
	runtime.Room.mu.Unlock()
	catalog.source = bridgePVFSource{}
	if err := service.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
		t.Fatalf("重复冻结重新读取来源或重算计划：%v", err)
	}
	state.mu.Lock()
	unchanged := reflect.DeepEqual(frozen, state.plan)
	state.mu.Unlock()
	if runtime.settlementCardRewardState != state || !unchanged {
		t.Fatal("重复冻结改写了本局免费翻牌费用或奖励计划")
	}

	// Item notification projection still needs the real resource. The frozen
	// reward itself no longer consults it.
	catalog.source = archive
	runtime.settlementCardLayoutSent = true
	runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
	connection.write.Reset()
	if err := service.selectCurrentDungeonCardLocked(
		session, runtime, dungeonCardSideFree, 0, "真实PVF免费翻牌生产计划发奖",
	); err != nil {
		t.Fatal(err)
	}
	selectedMember, delivered, busy := state.deliveryStatus(dungeonCardSideFree)
	if !runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] ||
		!runtime.settlementCardSideSelectionSent[dungeonCardSideFree] ||
		!runtime.settlementCardSideRewardCommitted[dungeonCardSideFree] ||
		selectedMember != 0 || !delivered || busy {
		t.Fatal("免费翻牌选择或生产计划发奖没有完成")
	}
	grantedCharacter, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取发奖角色：found=%t err=%v", found, err)
	}
	if got, want := grantedCharacter.Stats["gold"], int64(100)+free.Gold; got != want {
		t.Fatalf("免费翻牌金币=%d want=%d (钱包100 + frozenFree.Gold %d)", got, want, free.Gold)
	}
	grantedBag, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取发奖库存：found=%t err=%v", found, err)
	}
	wantItems := map[int64]int64{}
	for _, item := range free.Items {
		wantItems[item.ItemID] += item.Count
	}
	gotItems := realPVFFreeCardItemCountsForTest(grantedBag.Slots)
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Fatalf("免费翻牌物品入包=%+v want=%+v slots=%+v", gotItems, wantItems, grantedBag.Slots)
	}

	connection.write.Reset()
	if err := service.selectCurrentDungeonCardLocked(
		session, runtime, dungeonCardSideFree, 0, "真实PVF免费翻牌重复请求",
	); err != nil {
		t.Fatal(err)
	}
	replayedCharacter, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取重复请求角色：found=%t err=%v", found, err)
	}
	replayedBag, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("读取重复请求库存：found=%t err=%v", found, err)
	}
	replayedMember, replayedDelivered, replayedBusy := state.deliveryStatus(dungeonCardSideFree)
	if !reflect.DeepEqual(grantedCharacter, replayedCharacter) ||
		!reflect.DeepEqual(grantedBag, replayedBag) ||
		replayedMember != selectedMember || !replayedDelivered || replayedBusy {
		t.Fatal("重复免费翻牌请求改变了资产或再次授权发奖")
	}
	t.Logf("real PVF free card: kills=%+v level=90 difficulty=4 rank=99 gold=%d cost=%d items=%d wallet_after=%d",
		kills, free.Gold, free.Cost, len(free.Items), grantedCharacter.Stats["gold"])
}

func realPVFFreeCardItemCountsForTest(slots map[string]dnfrepo.ItemStack) map[int64]int64 {
	counts := make(map[int64]int64)
	for _, stack := range slots {
		if stack.ItemID > 0 && stack.Count > 0 {
			counts[stack.ItemID] += stack.Count
		}
	}
	return counts
}
