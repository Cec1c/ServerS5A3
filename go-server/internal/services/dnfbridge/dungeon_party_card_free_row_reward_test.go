package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住运维实测缺陷「翻牌界面异常 能看到翻但是大家都是 X」里**纯下游的那一半**：
//
//	已翻开的牌位在 op71 里必须带上**它自己那份内容**；改动前只有付费行带内容，
//	免费行永远 `count=0` ⇒ 客户端画出空牌面（蓝色 X）。
//	见 dungeon_party_card_free_row_reward.go 文件头（含现场逐槽字节）。

// cardFreeRowRewardRuntime 造一个"计划里免费 429 金 / 付费 466 金"的最小运行时。
func cardFreeRowRewardRuntime(t *testing.T) *runtimeDungeonState {
	t.Helper()
	state, err := newDungeonCardState(dungeonCardRewardPlan{
		ID:          "free-row-plan",
		CharacterID: "100",
		Source:      "free_row_reward_test",
		Sides: [dungeonCardSideCount]dungeonCardRewardBundle{
			{Gold: 429},
			{Gold: 466},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeDungeonState{
		Dungeon:                   worldmap.Dungeon{ID: 310},
		settlementCardRewardState: state,
	}
}

// The next flipper's own acknowledgement must retain the earlier peer's
// complete free-card face, not just its owner label.
func TestCardOwnAckRetainsPreviouslyFlippedPeerFreeRewards(t *testing.T) {
	runtime := cardFreeRowRewardRuntime(t)
	runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
	runtime.settlementCardSideMember[dungeonCardSideFree] = 1
	runtime.partyInstance = &dungeonPartyInstance{}
	runtime.partyInstance.recordDungeonPartyCardFlip(dungeonCardSideFree, 0, 99, 0, 0xff,
		[]currentDungeonOp71RewardTuple{{ValueA: 0, ValueB: 7}, {ValueA: 14003, ValueB: 1}})
	slots, _ := currentDungeonSelectedCardSlots(runtime, 1)
	if slots[0].StateA != 0 || len(slots[0].Rewards) != 2 || slots[0].Rewards[1].ValueA != 14003 {
		t.Fatalf("peer free card lost its item in own acknowledgement: %+v", slots[0])
	}
	if slots[1].StateA != 1 || len(slots[1].Rewards) != 1 || slots[1].Rewards[0].ValueB != 429 {
		t.Fatalf("local card changed: %+v", slots[1])
	}
	runtime.partyInstance.recordDungeonPartyCardFlip(dungeonCardSidePaid, 0, 99, 0xff, 0,
		[]currentDungeonOp71RewardTuple{{ValueA: 33622, ValueB: 1}})
	slots, _ = currentDungeonSelectedCardSlots(runtime, 1)
	if slots[0].StateB != 0 || len(slots[0].Rewards) != 1 || slots[0].Rewards[0].ValueA != 33622 {
		t.Fatalf("paid face did not replace earlier free face: %+v", slots[0])
	}
}

// TestCardFreeRowRewardIsCarriedOnTheFlippedCard 是修复档的主判据：
// 免费行翻开的那个牌位必须带 `(0, 免费金币)` 这一个元组（客户端据此画牌面），
// 付费行翻开的牌位仍然带付费内容，两行同牌位时**付费优先**（厂商行为不变）。
func TestCardFreeRowRewardIsCarriedOnTheFlippedCard(t *testing.T) {
	t.Setenv(dungeonPartyCardFreeRowRewardEnv, "")

	// 只有免费行被翻开：牌位 1。
	runtime := cardFreeRowRewardRuntime(t)
	runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
	runtime.settlementCardSideMember[dungeonCardSideFree] = 1
	slots, tupleCount := currentDungeonSelectedCardSlots(runtime, 1)
	if slots[1].StateA != 1 || slots[1].StateB != 0xff {
		t.Fatalf("free row slot1=%+v want StateA=1 StateB=0xff", slots[1])
	}
	if len(slots[1].Rewards) != 1 ||
		slots[1].Rewards[0].ValueA != 0 || slots[1].Rewards[0].ValueB != 429 {
		t.Fatalf("free row slot1 rewards=%+v want one (0,429) gold tuple (the card face the client draws)",
			slots[1].Rewards)
	}
	if tupleCount != 1 {
		t.Fatalf("reward tuple count=%d want 1", tupleCount)
	}
	body, err := buildCurrentDungeonOp71SuccessBody(slots)
	if err != nil {
		t.Fatal(err)
	}
	// 33 字节 = 全空；41 字节 = 多一个 8 字节元组。免费行那一张牌必须让帧变成 41。
	if len(body) != 41 {
		t.Fatalf("op71 body len=%d want 41 (the flipped free row now carries its own face) body=%x",
			len(body), body)
	}

	// 同一牌位付费行也翻了 ⇒ 付费内容胜出（厂商口径，逐字节与改动前相同）。
	paid := cardFreeRowRewardRuntime(t)
	paid.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
	paid.settlementCardSideMember[dungeonCardSideFree] = 1
	paid.settlementCardSideSelectionKnown[dungeonCardSidePaid] = true
	paid.settlementCardSideMember[dungeonCardSidePaid] = 1
	paidSlots, _ := currentDungeonSelectedCardSlots(paid, 1)
	if len(paidSlots[1].Rewards) != 1 || paidSlots[1].Rewards[0].ValueB != 466 {
		t.Fatalf("paid row must win on the same slot: rewards=%+v want (0,466)", paidSlots[1].Rewards)
	}

	// 两个人各翻一张（免费行牌位 0、付费行牌位 1）⇒ 两张牌各自带自己的内容。
	both := cardFreeRowRewardRuntime(t)
	both.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
	both.settlementCardSideMember[dungeonCardSideFree] = 0
	both.settlementCardSideSelectionKnown[dungeonCardSidePaid] = true
	both.settlementCardSideMember[dungeonCardSidePaid] = 1
	bothSlots, bothCount := currentDungeonSelectedCardSlots(both, 0)
	if len(bothSlots[0].Rewards) != 1 || bothSlots[0].Rewards[0].ValueB != 429 {
		t.Fatalf("slot0 free face=%+v want (0,429)", bothSlots[0].Rewards)
	}
	if len(bothSlots[1].Rewards) != 1 || bothSlots[1].Rewards[0].ValueB != 466 {
		t.Fatalf("slot1 paid face=%+v want (0,466)", bothSlots[1].Rewards)
	}
	if bothCount != 2 {
		t.Fatalf("two flipped cards must carry two tuples, got %d", bothCount)
	}
}

// TestCardFreeRowRewardRollbackIsByteIdentical 钉住回退档：免费行永远 `count=0`，
// 即运维截图里那三张 X 的字节（逐字节等于改动前）。
func TestCardFreeRowRewardRollbackIsByteIdentical(t *testing.T) {
	build := func(t *testing.T, switchValue string) []byte {
		t.Helper()
		t.Setenv(dungeonPartyCardFreeRowRewardEnv, switchValue)
		runtime := cardFreeRowRewardRuntime(t)
		runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
		runtime.settlementCardSideMember[dungeonCardSideFree] = 1
		slots, _ := currentDungeonSelectedCardSlots(runtime, 1)
		if switchValue == "legacy" && len(slots[1].Rewards) != 0 {
			t.Fatalf("legacy mode gave the free row a face: %+v", slots[1].Rewards)
		}
		body, err := buildCurrentDungeonOp71SuccessBody(slots)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	fixed := build(t, "")
	legacy := build(t, "legacy")
	if len(legacy) != 33 {
		t.Fatalf("legacy op71 body len=%d want 33 (empty card face = today's bytes) body=%x",
			len(legacy), legacy)
	}
	if string(fixed) == string(legacy) {
		t.Fatalf("the switch does not gate the free-row face: both bodies=%x", fixed)
	}
	// 回退字面量口径（默认/非法值 = 修复档；off 系列 = 回退档）。
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "on", want: true},
		{value: "vendor", want: true},
		{value: "bogus", want: true},
		{value: "legacy", want: false},
		{value: " LEGACY ", want: false},
		{value: "off", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "no", want: false},
	} {
		t.Setenv(dungeonPartyCardFreeRowRewardEnv, test.value)
		if got := dungeonPartyCardFreeRowRewardEnabled(); got != test.want {
			t.Fatalf("read(%q)=%t want %t", test.value, got, test.want)
		}
	}
}

// TestCardFreeRowRewardReachesTheWireAndThePartner 端到端：走真实入口
// `selectCurrentDungeonCardLocked`，免费行翻开后
//   - 自己那一帧必须带内容（41 字节，不再 33）；
//   - 队友收到的那一帧同样带内容（账本规则：付费行未知时用免费行内容）。
func TestCardFreeRowRewardReachesTheWireAndThePartner(t *testing.T) {
	run := func(t *testing.T, switchValue string) (ownLen, peerLen int, log string) {
		t.Helper()
		t.Setenv(dungeonPartyCardFreeRowRewardEnv, switchValue)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 41, 464)
		service.packetLog = logger
		leader.dungeon.runtime = leaderRuntime
		follower.dungeon.runtime = followerRuntime
		if service.adoptPartyDungeonInstance(leader, leaderRuntime, scene) == nil {
			t.Fatal("leader did not adopt the shared instance")
		}
		if service.adoptPartyDungeonInstance(follower, followerRuntime, scene) == nil {
			t.Fatal("follower did not adopt the shared instance")
		}
		freezeDungeonCardStateForTest(t, leaderRuntime, "401")
		freezeDungeonCardStateForTest(t, followerRuntime, "402")
		for _, runtime := range []*runtimeDungeonState{leaderRuntime, followerRuntime} {
			runtime.settlementCardLayoutSent = true
			runtime.settlementClearRewardSent = true
			runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
		}
		leaderConn := leader.conn.(*bufferConn)
		followerConn := follower.conn.(*bufferConn)
		leaderConn.write.Reset()
		followerConn.write.Reset()

		// 队友（队伍槽位 1）翻免费行牌位 1。
		follower.dungeon.mu.Lock()
		flipErr := service.selectCurrentDungeonCardLocked(
			follower, followerRuntime, dungeonCardSideFree, 1, "test_free_row_reward",
		)
		follower.dungeon.mu.Unlock()
		if flipErr != nil {
			t.Fatalf("follower free flip: %v", flipErr)
		}
		own, _ := splitGameServerUpperPacket(t, followerConn.write.Bytes())
		peer, _ := splitGameServerUpperPacket(t, leaderConn.write.Bytes())
		ownLen, peerLen = len(own.Body), len(peer.Body)
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, readErr := os.ReadFile(logPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return ownLen, peerLen, string(logged)
	}

	ownLen, peerLen, log := run(t, "")
	if ownLen != 41 {
		t.Fatalf("the flipper's own frame len=%d want 41 (free row now carries its face)\n%s", ownLen, log)
	}
	if peerLen != 41 {
		t.Fatalf("the partner's frame len=%d want 41 (ledger must carry the free-row face)\n%s", peerLen, log)
	}
	for _, want := range []string{
		"slot_reward_tuple_count=1",
		"slot_reward_tuples=0=",
		"free_row_reward_mode=on",
		"free_row_reward_env=" + dungeonPartyCardFreeRowRewardEnv,
		"kind=game-dungeon-card-flip-party-broadcast",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("judgement fields are missing %q\n%s", want, log)
		}
	}

	legacyOwn, legacyPeer, legacyLog := run(t, "legacy")
	if legacyOwn != 33 || legacyPeer != 33 {
		t.Fatalf("legacy frames=%d/%d want 33/33 (empty card faces = today's bytes)\n%s",
			legacyOwn, legacyPeer, legacyLog)
	}
	if !strings.Contains(legacyLog, "slot_reward_tuple_count=0") ||
		!strings.Contains(legacyLog, "slot_reward_tuples=-") {
		t.Fatalf("legacy mode must report an empty card face\n%s", legacyLog)
	}
}
