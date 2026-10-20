package dnfbridge

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ── 锦标赛翻牌交付（现场 bug 回归）──────────────────────────────────────────────
//
// 现场（2026-09-28 线上，dungeon 121 青龙大会 / 123-124 魂·大会）：第 4 轮之后客户端出
// 翻牌（宝箱）界面，玩家选不动、奖励发不出来，只能自己走出副本。日志里只有：
//
//	tournament-reward-delivered card_type=0 is_gold=true gold=7280          ← 金币卡 OK
//	tournament-reward-selection-blocked card_type=1 card_index=0 card_count=2
//	    reason=reward_delivery_failed
//	    error=dungeon card reward plan is invalid: dungeon reward is invalid
//
// 根因：`currentTournamentRewardBundle` 给道具卡只填 `ItemID/Count/Stackable`
// （`SlotStart=SlotEnd=0`），调用点又传 `mainSlotCount = 0`，于是 owner 的
// `!CardRewardSlotRangeUsable(0, 0) && cmd.MainSlots == 0` 把**每一张道具卡**都拒了。
//
// 这三组用例钉住四件事：
//  1. 道具卡用**真实 PVF 定义**（既有 `pvfDungeonDropCatalog.ResolveItem`）给出的落点段
//     发放，落点真的落在那个段里；
//  2. 金币卡路径逐字节不变（不碰 PVF、不碰背包）；
//  3. **solicited（客户端自己发来的 2 字节选奖）的应答形状（修复档，默认）**：
//     先一条 458 选中投影（7 字节，字面量比对），选奖未完成时再补一条 9 字节 457 选奖态；
//     选完则只有投影。回退档（`DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=state`）与
//     **unsolicited**（3s 兜底自动选奖）路径则一个字节的 458 都不发。
//  4. 失败时的具名原因必须带 item/规则/解析证据（下一次线上可直接定位）。

// newTournamentRewardPVFCatalogForTest 造一份只含用例需要的那几个道具的运行时 PVF 道具目录。
// 走的是与生产**同一条** `newPVFDungeonDropCatalog`（不新开 reader、不手搓定义）。
func newTournamentRewardPVFCatalogForTest(t *testing.T, documents map[string]string) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "",
		"equipment/equipment.lst": "",
	}
	for path, text := range documents {
		source[path] = text
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build tournament reward PVF catalog: %v", err)
	}
	return catalog
}

// newTournamentRewardDeliveryFixture 造"vendor 档 + 冠军 + 奖励态在位"的现场夹具
// （与 `dungeon_tournament_closing_test.go` 的收尾链路同一套 fixture）。
func newTournamentRewardDeliveryFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture has no repository group")
	}
	session.dungeon.mu.Lock()
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = tournamentRewardStateForTest(t, false)
	session.dungeon.mu.Unlock()
	return service, session, runtime, repositories
}

// tournamentSelectionHandshakePackets 只挑出选奖握手的下行（457/458），并按发出顺序返回。
//
// 落奖/通关路径上本来还会有别的下行（op14 背包刷新、op115、op574…），它们与本用例的
// 判据无关；这里只钉握手的**条数与逐字节形状**（顺序断言由调用方用返回的切片做）。
func tournamentSelectionHandshakePackets(t *testing.T, session *gameSession) []struct {
	msgID uint16
	body  []byte
} {
	t.Helper()
	all := drainUpperPackets(t, session.conn.(*bufferConn))
	out := make([]struct {
		msgID uint16
		body  []byte
	}, 0, 2)
	for _, packet := range all {
		switch packet.msgID {
		case a21CmdTournamentRewardSelect, a21CmdTournamentRewardSelectState:
			out = append(out, packet)
		}
	}
	return out
}

