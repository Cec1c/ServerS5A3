package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPartyBlackDiamondCardsFreezeOnceAndProjectIdenticalOwnerRewards(t *testing.T) {
	instance := &dungeonPartyInstance{members: map[uint16]time.Time{6: {}, 14: {}}}
	runtime := &runtimeDungeonState{partyInstance: instance}
	service := &Service{}
	session := &gameSession{selectedCharacterID: 6}
	session.party.state.Members = []alignedcmd.PartyMemberState{{UserID: 6}, {UserID: 14}}
	var calls int
	roll := func(owner uint16) (dungeonCardRewardBundle, error) {
		calls++
		if owner == 14 {
			return dungeonCardRewardBundle{}, nil
		} // ordinary teammate
		return dungeonCardRewardBundle{Gold: 12, Items: []dungeonCardItemReward{{ItemID: 700, Count: 1}}}, nil
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := service.freezePartyBlackDiamondCards(session, runtime, roll); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls != 2 {
		t.Fatalf("roll calls=%d want each member once", calls)
	}
	ownerReward := partyBlackDiamondCardForOwner(runtime, 6)
	if ownerReward.Gold != 12 || len(ownerReward.Items) != 1 || ownerReward.Items[0].ItemID != 700 {
		t.Fatalf("owner reward=%+v", ownerReward)
	}
	ownerReward.Items[0].ItemID = 999 // returned copies cannot corrupt the claim or peer view
	var own, peer [currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair
	projectPartyBlackDiamondCards(runtime, &own)
	projectPartyBlackDiamondCards(runtime, &peer)
	if !reflect.DeepEqual(own, peer) || len(own[0]) != 2 || own[0][1].Key != 700 || len(own[1]) != 0 {
		t.Fatalf("views own=%+v peer=%+v", own, peer)
	}
}

// 2026-10-02 (V22)：把"队友看到黑钻额外翻牌"的**线上自证判据**钉住。
// 这条链路此前没有任何日志，双开实测无法证明它跑过。现在：
//   - 真有黑钻牌被冻结 ⇒ 一条 `game-dungeon-black-diamond-cards-frozen`（含 owner:gold:items）；
//   - 投影发生 ⇒ `projectPartyBlackDiamondCards` 返回 slot:owner:rows 摘要（调用方记事件）；
//   - 全员普通 ⇒ **零日志**（不能刷噪音）。
func TestPartyBlackDiamondCardsFreezeAndProjectionAreObservable(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	instance := &dungeonPartyInstance{id: 42, members: map[uint16]time.Time{6: {}, 14: {}}}
	runtime := &runtimeDungeonState{partyInstance: instance}
	service := &Service{packetLog: logger}
	session := &gameSession{connID: "bd-card", selectedCharacterID: 6}
	session.party.state.Members = []alignedcmd.PartyMemberState{{UserID: 6}, {UserID: 14}}
	roll := func(owner uint16) (dungeonCardRewardBundle, error) {
		if owner == 6 { // 黑钻：有牌
			return dungeonCardRewardBundle{Gold: 1200, Items: []dungeonCardItemReward{{ItemID: 700, Count: 1}}}, nil
		}
		return dungeonCardRewardBundle{}, nil // 普通：无牌
	}
	if err := service.freezePartyBlackDiamondCards(session, runtime, roll); err != nil {
		t.Fatal(err)
	}
	var slots [currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair
	projected := projectPartyBlackDiamondCards(runtime, &slots)
	if len(projected) != 1 || !strings.HasPrefix(projected[0], "0:6:") {
		t.Fatalf("projection summary=%v want one row for slot0/owner6", projected)
	}
	// 赋值语义必须保持：普通成员（owner=14，座位 1）也要被写成一个**空非 nil** 行，
	// 因为 currentDungeonProjectCardBundleToClearRewardSlots 是 `slots[base] = pairs`。
	// 若改成"空 bundle 跳过"，某些座位会保留旧内容 ⇒ 下发字节与改动前不同。
	if slots[1] == nil || len(slots[1]) != 0 {
		t.Fatalf("ordinary seat must still be assigned an empty row: %#v", slots[1])
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "kind=game-dungeon-black-diamond-cards-frozen") {
		t.Fatalf("freeze event missing:\n%s", text)
	}
	if !strings.Contains(text, "black_diamond_count=1") || !strings.Contains(text, "ordinary_count=1") {
		t.Fatalf("freeze counts wrong:\n%s", text)
	}
	if !strings.Contains(text, "frozen=6:1200:1") {
		t.Fatalf("freeze detail must carry owner:gold:items:\n%s", text)
	}
	if !strings.Contains(text, "instance_id=42") {
		t.Fatalf("freeze event must identify the instance:\n%s", text)
	}
}

// 全员普通（没有黑钻角色）时必须零额外日志：这是"不刷日志"的负向判据。
func TestPartyBlackDiamondCardsAllOrdinaryLogsNothing(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	instance := &dungeonPartyInstance{id: 7, members: map[uint16]time.Time{6: {}, 14: {}}}
	runtime := &runtimeDungeonState{partyInstance: instance}
	service := &Service{packetLog: logger}
	session := &gameSession{connID: "bd-card-none", selectedCharacterID: 6}
	session.party.state.Members = []alignedcmd.PartyMemberState{{UserID: 6}, {UserID: 14}}
	if err := service.freezePartyBlackDiamondCards(session, runtime, func(uint16) (dungeonCardRewardBundle, error) {
		return dungeonCardRewardBundle{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var slots [currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair
	if projected := projectPartyBlackDiamondCards(runtime, &slots); len(projected) != 0 {
		t.Fatalf("ordinary party must project nothing: %v", projected)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "black-diamond") {
		t.Fatalf("ordinary party must not log black-diamond events:\n%s", string(raw))
	}
}
