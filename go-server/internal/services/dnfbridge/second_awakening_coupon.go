package dnfbridge

import (
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Current a751cccf EXE, RVA 0x20edd3e: action 121 emits the same 19-byte
// op515 request as action 118. Its ordinary gate (RVA 0x23a1920) requires
// awakening stage 1 and an existing second-awakening class profile.
// A nonzero second PVF parameter bypasses that native gate. The external-job
// self-awakening effect is not established by that bypass and remains blocked.
func resolveSecondAwakeningCouponConditions(c quest.ClearCoupon, doc *pvf.Document) (quest.ClearCoupon, error) {
	fail := func() (quest.ClearCoupon, error) { return c, quest.ErrCouponClearUnavailable }
	action, ok := doc.Section("action type")
	if !ok || len(action) < 2 || len(action) > 3 || action[0].Value != "[second awaken clear]" || action[1].Kind != pvf.TokenInt || action[1].Int != 0 {
		return fail()
	}
	if len(action) == 3 && (action[2].Kind != pvf.TokenInt || action[2].Int != 1) {
		return fail()
	}
	if kind, _ := doc.Text("stackable type"); kind != "[etc]" {
		return fail()
	}
	if packet, ok := doc.Int("use action packet"); !ok || packet != 1 {
		return fail()
	}
	if c.MinimumLevel < 0 || c.MaximumLevel < 0 || c.MaximumLevel > 0 && c.MaximumLevel < c.MinimumLevel {
		return fail()
	}
	for _, place := range doc.Texts("action usable place") {
		if place != "[village]" && place != "[seria room]" {
			return fail()
		}
		c.Places = append(c.Places, place)
	}
	for _, tag := range doc.Texts("impossible jobs") {
		job := -1
		for j := 0; j <= jobmap.MaxID; j++ {
			if jobmap.MatchesQuestTag("["+strings.Trim(strings.TrimSpace(tag), "[]")+"]", j) {
				job = j
				break
			}
		}
		if job < 0 {
			return fail()
		}
		c.ExcludedJobs = append(c.ExcludedJobs, job)
	}
	if values, exists := doc.Section("unable use grow type"); exists {
		if len(values) == 0 || len(values)%2 != 0 {
			return fail()
		}
		for i := 0; i < len(values); i += 2 {
			j, b := values[i], values[i+1]
			if j.Kind != pvf.TokenInt || b.Kind != pvf.TokenInt || j.Int < 0 || j.Int > jobmap.MaxID || b.Int < 1 || b.Int > 15 {
				return fail()
			}
			c.ExcludedBranches = append(c.ExcludedBranches, [2]int{int(j.Int), int(b.Int)})
		}
	}
	// Every occurrence counts; some PVF files repeat this section.
	for _, section := range doc.Sections {
		if section.Name != "impossible contents" {
			continue
		}
		for _, token := range doc.Tokens[section.Start:section.End] {
			switch token.Value {
			case "hardcore mode":
				c.DisallowHardcore = true
			case "ban redeemitem", "gift": // Redemption/gifting restriction, not a use fee.
			default:
				return fail()
			}
		}
	}
	for _, field := range []string{"action usable condition", "using limit info", "total usable count", "usable game server type"} {
		if tokens, exists := doc.Section(field); exists && len(tokens) > 0 {
			return fail()
		}
	}
	// Top-level need material is the NPC exchange price (npc_shop_pvf.go),
	// not a second charge for using the already-owned coupon.
	return c, nil
}

func (s *Service) handleCurrentSecondAwakeningCoupon(p *gameSession, r currentQuestClearCouponRequest) (bool, error) {
	reject := func(reason string, err error) (bool, error) {
		s.logQuestClearCouponEvent(p, "game-second-awakening-coupon-blocked", r, "reason", reason, "error", err)
		if p == nil || p.conn == nil {
			return true, nil
		}
		return true, s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketUseStackableAction), buildCurrentQuestCouponResult(r, false), 1)
	}
	if p == nil || !p.loginPacketSeen || p.accountID == "" || !r.ReservedValid || r.ListType != repo.MainInventoryListType || r.SourceSlotIndex < 0 {
		return reject("request_invalid", nil)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject("binding_missing", nil)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, p.accountID)
	if err != nil {
		return reject("binding_busy", err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	if _, inRaid := s.runtimeRaidForMember(identity.character); inRaid {
		return reject("raid_active", nil)
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil || g.CharacterSettlement == nil {
		return reject("repository_unavailable", nil)
	}
	id := strconv.Itoa(int(identity.character))
	inv, found, err := g.Inventory.Load(ctx, id)
	if err != nil || !found || inv.CharacterID != id {
		return reject("inventory_unavailable", err)
	}
	stack, found := inv.Slots[currentCeraShopInventorySlotKey(r.ListType, r.SourceSlotIndex)]
	if !found || stack.Count <= 0 {
		return reject("source_missing", nil)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject("pvf_unavailable", err)
	}
	coupon, err := resolveCurrentQuestCoupon(items, stack.ItemID, r.ActionIndex)
	if err != nil {
		return reject("coupon_invalid", err)
	}
	definition, err := items.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return reject("item_definition_missing", err)
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return reject("quests_unavailable", err)
	}
	profiles, skills, err := s.currentProfessionResources(ctx)
	if err != nil {
		return reject("profession_unavailable", err)
	}
	owner, err := quest.NewOwner(g)
	if err != nil {
		return reject("owner_unavailable", err)
	}
	if !p.townMu.TryLock() {
		return reject("town_changing", nil)
	}
	if !p.dungeon.mu.TryLock() {
		p.townMu.Unlock()
		return reject("dungeon_changing", nil)
	}
	if !s.pvpMu.TryLock() {
		p.dungeon.mu.Unlock()
		p.townMu.Unlock()
		return reject("pvp_changing", nil)
	}
	result, err := func() (quest.UseClearCouponResult, error) {
		defer s.pvpMu.Unlock()
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		area, known := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
		if p.dungeon.runtime != nil || p.pvpRoom != nil || p.townSceneReadyCharacterID != identity.character || !p.townPositionSnapshot.PositionValid || !known || area.MapPath == "" {
			return quest.UseClearCouponResult{}, fmt.Errorf("second awakening coupon requires a ready town scene")
		}
		for _, queued := range s.pvpMatching {
			if queued != nil && queued.member != nil && queued.member.session == p {
				return quest.UseClearCouponResult{}, fmt.Errorf("pvp matching is active")
			}
		}
		place := "[village]"
		if townMapPathLooksLikeSeria(area.MapPath) {
			place = "[seria room]"
		}
		if len(coupon.Places) > 0 {
			allowed := false
			for _, v := range coupon.Places {
				allowed = allowed || v == place
			}
			if !allowed {
				return quest.UseClearCouponResult{}, fmt.Errorf("coupon is not usable in this town area")
			}
		}
		now := s.gameplayNow()
		return owner.UseClearCoupon(ctx, catalog, quest.UseClearCouponInput{AccountID: p.accountID, CharacterID: id, Slot: r.SourceSlotIndex, Coupon: coupon, Now: now, Profiles: profiles, Skills: skills, ValidateSource: func(c repo.CharacterRecord, held repo.ItemStack) error {
			if !s.boundGameSessionCharacterCurrent(identity) || itemlock.IsLockedAt(held, now) {
				return quest.ErrCouponClearUnavailable
			}
			projected, _ := applyCurrentPVFItemExpirationAt(held, definition, now)
			if currentDungeonDiscardStackExpired(projected, now) {
				return quest.ErrCouponClearUnavailable
			}
			return nil
		}})
	}()
	// Binding writers can hold dungeon.mu while waiting for this reservation.
	// Release after the atomic commit, before projection takes scene locks or
	// waits on socket I/O. A failed projection never retries the asset mutation.
	s.releaseCurrentCharacterAssets(reservation)
	if err != nil {
		return reject("commit_rejected", err)
	}
	if !result.Changed {
		return reject("nothing_to_complete", nil)
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return true, nil
	}
	s.logQuestClearCouponEvent(p, "game-second-awakening-coupon-committed", r, "item_id", stack.ItemID, "remaining", result.Remaining, "quests_completed", len(result.ChangedQuestIDs))
	// Commit has happened. A projection failure is never a reason to consume again.
	if err = s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketUseStackableAction), buildCurrentQuestCouponResult(r, true), 1); err != nil {
		return true, err
	}
	if err = s.sendSelectedIncrementalItemSlotRefreshes(p, "second_awakening_coupon", []alignedcmd.ItemSlotRefresh{{ListType: r.ListType, SlotIndex: r.SourceSlotIndex}}); err != nil {
		return true, err
	}
	if err = s.sendCurrentAwakeningCouponSnapshots(ctx, p, result); err != nil {
		return true, err
	}
	// The native empty active list clears the completed-quest cache. Project
	// both from this commit, with the completed table following the active list.
	if err = s.sendCurrentActiveQuestSnapshotFromCommittedQuest(p, result.Quests, "second_awakening_coupon"); err != nil {
		return true, err
	}
	if err = s.sendCurrentClearQuestListFromCommittedQuest(p, result.Quests, "second_awakening_coupon"); err != nil {
		return true, err
	}
	return true, s.sendCurrentAcceptableQuestListOnlyForSession(p, "second_awakening_coupon")
}
