package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归（v106 已发布 Script.pvf 的 316 魔界裂缝/particular hell）：
// 断言对象全部取自真档，而不是手写 fixture —— Hell.dgn 的 [required item]
// 基础数量、etc/particularhellparty.etc 的 [required item weight]，再让真实桥侧
// 准入/扣费入口（validateCurrentRiftEntry / consumeCurrentRiftEntry）在真档
// 免费票表下走出 2/4/6 与第 4 次拒绝。
//
// 运行方式（必须显式给路径，否则 skip）：
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE='E:\90\client-v106-single-luke-20261016\client-release\patch\client\Script.pvf'
//	go test -count=1 -run Round6ParticularHell -v ./internal/services/dnfbridge
const (
	// 真档 Hell.dgn [required item] 的基础数量（用户现场第一次入场显示的 2 个石头）。
	round6ParticularHellTicketBaseCount = 2
	// 真档 etc/particularhellparty.etc [required item weight]。
	round6ParticularHellWeight = 1.0
)

type round6ParticularHellFixture struct {
	service       *Service
	repos         dnfrepo.Group
	dungeon       worldmap.Dungeon
	base          []dnfdungeon.EntryItemRequirement
	freePassCount int
	slot          string
}

// round6OpenParticularHellRealPVF 打开真档 316，并给角色 19 放一份「只有石头、
// 没有免费票」的主背包：真档 45 条 [particular hell free pass item] 一组都付不起，
// 于是真正被扣的是 Hell.dgn 的普通票。
func round6OpenParticularHellRealPVF(t *testing.T, ticketCount int64) round6ParticularHellFixture {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real world map: %v", err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatalf("resolve real world map: %v", err)
	}
	dungeon, found := table.FindDungeon(currentRiftDungeonID)
	if !found {
		t.Fatalf("real dungeon %d missing", currentRiftDungeonID)
	}
	area, found := table.FindAreaByDungeon(currentRiftDungeonID)
	if !found {
		t.Fatalf("real area for %d missing", currentRiftDungeonID)
	}
	base := currentTournamentEntryItems(dungeon)
	if len(base) == 0 {
		t.Fatalf("real dungeon %d has no [required item]", currentRiftDungeonID)
	}
	// 只有 ConsumeOnEntry 的条目会被真档扣费路径检查/扣除；把石头放主背包第一格。
	slot := "0:20"
	slots := map[string]dnfrepo.ItemStack{}
	for _, item := range base {
		if !item.ConsumeOnEntry {
			continue
		}
		slots[slot] = dnfrepo.ItemStack{ItemID: item.ItemID, Count: ticketCount}
		break
	}
	if len(slots) == 0 {
		t.Fatalf("real dungeon %d has no consuming [required item]: %+v", currentRiftDungeonID, base)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "test-account",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		initialEquipmentArchive: archive,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	return round6ParticularHellFixture{
		service: service, repos: repos, dungeon: dungeon, base: base,
		freePassCount: len(area.ParticularHellFreePassItems), slot: slot,
	}
}

func round6ParticularHellTicketCount(t *testing.T, repos dnfrepo.Group, slot string) int64 {
	t.Helper()
	bag, found, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return bag.Slots[slot].Count
}