// assertTournamentRewardSolicitedReply 钉"客户端自己发来的 2 字节选奖（solicited）"在
// **修复档**（默认 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=select`）下的完整应答形状：
//
//	① **恰好一条** 458 选中投影，且**逐字面量**比对（`1 | 2 | s s | 2 | s s`；
//	   `s` = 拿走这张卡的队伍槽位，没人拿 = 0xFF）—— 不用生产构造器算期望值，
//	   免得"用真源验真源"变成空断言；
//	② 选奖未完成 ⇒ 再补**恰好一条** 9 字节 457 选奖态（族内 0.1.8 的
//	   "refreshing state 6 between the two visual groups"）；选完 ⇒ 一条都不补。
//
// wantProjectionHex 是 458 体的十六进制字面量（例如 `010200ff02ffff`）。
func assertTournamentRewardSolicitedReply(
	t *testing.T,
	session *gameSession,
	wantProjectionHex string,
	wantStatePackets int,
) {
	t.Helper()
	packets := tournamentSelectionHandshakePackets(t, session)
	if len(packets) != 1+wantStatePackets {
		t.Fatalf("selection handshake packets = %d want %d（1 条 458 投影 + %d 条 457 选奖态）: %+v",
			len(packets), 1+wantStatePackets, wantStatePackets, packets)
	}
	if packets[0].msgID != a21CmdTournamentRewardSelect {
		t.Fatalf("first handshake packet msg_id = %d want the 458 selection projection (%d)",
			packets[0].msgID, a21CmdTournamentRewardSelect)
	}
	if got := hex.EncodeToString(packets[0].body); got != wantProjectionHex {
		t.Fatalf("458 selection projection = %s want %s（1 | 每组卡数 | 每张卡的队伍槽位，未拿 = ff）",
			got, wantProjectionHex)
	}
	for index := 1; index < len(packets); index++ {
		if packets[index].msgID != a21CmdTournamentRewardSelectState {
			t.Fatalf("handshake packet %d msg_id = %d want %d (457 state)",
				index, packets[index].msgID, a21CmdTournamentRewardSelectState)
		}
		if len(packets[index].body) != 9 || packets[index].body[0] != 1 {
			t.Fatalf("state body = %x want the 9-byte success + 2x4 slot projection", packets[index].body)
		}
	}
}

// assertTournamentRewardStateOnlyReply 钉**没有 458 的两条路径**：
//
//	① 回退档（`DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=state`）= 逐字节回到改动前；
//	② **unsolicited**（3s 兜底自动选奖，默认档下也一样）。
//
// 两条都必须：选奖握手下行里**没有** 458、也没有任何 7 字节体；未完成时恰好
// wantStatePackets 条 9 字节 457。整条下行流也一并检查（这两条路径下连
// `a21CmdTournamentRewardSelect` 这个 msg_id 都不允许出现）。
func assertTournamentRewardStateOnlyReply(t *testing.T, session *gameSession, wantStatePackets int) {
	t.Helper()
	packets := drainUpperPackets(t, session.conn.(*bufferConn))
	statePackets := 0
	for _, packet := range packets {
		if packet.msgID == a21CmdTournamentRewardSelect {
			t.Fatalf("the 458 selection projection must not be sent on this path (unsolicited timer auto-select / rollback mode): body=%x", packet.body)
		}
		if len(packet.body) == 7 {
			t.Fatalf("a 7-byte downlink body appeared on a path that must never send 458: msg_id=%d body=%x",
				packet.msgID, packet.body)
		}
		if packet.msgID != a21CmdTournamentRewardSelectState {
			continue
		}
		statePackets++
		if len(packet.body) != 9 || packet.body[0] != 1 {
			t.Fatalf("state body = %x want the 9-byte success + 2x4 slot projection", packet.body)
		}
	}
	if statePackets != wantStatePackets {
		t.Fatalf("state packets = %d want %d", statePackets, wantStatePackets)
	}
}

