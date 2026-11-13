package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

func (s *Service) handleCurrentSingleQuestCoupon(p *gameSession, r quest.FinishQuestRequest, timing *currentQuestFinishTiming) error {
	reject := func() error { return s.sendGameUpperFailure(p, uint16(dnfenum.CmdPacketFinishQuest), 22) }
	if r.Reserved > 32767 || r.QuestID == 0 || r.Multiplier != 1 || r.HasRewardSelect || sessionHasActiveDungeonRuntime(p) {
		return reject()
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil || g.CharacterSettlement == nil {
		return reject()
	}
	id := strconv.Itoa(int(p.selectedCharacterID))
	inv, found, err := g.Inventory.Load(ctx, id)
	if err != nil || !found || inv.CharacterID != id {
		return reject()
	}
	stack, found := inv.Slots[currentCeraShopInventorySlotKey(0, int16(r.Reserved))]
	if !found || stack.Count <= 0 {
		return reject()
	}
	tables, items, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		return reject()
	}
	coupon, err := resolveCurrentQuestCoupon(items, stack.ItemID, 109)
	if err != nil {
		return reject()
	}
	if !s.currentCouponPlaceAllowed(p, coupon) {
		return reject()
	}
	definition, err := items.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return reject()
	}
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return reject()
	}
	now := s.gameplayNow()
	in := quest.UseClearCouponInput{AccountID: s.accountIDForSession(p), CharacterID: id, Slot: int16(r.Reserved), Coupon: coupon, Now: now, NormalizeSource: func(held repo.ItemStack) (repo.ItemStack, error) {
		return normalizeCurrentQuestCouponSource(held, definition, now)
	}}
	timing.markLoadDone()
	return s.handleCurrentFinishQuestTimed(p, r, g, quests, tables, currentQuestFinishItemAllocator(items), timing, currentQuestProfessionResources{SingleQuestCoupon: &in})
}
