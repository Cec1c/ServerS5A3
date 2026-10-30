package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/inventory"
)

// Verified against the paired V188: 0x25d7230 identifies quest potions by
// action 194; 0x24ee6e0 invokes the native 19-byte op515 sender. Its success
// callback 0x24f8c00 updates the quest-potion timer from the local PVF.
const privateQuestExperiencePotionAction uint32 = 194

func (s *Service) handlePrivateQuestExperiencePotion(session *gameSession, body []byte) (bool, error) {
	if len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != privateQuestExperiencePotionAction {
		return false, nil
	}
	slot := int16(binary.LittleEndian.Uint16(body))
	reject := func(reason any) (bool, error) {
		s.logInfo("quest experience potion rejected", "slot", slot, "reason", reason)
		return true, s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), inventory.BuildStackableActionFailure(slot, body[2], byte(privateQuestExperiencePotionAction), 1), 1)
	}
	if session == nil {
		return true, nil
	}
	if len(body) != 19 || slot < 0 || body[2] != 0 || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint32(body[11:15]) != 0 || binary.LittleEndian.Uint32(body[15:19]) != 0 {
		return reject("invalid_envelope")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return reject("unbound_character")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return reject("repository_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	characterID := strconv.Itoa(int(identity.character))
	bag, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return reject(err)
	}
	stack, exists := bag.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || !exists || stack.Count <= 0 || stack.ItemID <= 0 || stack.ItemID > 0xffffffff {
		return reject("source_missing")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return reject("pvf_unavailable")
	}
	item, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || item.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(item.StackableType) != "[waste]" || !strings.EqualFold(item.ActionType, "[quest_growth_drug]") {
		return reject("pvf_action_mismatch")
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
	if err != nil {
		return reject(err)
	}
	effect, err := experiencebuff.Parse(doc)
	if err != nil || effect.Kind != experiencebuff.Quest {
		return reject("invalid_effect")
	}
	usage, err := resolveCurrentItemUsageDefinition(catalog, stack.ItemID)
	if err != nil {
		return reject(err)
	}
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		return reject(err)
	}
	remaining, err := owner.UseQuestExperiencePotion(ctx, inventory.QuestExperiencePotionRequest{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, SlotIndex: slot,
		ItemID: stack.ItemID, PVFPath: item.PVFPath, Effect: effect, Usage: usage,
		Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) },
	}, s.gameplayNow())
	if err != nil {
		return reject(err)
	}
	s.logInfo("quest experience potion committed", "character_id", identity.character, "item_id", stack.ItemID, "remaining", remaining)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), inventory.BuildStackableActionAck(slot, 0, privateQuestExperiencePotionAction), 1); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "quest_experience_potion", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	return true, s.sendCurrentItemUsageForSession(session, "quest_experience_potion")
}