// setTournamentRewardAckRollbackForTest 把夹具切成**回退档**。
//
// ⚠️ 这些用例的 Service 是**结构体字面量**（`newBackToVillageRuntime` 直接写
// `options{...}`），**不走 `optionsFromConfig`**，所以环境变量在夹具里不生效 ——
// 必须直接写 options 字段。环境变量本身的解析口径由
// `TestDungeonTournamentRewardAckSwitchDefaultsToSelect` 钉住（与生产同一条
// `readDungeonTournamentRewardAckMode`）。
func setTournamentRewardAckRollbackForTest(t *testing.T, service *Service) {
	t.Helper()
	t.Setenv(dungeonTournamentRewardAckEnvKey, dungeonTournamentRewardAckValueState)
	if got := readDungeonTournamentRewardAckMode(
		os.Getenv(dungeonTournamentRewardAckEnvKey)); got != dungeonTournamentRewardAckStateOnly {
		t.Fatalf("rollback env value normalized to %v want %v", got, dungeonTournamentRewardAckStateOnly)
	}
	service.options.dungeonTournamentRewardAck = dungeonTournamentRewardAckStateOnly
	if service.dungeonTournamentRewardAck().answersSolicitedSelection() {
		t.Fatal("rollback fixture must not answer a solicited pick with the 458 projection")
	}
}

// findTournamentRewardStack 在主背包 list 0 里找这次发放落到了哪一格。
func findTournamentRewardStack(t *testing.T, inventory dnfrepo.InventoryRecord, itemID int64) (int16, dnfrepo.ItemStack, bool) {
	t.Helper()
	for key, stack := range inventory.Slots {
		if stack.ItemID != itemID {
			continue
		}
		slotText, ok := strings.CutPrefix(key, "0:")
		if !ok {
			t.Fatalf("the reward must land in the main bag (list 0), got slot key %q", key)
		}
		slot, err := strconv.ParseInt(slotText, 10, 16)
		if err != nil {
			t.Fatalf("inventory slot key %q: %v", key, err)
		}
		return int16(slot), stack, true
	}
	return 0, dnfrepo.ItemStack{}, false
}

// (a) 道具卡：真实 PVF 定义给出 `[material]` → list 0 121..176，落奖后必须真的落在这一段里。
//
// 旧代码在这里**根本不会落库**：bundle 的 SlotStart/SlotEnd 是 0、mainSlots 也是 0，
// owner 直接以 `dungeon reward is invalid` 拒收，于是现场"翻牌选不动"。
func TestTournamentItemCardDeliversIntoResolvedPVFSlotRange(t *testing.T) {
	service, session, runtime, repositories := newTournamentRewardDeliveryFixture(t)
	service.pvfItemCatalog = newTournamentRewardPVFCatalogForTest(t, map[string]string{
		"stackable/stackable.lst":              "3329 `material/material_3329.stk`\n",
		"stackable/material/material_3329.stk": "[name] `Material`\n[stackable type] `[material]`\n[stack limit] 999\n",
	})
	session.conn.(*bufferConn).write.Reset()

	// 夹具四张卡：type0 = {金币 1234, 道具 3329×1}，type1 = {金币 1234, 道具 3329×2}
	// （`tournamentRewardStateForTest`）。{0, 1} = 第一排的道具卡。
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 1})
	if err != nil || !handled {
		t.Fatalf("item card select handled=%t err=%v", handled, err)
	}
	state := runtime.tournamentReward
	if state.Selected[0] != 1 {
		t.Fatalf("selection = %+v want group 0 = card 1（发不出去就不该被记成已选）", state.Selected)
	}
	inventory, found, loadErr := repositories.Inventory.Load(context.Background(), "99")
	if loadErr != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, loadErr)
	}
	slot, stack, ok := findTournamentRewardStack(t, inventory, 3329)
	if !ok {
		t.Fatalf("item 3329 was not delivered: slots=%+v", inventory.Slots)
	}
	if slot < currentPVFMainMaterialStart || slot > currentPVFMainMaterialEnd {
		t.Fatalf("item 3329 landed in slot %d, want the PVF [material] page %d..%d",
			slot, currentPVFMainMaterialStart, currentPVFMainMaterialEnd)
	}
	if stack.Count != 1 {
		t.Fatalf("delivered count = %d want 1", stack.Count)
	}
	if stack.Extra["pvf_path"] != "stackable/material/material_3329.stk" ||
		stack.Extra["item_kind"] != string(dungeonDropItemStackable) ||
		stack.Extra["stackable_type"] != "[material]" {
		t.Fatalf("delivered stack identity = %+v want the ordinary card path's identity keys", stack.Extra)
	}
	// 第一排第 1 张（下标 1）被本槽位（0）拿走 ⇒ `01 | 02 | ff 00 | 02 | ff ff`。
	assertTournamentRewardSolicitedReply(t, session, "0102ff0002ffff", 1)
}