// TestRound6ParticularHellRealTicketBaseAndWeight 钉住真档两个输入数字：
// Hell.dgn [required item] 基础数量 = 2、etc/particularhellparty.etc weight = 1，
// 以及由它们推出的 2/4/6 与「普通票是免费票之后的最后一组」次序。
func TestRound6ParticularHellRealTicketBaseAndWeight(t *testing.T) {
	fixture := round6OpenParticularHellRealPVF(t, 20)
	service := fixture.service

	consuming := make([]dnfdungeon.EntryItemRequirement, 0, len(fixture.base))
	for _, item := range fixture.base {
		if item.ConsumeOnEntry {
			consuming = append(consuming, item)
		}
	}
	if len(consuming) != 1 {
		t.Fatalf("real 316 consuming [required item] count=%d want 1: %+v", len(consuming), fixture.base)
	}
	ticket := consuming[0]
	if ticket.Count != round6ParticularHellTicketBaseCount {
		t.Fatalf("real 316 [required item] base count=%d want %d (item=%d)", ticket.Count, round6ParticularHellTicketBaseCount, ticket.ItemID)
	}
	if ticket.ItemID <= 0 {
		t.Fatalf("real 316 [required item] id=%d", ticket.ItemID)
	}

	weight, err := service.currentRiftRequiredItemWeight()
	if err != nil {
		t.Fatalf("real particularhellparty weight: %v", err)
	}
	if weight != round6ParticularHellWeight {
		t.Fatalf("real [required item weight]=%v want %v", weight, round6ParticularHellWeight)
	}

	if currentRiftDailyLimit != 3 {
		t.Fatalf("daily limit=%d want 3", currentRiftDailyLimit)
	}
	if !service.currentRiftEntryEnabled(currentRiftDungeonID) {
		t.Fatal("official-24.9 316 entry must be enabled")
	}
	if service.currentRiftEntryEnabled(5100) {
		t.Fatal("the rift rule must not leak to another dungeon")
	}
	otherProfile := &Service{options: options{currentClientVersion: "official-24.7"}}
	if otherProfile.currentRiftEntryEnabled(currentRiftDungeonID) {
		t.Fatal("the rift rule must not leak to another protocol profile")
	}

	// 由真档数字推出的费用序列，以及免费票/普通票的替代次序。
	for used := int64(0); used < 3; used++ {
		requirements, err := dnfdungeon.DailyEntryRequirements(fixture.base, used, weight)
		if err != nil {
			t.Fatalf("requirements used=%d: %v", used, err)
		}
		want := int64(float64(ticket.Count) * math.Pow(float64(used+1), weight))
		if requirements[0].Count != want || want != 2*(used+1) {
			t.Fatalf("used=%d count=%d want=%d (2/4/6 序列)", used, requirements[0].Count, want)
		}
	}
	passes, err := service.currentRiftFreePasses(currentRiftDungeonID)
	if err != nil {
		t.Fatalf("real particular hell free passes: %v", err)
	}
	if len(passes) != fixture.freePassCount {
		t.Fatalf("free passes=%d want %d", len(passes), fixture.freePassCount)
	}
	alternatives, err := dnfdungeon.DailyEntryAlternatives(fixture.base, passes, 1, weight)
	if err != nil {
		t.Fatalf("alternatives: %v", err)
	}
	if len(alternatives) != len(passes)+1 {
		t.Fatalf("alternatives=%d want %d", len(alternatives), len(passes)+1)
	}
	last := alternatives[len(alternatives)-1]
	if len(last) != 1 || last[0].ItemID != ticket.ItemID || last[0].Count != 4 {
		t.Fatalf("last alternative=%+v want the real ordinary ticket %d x4", last, ticket.ItemID)
	}
	t.Logf("real 316 area dungeon=%d ticket=%d base=%d weight=%v freePasses=%d → 2/4/6, 4th refused by limit %d",
		fixture.dungeon.ID, ticket.ItemID, ticket.Count, weight, len(passes), currentRiftDailyLimit)
}

