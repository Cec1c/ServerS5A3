package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The matching EXE maps [add guildmileage] to token 0x2d66, then action 145.
// Its constructor copies that value into the op515 sender's action field.
// Historical action 143 is not the value this client emits; [use action packet] 1 is
// only a switch. See Docs/GUILD_POINTS.md for the matched module evidence.
const currentGuildMileageItemAction uint32 = 145

func parseCurrentGuildMileageItem(doc *dnfpvf.Document, def *dungeonDropItemDefinition) error {
	if !strings.EqualFold(def.ActionType, "[add guildmileage]") {
		return nil
	}
	action, _ := doc.Section("action type")
	conditions := doc.Texts("action usable condition")
	places := doc.Texts("action usable place")
	packet, _ := doc.Int("use action packet")
	minimum, _ := doc.Int("minimum level")
	if normalizeDungeonDropStackableType(def.StackableType) != "[waste]" || len(action) != 2 || action[1].Kind != dnfpvf.TokenInt || action[1].Int <= 0 || action[1].Int > math.MaxUint32 || len(conditions) != 1 || conditions[0] != "[guildmember]" || len(places) != 1 || places[0] != "[village]" || packet != 1 || minimum < 0 {
		return fmt.Errorf("incomplete guild mileage action/amount/member/village contract")
	}
	def.GuildMileageItem = &inventory.GuildMileageItemDefinition{ItemID: int64(def.ItemID), PVFPath: def.PVFPath, Amount: uint32(action[1].Int), MinimumLevel: minimum}
	return nil
}

func (s *Service) handleCurrentGuildMileageItem(session *gameSession, body []byte) (bool, error) {
	if len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != currentGuildMileageItemAction {
		return false, nil
	}
	op := uint16(dnfenum.CmdPacketUseStackableAction)
	reject := func(reason any) (bool, error) {
		s.logGameEvent(session, "game-guild-mileage-item-rejected", "reason", reason)
		return true, s.sendGameUpperRawClass(session, op, buildCurrentGuildMileageItemFailure(body, 17), 1)
	}
	if len(body) != 19 || body[2] != dnfrepo.MainInventoryListType || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint32(body[11:15]) != 0 || binary.LittleEndian.Uint32(body[15:19]) != 0 {
		return reject("invalid_action_envelope")
	}
	slot := int16(binary.LittleEndian.Uint16(body))
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 || slot < 0 {
		return reject("unbound_character_or_slot")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return reject("repository_unavailable")
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	// Town and canonical ownership remain fixed through the paid commit. No
	// cross-session projection is attempted while the actor's town lock is held.
	// Character replacement can hold a scene lock while waiting for this
	// reservation. Do not wait back on it: a busy scene rejects without debit.
	if !session.townMu.TryLock() {
		return reject("town_transition_in_progress")
	}
	result, err := func() (inventory.GuildMileageItemResult, error) {
		defer session.townMu.Unlock()
		revalidate := func() bool {
			if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || !session.townPositionSnapshot.PositionValid {
				return false
			}
			if !session.dungeon.mu.TryLock() {
				return false
			}
			inDungeon := session.dungeon.runtime != nil
			session.dungeon.mu.Unlock()
			area, found := s.townArea(int64(session.townPositionSnapshot.TownID), int64(session.townPositionSnapshot.AreaID))
			return !inDungeon && found && area.MapPath != "" && !townMapPathLooksLikeSeria(area.MapPath)
		}
		if !revalidate() {
			return inventory.GuildMileageItemResult{}, fmt.Errorf("guild mileage bag requires a current village scene")
		}
		owner := dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(identity.character)), AccountID: s.accountIDForSession(session)}
		record, found, err := repos.Inventory.Load(ctx, owner.CharacterID)
		if err != nil {
			return inventory.GuildMileageItemResult{}, err
		}
		stack, exists := record.Slots["0:"+strconv.Itoa(int(slot))]
		if !found || !exists || stack.Count <= 0 || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
			return inventory.GuildMileageItemResult{}, inventory.ErrGuildMileageItem
		}
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return inventory.GuildMileageItemResult{}, err
		}
		def, err := catalog.ResolveItem(uint32(stack.ItemID))
		if err != nil {
			return inventory.GuildMileageItemResult{}, err
		}
		if def.GuildMileageItem == nil {
			return inventory.GuildMileageItemResult{}, inventory.ErrGuildMileageItem
		}
		itemOwner, err := inventory.NewOwner(repos)
		if err != nil {
			return inventory.GuildMileageItemResult{}, err
		}
		return itemOwner.UseGuildMileageItem(ctx, inventory.GuildMileageItemRequest{Owner: owner, SlotIndex: slot, Definition: *def.GuildMileageItem, Revalidate: revalidate}, s.gameplayNow())
	}()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "game-guild-mileage-item-used", "character_id", identity.character, "guild_id", result.GuildID, "mileage", result.Mileage, "remaining", result.Remaining)
	if err := s.sendGameUpperRawClass(session, op, buildCurrentGuildMileageItemAck(slot), 1); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_mileage_item_after_ack", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return true, err
	}
	return true, s.sendCurrentGuildManagementState(session, g)
}

// The matching EXE class1/op515 callback (RVA 0x20f3580) reads u16 slot,
// u8 list and u32 action after the dispatcher consumes the success byte.
// Action 145 has no additional payload reader. It consumes one local item;
// the following op14 supplies the authoritative remaining stack.
func buildCurrentGuildMileageItemAck(slot int16) []byte {
	return inventory.BuildStackableActionAck(slot, dnfrepo.MainInventoryListType, currentGuildMileageItemAction)
}

func buildCurrentGuildMileageItemFailure(request []byte, code byte) []byte {
	// This handler only accepts an envelope with at least eleven bytes.
	// RVA 0x20f425a reads these fields even after a rejected command. Unlike
	// success, the native failure path reads the action as one byte.
	return inventory.BuildStackableActionFailure(int16(binary.LittleEndian.Uint16(request)), request[2], byte(currentGuildMileageItemAction), code)
}