// (b) 金币卡路径不变：只加金币、不碰背包、**不需要** PVF 道具目录
// （夹具里 `pvfItemCatalog` 是 nil，金币卡若多走一步解析就会失败）。
func TestTournamentGoldCardDeliveryKeepsGoldOnlyPath(t *testing.T) {
	service, session, runtime, repositories := newTournamentRewardDeliveryFixture(t)
	if service.pvfItemCatalog != nil {
		t.Fatal("fixture unexpectedly preloaded an item catalog")
	}
	before, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}

	// {0, 0} = 第一排的金币卡（1234）。
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("gold card select handled=%t err=%v", handled, err)
	}
	after, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("reload character: found=%t err=%v", found, err)
	}
	if after.Stats["gold"] != before.Stats["gold"]+1234 {
		t.Fatalf("gold = %d want %d（金币卡金额必须原样入账）", after.Stats["gold"], before.Stats["gold"]+1234)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if found && len(inventory.Slots) != 0 {
		t.Fatalf("a gold card must not touch the bag: %+v", inventory.Slots)
	}
	if runtime.tournamentReward.Selected[0] != 0 {
		t.Fatalf("gold selection = %+v want group 0 committed", runtime.tournamentReward.Selected)
	}
	// 第一排第 0 张被本槽位（0）拿走 ⇒ `01 | 02 | 00 ff | 02 | ff ff`。
	assertTournamentRewardSolicitedReply(t, session, "010200ff02ffff", 1)
}

// (c) solicited 选奖、落奖后选奖**未完成** ⇒ 458 选中投影（7 字节）+ 一条 9 字节 457 选奖态。
//
// 这两条合起来就是族内 0.1.8 `handleCurrentDungeonTournamentRewardSelect` 的形状
// （先回 selection，再在未完成时补 state）；也是本批修复的核心：
// 改动前只有 457，客户端的翻牌界面因此永远推进不了。
func TestTournamentRewardDeliveryResendsStatePacketWhenSelectionIncomplete(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	session.conn.(*bufferConn).write.Reset()

	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	state := runtime.tournamentReward
	if state.isSelectionComplete() {
		t.Fatal("fixture selection must still be incomplete after a single group")
	}
	if state.Selected[1] != tournamentUnselectedCard {
		t.Fatalf("group 1 = %d want unselected", state.Selected[1])
	}
	assertTournamentRewardSolicitedReply(t, session, "010200ff02ffff", 1)
}

// (c′) solicited 选奖、选奖**已完成** ⇒ **只有**那条 458 选中投影，不再补 457 选奖态
// （族内 0.1.8：`if (!currentDungeonTournamentRewardSelectionComplete(tournament))` 才补）。
//
// 夹具用 `completedAllRounds=false`（只有 group 0 启用），一次选择就选完；
// 两个组都停用/都选完时投影尾部只有 `00`（该排 0 张卡）。
func TestTournamentRewardDeliveryAnswersCompleteSelectionWithOnlyTheProjection(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	state, err := newTournamentRewardState([]tournamentCardReward{
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 1},
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 2},
	}, 1, 0, 200000, tournamentFinalRound, false)
	if err != nil {
		t.Fatalf("fixture reward state: %v", err)
	}
	session.dungeon.mu.Lock()
	runtime.tournamentReward = &state
	clearTournamentRoomForTest(t, runtime)
	session.dungeon.mu.Unlock()

	session.conn.(*bufferConn).write.Reset()
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	if !runtime.tournamentReward.isSelectionComplete() {
		t.Fatalf("selection = %+v want complete after the only enabled group", runtime.tournamentReward.Selected)
	}
	// `01 | 02 | 00 ff | 00`（第二组 `completedAllRounds=false` ⇒ 卡数 0）。
	assertTournamentRewardSolicitedReply(t, session, "010200ff00", 0)
}

