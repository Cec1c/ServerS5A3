package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
)

func aikenUpgradeGameplayModule() gameplayModuleDefinition {
	const (
		optionOpcode  uint16 = 1340
		upgradeOpcode uint16 = 1342
	)
	handler := func(s *Service, g *gameSession, r gameplayRequest) error {
		return s.handleCurrentAikenUpgrade(g, r.Body)
	}
	optionHandler := defaultClassGameplayHandler("aiken-option-class-rejected", "current_exe_op1340_requires_class1", func(s *Service, g *gameSession, b []byte) error { return s.handleCurrentAikenOption(g, b) })
	setHandler := defaultClassGameplayHandler("aiken-set-class-rejected", "current_exe_op1341_requires_class1", func(s *Service, g *gameSession, b []byte) error { return s.handleCurrentAikenSet(g, b) })
	return gameplayModuleDefinition{
		Name: "aiken-upgrade",
		// Legacy names the client framing, not classification zero. Live
		// op1340/1341 requests use legacy framing with cmd=1. Preserve the
		// exact variable-length set body for the domain decoder.
		LegacyHandlers: map[uint16]gameplayHandler{upgradeOpcode: handler, optionOpcode: optionHandler, 1341: setHandler},
		UpperHandlers: map[uint16]gameplayHandler{
			1341:          setHandler,
			optionOpcode:  optionHandler,
			upgradeOpcode: defaultClassGameplayHandler("aiken-upgrade-rejected", "class mismatch", func(s *Service, g *gameSession, b []byte) error { return s.handleCurrentAikenUpgrade(g, b) }),
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{upgradeOpcode: func(b []byte) []byte { return stripLegacyTransportTrailer(b, 2) }},
	}
}
func (s *Service) handleCurrentAikenUpgrade(g *gameSession, b []byte) error {
	if g == nil {
		return nil
	}
	// Keep this request's identity across transaction and socket outcomes.
	// Diagnostics must remain available when the raw packet logger is disabled.
	// send_ok describes the existing send helper's result, not client receipt.
	s.mu.Lock()
	connID, charID := g.connID, g.selectedCharacterID
	s.mu.Unlock()
	slot := int16(-1)
	if len(b) == 2 {
		slot = int16(binary.LittleEndian.Uint16(b))
	}
	diagnostic := func(event string, fields ...any) {
		if s.logger != nil {
			args := []any{"conn_id", connID, "char_id", charID, "opcode", 1342, "slot", slot}
			s.logger.Info(event, append(args, fields...)...)
		}
	}
	diagnostic("aiken-upgrade-request", "body_len", len(b), "slot_decoded", len(b) == 2)
	reject := func(e error) error {
		reason := "invalid_or_unavailable"
		nativeReason := byte(0)
		if errors.Is(e, aiken.ErrMaterials) {
			reason = "materials_insufficient"
			// v156 00E66CD7: reason 8 -> dstr FILE/102 ID 60557.
			nativeReason = 8
		}
		diagnostic("aiken-upgrade-rejected", "reason", reason, "native_reason", nativeReason)
		err := s.sendGameUpperRawClass(g, 1342, []byte{0, nativeReason}, 1)
		diagnostic("aiken-upgrade-send-result", "msg_id", 1342, "classification", 1,
			"ack_success", false, "native_reason", nativeReason, "committed", false, "send_ok", err == nil)
		return err
	}
	if g.selectedCharacterID == 0 || len(b) != 2 {
		return reject(aiken.ErrInvalid)
	}
	g.dungeon.mu.Lock()
	inDungeon := g.dungeon.runtime != nil
	g.dungeon.mu.Unlock()
	if inDungeon {
		return reject(aiken.ErrInvalid)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject(aiken.ErrInvalid)
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return reject(e)
	}
	concrete := catalog
	rules, e := currentAikenRules(concrete)
	if e != nil {
		return reject(e)
	}
	owner, e := aiken.NewOwner(repos)
	if e != nil {
		return reject(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, e := owner.Upgrade(ctx, aiken.Command{AccountID: s.accountIDForSession(g), CharacterID: strconv.Itoa(int(g.selectedCharacterID)), Slot: int16(binary.LittleEndian.Uint16(b)), Rules: rules, Now: s.gameplayNow()})
	if e != nil {
		return reject(e)
	}
	entries := currentAikenChangedEntries(result)
	if s.logger != nil {
		// Sorted by the same slots as the actual 14 projection. Zero item/count
		// denotes a cleared slot; no instance bytes or opaque Extra are logged.
		slots := make([]int, 0, len(result.Changed))
		for changedSlot := range result.Changed {
			slots = append(slots, int(changedSlot))
		}
		sort.Ints(slots)
		itemIDs, counts := make([]int64, 0, len(slots)), make([]int64, 0, len(slots))
		for _, changedSlot := range slots {
			stack := result.Changed[int16(changedSlot)]
			itemIDs, counts = append(itemIDs, stack.ItemID), append(counts, stack.Count)
		}
		diagnostic("aiken-upgrade-committed", "before", result.Before, "after", result.After,
			"item_id", result.Changed[slot].ItemID, "material_item_id", rules.Material,
			"changed_slots", slots, "item_ids", itemIDs, "counts", counts)
	}
	// E672A0->10E2D70 snapshots the existing item; notify its new instance first.
	// A write failure never reruns the asset transaction or sends a success ACK.
	e = s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, entries), 0)
	diagnostic("aiken-upgrade-send-result", "msg_id", 14, "classification", 0,
		"committed", true, "send_ok", e == nil)
	if e != nil {
		return e
	}
	e = s.sendGameUpperRawClass(g, 1342, []byte{1}, 1)
	diagnostic("aiken-upgrade-send-result", "msg_id", 1342, "classification", 1,
		"ack_success", true, "committed", true, "send_ok", e == nil)
	return e
}

func currentAikenChangedEntries(result aiken.Result) []currentItemListEntry {
	slots := make([]int, 0, len(result.Changed))
	for slot := range result.Changed {
		slots = append(slots, int(slot))
	}
	sort.Ints(slots)
	entries := make([]currentItemListEntry, 0, len(slots))
	for _, n := range slots {
		stack := result.Changed[int16(n)]
		if stack.ItemID == 0 && n > 0 {
			// Current-client op14 deletes an existing item only for the empty
			// sentinel. Projecting a zero stack as an item can leave its old
			// quantity available to the next exchange ACK's increment.
			var entry currentItemListEntry
			patchEmptySlotRow(&entry, int16(n))
			entries = append(entries, entry)
			continue
		}
		if stack.ItemID == 0 {
			stack = repo.ItemStack{}
		}
		entries = append(entries, currentItemListEntryFromStack(0, int16(n), stack))
	}
	return entries
}

func (s *Service) handleCurrentAikenOption(g *gameSession, b []byte) error {
	if g == nil {
		return nil
	}
	reject := func(e error) error {
		reason := "invalid_or_unavailable"
		if errors.Is(e, aiken.ErrMaterials) {
			reason = "materials_insufficient"
		}
		if s.logger != nil {
			s.logger.Info("aiken-option-rejected", "reason", reason)
		}
		return s.sendGameUpperRawClass(g, 1340, []byte{0, 0}, 1)
	}
	if g.selectedCharacterID == 0 || len(b) != 4 || b[2] > 1 {
		return reject(aiken.ErrInvalid)
	}
	g.dungeon.mu.Lock()
	inDungeon := g.dungeon.runtime != nil
	g.dungeon.mu.Unlock()
	if inDungeon {
		return reject(aiken.ErrInvalid)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject(aiken.ErrInvalid)
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return reject(e)
	}
	rules, e := currentAikenRules(catalog)
	if e != nil {
		return reject(e)
	}
	owner, e := aiken.NewOwner(repos)
	if e != nil {
		return reject(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, e := owner.ChangeOption(ctx, aiken.OptionCommand{
		AccountID:   s.accountIDForSession(g),
		CharacterID: strconv.Itoa(int(g.selectedCharacterID)),
		Slot:        int16(binary.LittleEndian.Uint16(b[:2])),
		Position:    b[2],
		Option:      b[3],
		Rules:       rules,
		Now:         s.gameplayNow(),
	})
	if e != nil {
		return reject(e)
	}
	entries := currentAikenChangedEntries(result)
	if e := s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, entries), 0); e != nil {
		return e
	}
	return s.sendGameUpperRawClass(g, 1340, []byte{1}, 1)
}
