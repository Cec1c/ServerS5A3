package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Find a persisted, proven relative grant deadline; never create a new period
// from the current clock. The daily owner remains responsible for daily items.
func (s *Service) nextCurrentRelativeItemExpiration(session *gameSession, now time.Time) (time.Time, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return time.Time{}, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Inventory == nil || repos.Equipment == nil || repos.AccountInventory == nil {
		return time.Time{}, fmt.Errorf("relative expiry repositories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	id := strconv.Itoa(int(session.selectedCharacterID))
	account := s.accountIDForSession(session)
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	if !found || character.AccountID != account {
		return time.Time{}, fmt.Errorf("relative expiry owner mismatch")
	}
	inv, _, err := repos.Inventory.Load(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	shared, _, err := repos.AccountInventory.Load(ctx, account)
	if err != nil {
		return time.Time{}, err
	}
	eq, _, err := repos.Equipment.Load(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	var catalog *pvfDungeonDropCatalog
	inspect := func(stack dnfrepo.ItemStack) error {
		if stack.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" || stack.ExpireAt.IsZero() {
			return nil
		}
		if catalog == nil {
			var e error
			catalog, e = s.currentPVFItemCatalog()
			if e != nil {
				return e
			}
		}
		def, e := s.currentPVFDefinitionForItem(catalog, sceneInventoryUint32FromInt64(stack.ItemID))
		if e == nil && stack.Extra["hatched_from_item_id"] != "" {
			def, e = s.currentPetExpirationDefinition(catalog, stack)
		}
		if e != nil {
			return e
		}
		if !currentPVFRelativeGrantExpired(stack, def, stack.ExpireAt) {
			return nil
		}
		deadline := stack.ExpireAt
		if !deadline.After(now) {
			deadline = now
		}
		if next.IsZero() || deadline.Before(next) {
			next = deadline
		}
		return nil
	}
	for _, container := range []map[string]dnfrepo.ItemStack{inv.Slots, inv.Warehouse, shared.Slots} {
		for _, stack := range container {
			if e := inspect(stack); e != nil {
				return time.Time{}, e
			}
		}
	}
	for _, entry := range eq.Entries {
		if e := inspect(dnfrepo.ItemStack{ItemID: entry.ItemID, ExpireAt: entry.ExpireAt, Extra: entry.Extra}); e != nil {
			return time.Time{}, e
		}
	}
	return next, nil
}

type currentItemExpiryProjection struct {
	characterID uint16
	generation  uint64
	slots       []currentDailyDeleteSlot
	wornRedraw  bool
	petRefresh  bool
}

func (s *Service) mergeCurrentExpiryProjection(session *gameSession, characterID uint16, generation uint64, removed []currentDailyDeleteSlot) {
	p := session.pendingItemExpiryProjection
	if p != nil && (p.characterID != characterID || p.generation != generation) {
		p = nil
		session.pendingItemExpiryProjection = nil
	}
	if len(removed) == 0 {
		return
	}
	if p == nil {
		p = &currentItemExpiryProjection{characterID: characterID, generation: generation}
		session.pendingItemExpiryProjection = p
	}
	seen := make(map[currentDailyDeleteSlot]bool)
	for _, slot := range p.slots {
		seen[slot] = true
	}
	for _, slot := range removed {
		if !seen[slot] {
			p.slots = append(p.slots, slot)
			seen[slot] = true
		}
		if slot.list == 3 {
			p.wornRedraw = true
		}
		if slot.list == 7 || (slot.list == 3 && slot.slot == 26) {
			p.petRefresh = true
		}
	}
	sort.Slice(p.slots, func(i, j int) bool {
		if p.slots[i].list != p.slots[j].list {
			return p.slots[i].list < p.slots[j].list
		}
		return p.slots[i].slot < p.slots[j].slot
	})
}

// Retry from current authoritative rows: a slot may have been reused after a
// failed write. A retained expiry receipt must never clear its replacement.
func (s *Service) sendCurrentExpiryProjection(session *gameSession) error {
	p := session.pendingItemExpiryProjection
	if p == nil {
		return nil
	}
	if session.selectedCharacterID != p.characterID || session.characterGeneration != p.generation {
		session.pendingItemExpiryProjection = nil
		return nil
	}
	var regular []alignedcmd.ItemSlotRefresh
	var pets []alignedcmd.ItemSlotRefresh
	var worn []int16
	petWorn := false
	for _, slot := range p.slots {
		if slot.list == 3 && slot.slot == 26 {
			petWorn = true
		} else if slot.list == 3 {
			worn = append(worn, slot.slot)
		} else if slot.list == 7 {
			pets = append(pets, alignedcmd.ItemSlotRefresh{ListType: slot.list, SlotIndex: slot.slot})
		} else {
			regular = append(regular, alignedcmd.ItemSlotRefresh{ListType: slot.list, SlotIndex: slot.slot})
		}
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "relative_expiration", regular); err != nil {
		return err
	}
	if err := s.sendSelectedPetMoveSlotRefreshes(session, "relative_expiration", pets); err != nil {
		return err
	}
	if petWorn {
		if !currentTownSelfActorAppearanceReady(session) {
			return fmt.Errorf("pet expiry worn actor not ready")
		}
		if err := s.sendCurrentExpiredPetWornSlot(session); err != nil {
			return err
		}
	}
	if len(worn) > 0 {
		if !currentTownSelfActorAppearanceReady(session) {
			return fmt.Errorf("relative expiry worn actor not ready")
		}
		if err := s.sendWornEquipmentMutationSlots(session, "relative_expiration", worn); err != nil {
			return err
		}
	}
	if p.wornRedraw {
		if !currentTownSelfActorAppearanceReady(session) {
			return fmt.Errorf("relative expiry redraw actor not ready")
		}
		// Reuse the established wear projection and its lifecycle guards.
		if err := s.sendAlignedPostAction(session, alignedcmd.Result{Operation: "relative_expiration"}, alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
			return err
		}
		if err := s.sendSelectedCurrentCombatPowerAffixes(session, "relative_expiration_after_commit"); err != nil {
			return err
		}
	}
	if p.petRefresh {
		if ready, _ := s.currentSceneActorReadyForState(session); !ready {
			return fmt.Errorf("pet expiry actor not ready")
		}
		if err := s.sendSelectedCreatureStateTableWithRefresh(session, "relative_expiration", true); err != nil {
			return err
		}
	}
	session.pendingItemExpiryProjection = nil
	return nil
}

// Item grants/moves reach an existing authoritative row or list projection.
// Bootstrap arms its own timer after the complete initial container plan.
func (s *Service) rearmCurrentRelativeExpirationAfterItems(session *gameSession, body []byte) {
	if session == nil || session.pendingItemExpiryProjection != nil || session.selectedCharacterID == 0 || session.selectedItemListBootstrapCharacterID != session.selectedCharacterID || len(body) < 3 {
		return
	}
	// Most item updates have no expiry. Only a real expiration-bearing row can
	// introduce an earlier deadline; don't read all repositories on every ACK.
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if len(body) != 3+count*currentItemListEntryWireSize {
		return
	}
	for i := 0; i < count; i++ {
		offset := 3 + i*currentItemListEntryWireSize + currentItemListExpireTimeOffset
		if binary.LittleEndian.Uint32(body[offset:offset+4]) != 0 {
			s.armCurrentDailyDeleteClock(session)
			return
		}
	}
}
