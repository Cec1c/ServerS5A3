package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
)

func (s *Service) handleCurrentAikenSet(g *gameSession, b []byte) error {
	if g == nil {
		return nil
	}
	reject := func(err error) error {
		code := byte(1)
		reason := "invalid_or_unavailable"
		if errors.Is(err, aiken.ErrMaterials) {
			code = 8
			reason = "materials_insufficient"
		}
		if s.logger != nil {
			s.logger.Info("aiken-set-rejected", "reason", reason)
		}
		return s.sendGameUpperRawClass(g, 1341, []byte{0, code}, 1)
	}
	request, err := aiken.DecodeSetRequest(b)
	if err != nil || g.selectedCharacterID == 0 {
		return reject(aiken.ErrInvalid)
	}
	g.dungeon.mu.Lock()
	busy := g.dungeon.runtime != nil
	g.dungeon.mu.Unlock()
	if busy {
		return reject(aiken.ErrInvalid)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject(aiken.ErrInvalid)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	rules, err := currentAikenSetRules(catalog)
	if err != nil {
		return reject(err)
	}
	owner, err := aiken.NewOwner(repos)
	if err != nil {
		return reject(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ApplySet(ctx, aiken.SetCommand{AccountID: s.accountIDForSession(g), CharacterID: strconv.Itoa(int(g.selectedCharacterID)), Request: request, Rules: rules, Now: s.gameplayNow()})
	if err != nil {
		return reject(err)
	}
	entries := currentAikenChangedEntries(aiken.Result{Changed: result.Changed})
	if err = s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, entries), 0); err != nil {
		return err
	}
	if result.EquipmentChanged {
		if err = s.sendSelectedCurrentEquipmentSlotItemUpdates(g, "aiken_set_replaced"); err != nil {
			return err
		}
	}
	if result.WarehouseChanged {
		if err = s.sendCurrentContainerListTypesWithRefresh(g, "aiken_set_replaced", true, []byte{2}); err != nil {
			return err
		}
	}
	result.Record.Selector = uint32(g.selectedCharacterID)
	if err = s.sendGameUpperRawClass(g, 1278, result.Record.Encode(), 0); err != nil {
		return err
	}
	if result.EquipmentChanged {
		if err = s.sendSelectedActorAppearanceRefresh(g, "aiken_set_replaced", "after_set_record"); err != nil {
			return err
		}
		if err = s.sendSelectedCurrentCombatPowerAffixes(g, "aiken_set_replaced"); err != nil {
			return err
		}
	}
	return s.sendGameUpperRawClass(g, 1341, []byte{1}, 1)
}