// (c″) **unsolicited**（3s 兜底自动选奖）⇒ 一个字节的 458 都不发，只沿用改动前的
// 9 字节 457 应答（未受邀 458 之后真机客户端异常退出的实证见 dungeon_tournament_config.go）。
func TestTournamentRewardTimerAutoSelectNeverSendsTheProjection(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	session.conn.(*bufferConn).write.Reset()

	session.dungeon.mu.Lock()
	service.autoSelectCurrentTournamentRewardsLocked(
		session, runtime, tournamentRewardSelectionSourceTimer)
	session.dungeon.mu.Unlock()

	if !runtime.tournamentReward.isSelectionComplete() {
		t.Fatalf("timer auto-select = %+v want both groups delivered",
			runtime.tournamentReward.Selected)
	}
	// 第一次落奖时选奖还没完 ⇒ 一条 457 选奖态；第二次落奖时已选完 ⇒ 不再补。
	// 全程一条 458 都没有。
	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// (c′) **unsolicited 且客户端已打开翻牌界面**（先发来体长为 0 的 457 ⇒ latch 置位）：
//
// ⚠️ 2026-09-29 改判 —— 这条路径在**默认档下必须一个字节的 458 都不发**。
// 上一版把默认档设成"bodyless 457 也算受邀"，于是 3s 兜底自动选奖也回 458 选中投影，
// 结果线上 `conn_id=game-002721` 在最后一条 458 之后 **1.265 秒**客户端 RST
// （`error=read tcp ...: read: connection reset by peer`，t=2026-09-29T06:05:20.494985829Z）。
// 对照：没有收到 458 的 6 个会话全部存活。原始行与 4/4 局统计见
// `dungeon_tournament_reward_solicit_test.go` 的取证块。
//
// 想复现旧行为必须**显式**写危险档（`..._SOLICIT=state-request` 或
// `..._REWARD_ACK=select+timer`），那两条由
// `TestTournamentRewardSolicitStateRequestOptInReproducesTheCrashingTier` /
// `TestTournamentRewardAckSelectTimerOptInStillAnswersTheTimerPath` 钉住。
func TestTournamentRewardTimerAutoSelectStaysSilentWhenOnlyTheStateRequestWasSeen(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	session.conn.(*bufferConn).write.Reset()

	// 模拟客户端先发来体长为 0 的 457（= 翻牌选奖界面已打开）。
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelectState, nil)
	if err != nil || !handled {
		t.Fatalf("state request handled=%t err=%v", handled, err)
	}
	if !session.tournamentRewardStateRequested {
		t.Fatal("the bodyless 457 must set the reward-UI latch")
	}
	session.conn.(*bufferConn).write.Reset()

	session.dungeon.mu.Lock()
	service.autoSelectCurrentTournamentRewardsLocked(session, runtime, tournamentRewardSelectionSourceTimer)
	session.dungeon.mu.Unlock()

	if !runtime.tournamentReward.isSelectionComplete() {
		t.Fatalf("timer auto-select = %+v want both groups delivered", runtime.tournamentReward.Selected)
	}
	// 默认档（`REWARD_ACK` 零值 = select、`REWARD_SOLICIT` 零值 = legacy）+ latch
	// ⇒ 兜底自动选奖**不得**发出 458（发了会崩客户端）；只允许 9 字节 457 选奖态。
	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// (c‴) **solicited 但这一组已经被拿走**（3s 兜底自动选奖先替玩家发掉了）⇒ 仍然回 458 投影。
//
// 这是"卡在翻牌"最可能的现场：计时器 3s 一到就把两张卡落库（聊天栏于是有物品），
// 玩家随后点卡时服务端只记 `reward-selection-duplicate` 就返回，客户端永远收不到
// "这张卡已经被拿走"，翻牌界面停在原地 —— 本用例钉住这条出口必须回投影。
func TestTournamentRewardDuplicateSolicitedPickIsStillAnsweredWithTheProjection(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)

	session.dungeon.mu.Lock()
	service.autoSelectCurrentTournamentRewardsLocked(
		session, runtime, tournamentRewardSelectionSourceTimer)
	session.dungeon.mu.Unlock()
	session.conn.(*bufferConn).write.Reset()

	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("duplicate select handled=%t err=%v", handled, err)
	}
	if runtime.tournamentReward.Selected[0] != 0 || runtime.tournamentReward.Selected[1] != 0 {
		t.Fatalf("duplicate pick must not re-select: %+v", runtime.tournamentReward.Selected)
	}
	// 两排都已被队伍槽位 0 拿走 ⇒ `01 | 02 | 00 ff | 02 | 00 ff`；选奖已完成 ⇒ 不再补 457。
	assertTournamentRewardSolicitedReply(t, session, "010200ff0200ff", 0)
}

