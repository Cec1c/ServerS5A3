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
	const opcode uint16 = 1342
	handler := func(s *Service, g *gameSession, r gameplayRequest) error {
		return s.handleCurrentAikenUpgrade(g, r.Body)
	}
	setHandler := defaultClassGameplayHandler("aiken-set-class-rejected", "current_exe_op1341_requires_class1", func(s *Service, g *gameSession, b []byte) error { return s.handleCurrentAikenSet(g, b) })
	return gameplayModuleDefinition{
		Name: "aiken-upgrade",
		// Preserve the exact variable-length set body in either framing.
		LegacyHandlers: map[uint16]gameplayHandler{opcode: handler, 1341: setHandler},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: defaultClassGameplayHandler("aiken-upgrade-rejected", "class mismatch", func(s *Service, g *gameSession, b []byte) error { return s.handleCurrentAikenUpgrade(g, b) }),
			1341:   setHandler,
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{opcode: func(b []byte) []byte { return stripLegacyTransportTrailer(b, 2) }},
	}
}
func (s *Service) handleCurrentAikenUpgrade(g *gameSession, b []byte) error {
	if g == nil {
		return nil
	}
	reject := func(e error) error {
		reason := "invalid_or_unavailable"
		if errors.Is(e, aiken.ErrMaterials) {
			reason = "materials_insufficient"
		}
		if s.logger != nil {
			s.logger.Info("aiken-upgrade-rejected", "reason", reason)
		}
		return s.sendGameUpperRawClass(g, 1342, []byte{0, 0}, 1)
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
	// E672A0->10E2D70 snapshots the existing item; notify its new instance first.
	// A write failure never reruns the asset transaction or sends a success ACK.
	if e := s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, entries), 0); e != nil {
		return e
	}
	return s.sendGameUpperRawClass(g, 1342, []byte{1}, 1)
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
		if stack.ItemID == 0 {
			stack = repo.ItemStack{}
		}
		entries = append(entries, currentItemListEntryFromStack(0, int16(n), stack))
	}
	return entries
}