// TestRound6ParticularHellRealCostProgressionAndFourthRefusal 用真档走真实准入/扣费
// 入口：2/4/6 三次真扣、每次 830 计数上行，第 4 次在被拒（limit，不是缺票）且资产不变。
func TestRound6ParticularHellRealCostProgressionAndFourthRefusal(t *testing.T) {
	const start = int64(20) // 2+4+6=12 之外还留够第 4 次所需的 8，保证拒绝来自次数而非缺票。
	fixture := round6OpenParticularHellRealPVF(t, start)
	service, repos, slot := fixture.service, fixture.repos, fixture.slot
	// 只放石头、不放免费票：真档 45 条免费票一组都付不起时才扣普通票。
	if got := round6ParticularHellTicketCount(t, repos, slot); got != start {
		t.Fatalf("fixture ticket count=%d want %d", got, start)
	}

	ctx := context.Background()
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountID: "test-account"}
	consumed := int64(0)
	for i := int64(1); i <= 3; i++ {
		character, _, err := repos.Character.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if err := service.validateCurrentRiftEntry(ctx, character, fixture.dungeon, session); err != nil {
			t.Fatalf("real preflight %d: %v", i, err)
		}
		runtime := &runtimeDungeonState{Dungeon: fixture.dungeon}
		if err := service.consumeCurrentRiftEntry(session, runtime); err != nil {
			t.Fatalf("real commit %d: %v", i, err)
		}
		wantConsumed := 2 * i
		consumed += wantConsumed
		if got := start - round6ParticularHellTicketCount(t, repos, slot); got != consumed {
			t.Fatalf("entry %d: debited=%d want=%d (2/4/6)", i, got, consumed)
		}
		used, err := dnfdungeon.DailyEntryUsed(runtime.Character, currentRiftDungeonID, service.gameplayNow())
		if err != nil || used != i {
			t.Fatalf("entry %d: used=%d err=%v", i, used, err)
		}
		if i == 1 {
			assertRound6ParticularHellCounterFrame(t, session.conn.(*bufferConn), i)
		}
	}

	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.validateCurrentRiftEntry(ctx, character, fixture.dungeon, session); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("4th preflight err=%v want %v", err, dnfdungeon.ErrDailyEntryLimit)
	}
	before := round6ParticularHellTicketCount(t, repos, slot)
	runtime := &runtimeDungeonState{Dungeon: fixture.dungeon}
	if err := service.consumeCurrentRiftEntry(session, runtime); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("4th commit err=%v want %v", err, dnfdungeon.ErrDailyEntryLimit)
	}
	if after := round6ParticularHellTicketCount(t, repos, slot); after != before {
		t.Fatalf("4th refusal mutated tickets: before=%d after=%d", before, after)
	}
	used, err := dnfdungeon.DailyEntryUsed(character, currentRiftDungeonID, service.gameplayNow())
	if err != nil || used != 3 {
		t.Fatalf("after refusal used=%d err=%v want 3", used, err)
	}
	if before < 4*round6ParticularHellTicketBaseCount {
		t.Fatalf("refusal came from shortage, not the daily limit: remaining=%d", before)
	}
	t.Logf("real 316 ticket=%d: debits 2/4/6 (total %d), counter 830 confirmed, 4th refused with %d tickets left",
		fixture.base[0].ItemID, consumed, before)
}

// assertRound6ParticularHellCounterFrame 在真实出站流里找 830（当前副本 used 计数）
// 并核对 (dungeonID, used)。只读已写入 bufferConn 的帧，不改产品路径。
func assertRound6ParticularHellCounterFrame(t *testing.T, conn *bufferConn, wantUsed int64) {
	t.Helper()
	frames := 0
	remaining := conn.write.Bytes()
	for len(remaining) > 0 {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		if packet.Header.MsgID == currentParticularHellDungeonMsgID {
			frames++
			if len(packet.Body) != 8 {
				t.Fatalf("830 body len=%d want 8", len(packet.Body))
			}
			if got := binary.LittleEndian.Uint32(packet.Body); got != uint32(currentRiftDungeonID) {
				t.Fatalf("830 dungeon=%d want %d", got, currentRiftDungeonID)
			}
			if got := binary.LittleEndian.Uint32(packet.Body[4:]); got != uint32(wantUsed) {
				t.Fatalf("830 used=%d want %d", got, wantUsed)
			}
		}
		remaining = rest
	}
	if frames != 1 {
		t.Fatalf("830 frames=%d want 1", frames)
	}
}