// (c⁗) **回退档**（`DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=state`）⇒ 逐字节回到改动前：
// solicited 选奖也只回一条 9 字节 457 选奖态，全程没有 458、没有 7 字节体。
func TestTournamentRewardAckRollbackModeKeepsTheStateOnlyStream(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	setTournamentRewardAckRollbackForTest(t, service)
	session.conn.(*bufferConn).write.Reset()

	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	if runtime.tournamentReward.Selected[0] != 0 {
		t.Fatalf("selection = %+v want group 0 committed", runtime.tournamentReward.Selected)
	}
	assertTournamentRewardStateOnlyReply(t, session, 1)
}

// (c⁵) 回退档 + 选奖已完成 ⇒ **什么都不发**（改动前的 `TestTournamentRewardDeliverySendsNothingOnceSelectionIsComplete`）。
func TestTournamentRewardAckRollbackModeSendsNothingOnceSelectionIsComplete(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	setTournamentRewardAckRollbackForTest(t, service)
	state, err := newTournamentRewardState([]tournamentCardReward{
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 1},
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 2},
	}, 1, 0, 200000, tournamentFinalRound, false)
	if err != nil {
		t.Fatalf("fixture reward state: %v", err)
	}
	session.dungeon.mu.Lock()
	runtime.tournamentReward = &state
	clearTournamentRoomForTest(t, runtime)
	session.dungeon.mu.Unlock()

	session.conn.(*bufferConn).write.Reset()
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	if !runtime.tournamentReward.isSelectionComplete() {
		t.Fatalf("selection = %+v want complete after the only enabled group", runtime.tournamentReward.Selected)
	}
	assertTournamentRewardStateOnlyReply(t, session, 0)
}

