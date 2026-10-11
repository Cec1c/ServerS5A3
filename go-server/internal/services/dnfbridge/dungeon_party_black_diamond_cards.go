package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/premium"
	"strconv"
	"strings"
)

// freezePartyBlackDiamondCards 冻结每个成员的额外牌（是否真有牌由 roll 决定：
// 调用点对**非黑钻成员**返回空 bundle）。冻结结果同时被本人理赔与所有客户端的
// kind-1 牌向量使用。调用方持有自己的副本锁时不会去拿对端的副本锁。
//
// 2026-10-02 (V22) 诊断：这条链路此前**一行日志都没有**，导致"队友看到黑钻角色额外翻牌"
// 这个验收项在线上既不能自证也不能证伪（日志里连它是否跑过都看不出来）。这里只加一条
// **只在真有黑钻牌被冻结时**才写的事件行；全员普通（bundle 全空）时仍然零日志、零噪音。
func (s *Service) freezePartyBlackDiamondCards(session *gameSession, runtime *runtimeDungeonState, roll func(uint16) (dungeonCardRewardBundle, error)) error {
	instance := runtime.partyInstance
	if instance == nil {
		return nil
	}
	session.party.mu.Lock()
	members := append([]uint16(nil), currentPartyBlackDiamondMemberIDs(session)...)
	session.party.mu.Unlock()
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.blackDiamondCards != nil {
		return nil
	}
	bundles := make(map[uint16]dungeonCardRewardBundle, len(members))
	var owners [dungeonCardSlotsPerSide]uint16
	ordinary := 0
	frozen := make([]string, 0, len(members))
	for slot, owner := range members {
		if slot >= len(owners) {
			break
		}
		if _, present := instance.members[owner]; !present {
			continue
		}
		bundle, err := roll(owner)
		if err != nil {
			return err
		}
		bundles[owner] = cloneDungeonCardRewardBundle(bundle)
		owners[slot] = owner
		if bundle.Gold == 0 && len(bundle.Items) == 0 {
			ordinary++
			continue
		}
		frozen = append(frozen, fmt.Sprintf("%d:%d:%d", owner, bundle.Gold, len(bundle.Items)))
	}
	instance.blackDiamondCards = bundles
	instance.blackDiamondCardOwners = owners
	if len(frozen) > 0 {
		s.logGameEvent(session, "game-dungeon-black-diamond-cards-frozen",
			"char_id", session.selectedCharacterID,
			"instance_id", instance.id,
			"party_members", len(members),
			"black_diamond_count", len(frozen),
			"ordinary_count", ordinary,
			"frozen", strings.Join(frozen, ","),
			"body_source", "freezePartyBlackDiamondCards_owner_gold_itemcount")
	}
	return nil
}

// Caller holds party.mu; capture stable slot identities before instance.mu.
func currentPartyBlackDiamondMemberIDs(session *gameSession) []uint16 {
	ids := make([]uint16, 0, len(session.party.state.Members))
	for _, member := range session.party.state.Members {
		ids = append(ids, member.UserID)
	}
	return ids
}

func (s *Service) partyMemberBlackDiamondActive(ctx context.Context, owner uint16) bool {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Account == nil {
		return false
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(owner)))
	if err != nil || !found || character.AccountID == "" {
		return false
	}
	account, found, err := repositories.Account.Load(ctx, character.AccountID)
	return err == nil && found && premium.Active(account, premium.TypeBlackDiamond, s.gameplayNow().UTC())
}

func partyBlackDiamondCardForOwner(runtime *runtimeDungeonState, owner uint16) dungeonCardRewardBundle {
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return cloneDungeonCardRewardBundle(instance.blackDiamondCards[owner])
}

// projectPartyBlackDiamondCards 把每个黑钻成员的额外牌投影进**收包者自己**的 kind-1 牌向量。
// 返回投影摘要（slot/owner/rows），由调用方决定怎么记日志 —— 这样本函数保持纯函数、
// 可在测试里直接比对"本人视图与队友视图逐字节一致"。
//
// 注意（2026-10-02 复核）：`currentDungeonProjectCardBundleToClearRewardSlots` 是**赋值**语义
// （`slots[base] = pairs`），所以空 bundle 的成员**也必须照原样调用**（把该座位写成空行），
// 否则会与改动前的下发字节不同。摘要里只列真正有牌的黑钻成员，不影响赋值行为。
func projectPartyBlackDiamondCards(runtime *runtimeDungeonState, slots *[currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair) []string {
	if runtime == nil || runtime.partyInstance == nil {
		return nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	var projected []string
	for slot, owner := range instance.blackDiamondCardOwners {
		if owner == 0 {
			continue
		}
		bundle := instance.blackDiamondCards[owner]
		currentDungeonProjectCardBundleToClearRewardSlots(slots, slot, bundle)
		if bundle.Gold == 0 && len(bundle.Items) == 0 {
			continue // 普通成员：赋值照旧（空行），只是不进摘要
		}
		projected = append(projected, fmt.Sprintf("%d:%d:%d", slot, owner, len(slots[slot])))
	}
	return projected
}