// (2) 失败绝不静默：装备卡带 StackCount=3 时由 owner 的具名校验收口，
// 日志里必须同时有 item / 命中的规则 / PVF 解析证据。
func TestTournamentItemCardFailureLogsNamedRuleAndResolutionEvidence(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	service.pvfItemCatalog = newTournamentRewardPVFCatalogForTest(t, map[string]string{
		"equipment/equipment.lst":         "3329 `weapon/test_sword.equ`\n",
		"equipment/weapon/test_sword.equ": "[name] `Test Sword`\n[equipment type] `[weapon]`\n[durability] 57\n",
	})
	state, err := newTournamentRewardState([]tournamentCardReward{
		{ItemID: 3329, StackCount: 3}, // 装备不可堆叠 ⇒ 数量必须为 1
		{IsGold: true, GoldAmount: 1234},
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 2},
	}, 1, 0, 200000, tournamentFinalRound, true)
	if err != nil {
		t.Fatalf("fixture reward state: %v", err)
	}
	session.dungeon.mu.Lock()
	runtime.tournamentReward = &state
	session.dungeon.mu.Unlock()

	logger, logPath := testTournamentRewardPacketLogger(t)
	service.packetLog = logger

	session.conn.(*bufferConn).write.Reset()
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	if runtime.tournamentReward.Selected[0] != tournamentUnselectedCard {
		t.Fatalf("a rejected delivery must roll the selection back: %+v", runtime.tournamentReward.Selected)
	}
	// 交付失败 ⇒ 这一组回滚成"还没人拿"，**只回一条**把该组标成 0xFF 的 458 选中投影，
	// 让客户端的翻牌界面留在可重选状态（改动前这里一条都不发：客户端只能干等）。
	// 选奖仍未完成，但改动前也只在这条"落奖成功"路径上补 457 ⇒ 这里不补。
	assertTournamentRewardSolicitedReply(t, session, "01"+"02"+"ffff"+"02"+"ffff", 0)
	logged := closeTournamentRewardPacketLogger(t, logger, logPath)
	for _, want := range []string{
		"kind=game-dungeon-tournament-reward-selection-blocked",
		"reason=reward_delivery_failed",
		"rule=non_stackable_card_item_count",
		"item=3329",
		"reward_evidence item=3329 count=3 pvf_resolved=true pvf_kind=equipment",
		"pvf_path=equipment/weapon/test_sword.equ",
		"slot_start=9 slot_end=64",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the failure log is missing %q; got:\n%s", want, logged)
		}
	}
}

// (2′) PVF 里查不到这个道具 ⇒ 具名拒绝（不猜段、不静默丢奖励），
// 日志里必须能看出"解析失败"而不是裸 `dungeon reward is invalid`。
func TestTournamentItemCardUnresolvedPVFLogsResolutionEvidence(t *testing.T) {
	service, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	service.pvfItemCatalog = newTournamentRewardPVFCatalogForTest(t, nil)

	logger, logPath := testTournamentRewardPacketLogger(t)
	service.packetLog = logger

	session.conn.(*bufferConn).write.Reset()
	handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{0, 1})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	if runtime.tournamentReward.Selected[0] != tournamentUnselectedCard {
		t.Fatalf("an undeliverable item card must roll the selection back: %+v", runtime.tournamentReward.Selected)
	}
	// 同上：失败 ⇒ 只回一条"这一组还没人拿"的 458 选中投影（第一排第 1 张）。
	assertTournamentRewardSolicitedReply(t, session, "01"+"02"+"ffff"+"02"+"ffff", 0)
	logged := closeTournamentRewardPacketLogger(t, logger, logPath)
	for _, want := range []string{
		"kind=game-dungeon-tournament-reward-selection-blocked",
		"reason=reward_delivery_failed",
		"PVF definition unresolved",
		"reward_evidence item=3329 count=1 pvf_resolved=false",
		"item_id=3329",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the failure log is missing %q; got:\n%s", want, logged)
		}
	}
}

// testTournamentRewardPacketLogger 把 service.packetLog 换成写临时文件的真 logger，
// 好让具名事件可以被 grep 断言（生产里落在 `runtime/log/packet_log.*.txt` 的
// `EVENT kind=…` 行）——与 `testCharacterOptionPacketLogger` 同一形状。
func testTournamentRewardPacketLogger(t *testing.T) (*packetLogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "packet_log.tournament-reward-delivery.txt")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatalf("open packet logger: %v", err)
	}
	if logger == nil {
		t.Fatal("packet logger was nil")
	}
	return logger, path
}

func closeTournamentRewardPacketLogger(t *testing.T, logger *packetLogger, path string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatalf("close packet logger: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read packet log: %v", err)
	}
	return string(data)
}
