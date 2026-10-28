package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeon"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/vending"
	"strconv"
	"strings"
	"time"
)

type vendingOutput struct{ ID, Weight, Count int64 }

func parseVendingGroup(d *doc.Document, group uint32, coinID int64) (int64, []vendingOutput, bool) {
	var number, material, quantity int64
	var outputs []vendingOutput
	match := func() (int64, []vendingOutput, bool) {
		return quantity, outputs, number == int64(group) && material == coinID && quantity > 0 && len(outputs) > 0
	}
	for _, section := range d.Sections {
		tokens := d.Tokens[section.Start:section.End]
		switch strings.Trim(section.Name, "[] ") {
		case "group num":
			if _, _, ok := match(); ok {
				return match()
			}
			number, material, quantity = 0, 0, 0
			outputs = nil
			if len(tokens) > 0 {
				number = tokens[0].Int
			}
		case "material":
			if len(tokens) == 2 {
				material, quantity = tokens[0].Int, tokens[1].Int
			}
		case "output":
			if len(tokens)%4 != 0 {
				return 0, nil, false
			}
			for i := 0; i < len(tokens); i += 4 {
				if tokens[i].Int > 0 && tokens[i+1].Int > 0 && tokens[i+2].Int > 0 {
					outputs = append(outputs, vendingOutput{tokens[i].Int, tokens[i+1].Int, tokens[i+2].Int})
				}
			}
		}
	}
	return match()
}

// Native 3302650 writes selected machine index+1:u32, drawCount:u32(1),
// coinSlot:u16. The first field selects etc/vendingmachine.lst; each current
// machine's output table has its own [group num] 1. It is not that group ID.
// Current official-24.9 reader 1FE64D0 consumes slot:u16, remaining:u32, rewardId/count:u32,
// two ignored u32 fields, and a u16 embedded-item count. The embedded rows
// drive the native settlement window; later op14 inventory refreshes do not
// populate that window. Each main-inventory row is listType:u8 + raw[119].
func (s *Service) handleCurrentVendingMachine(session *gameSession, body []byte) error {
	const opcode = uint16(dnfenum.CmdPacketUseVendingMachine)
	refuse := func(e error) error {
		s.logGameEvent(session, "game-black-diamond-vending-rejected", "error", e)
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	if session == nil || session.selectedCharacterID == 0 || len(body) != 10 || binary.LittleEndian.Uint32(body[4:]) != 1 {
		return refuse(fmt.Errorf("invalid vending request"))
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return refuse(fmt.Errorf("vending requires town"))
	}
	machineID := binary.LittleEndian.Uint32(body)
	slot := int16(binary.LittleEndian.Uint16(body[8:]))
	if slot < 0 {
		return refuse(fmt.Errorf("negative coin slot"))
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return refuse(fmt.Errorf("inventory unavailable"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, e := repos.Inventory.Load(ctx, characterID)
	if e != nil {
		return e
	}
	coin := inventory.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || coin.Count <= 0 || coin.ItemID <= 0 {
		return refuse(fmt.Errorf("diamond coin source invalid"))
	}
	s.initialEquipmentMu.Lock()
	a, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if e != nil {
		return e
	}
	text, e := a.ReadText("etc/vendingmachine.lst")
	if e != nil {
		return e
	}
	list, e := doc.Parse("etc/vendingmachine.lst", text)
	if e != nil {
		return e
	}
	var cost int64
	var outputs []vendingOutput
	for _, entry := range doc.ParseList(list) {
		if entry.ID != int64(machineID) {
			continue
		}
		text, e := a.ReadText("etc/" + entry.Path)
		if e != nil {
			continue
		}
		d, e := doc.Parse(entry.Path, text)
		if e != nil {
			continue
		}
		if quantity, rows, ok := parseVendingGroup(d, 1, coin.ItemID); ok {
			cost, outputs = quantity, rows
			break
		}
	}
	if cost == 0 {
		return refuse(fmt.Errorf("no matching PVF vending group"))
	}
	var weight int64
	for _, row := range outputs {
		weight += row.Weight
	}
	if weight <= 0 || weight > int64(^uint32(0)>>1) {
		return refuse(fmt.Errorf("vending weight invalid"))
	}
	draw, e := s.chooseDungeonIndex(int(weight))
	if e != nil {
		return e
	}
	var selected vendingOutput
	for _, row := range outputs {
		draw -= int(row.Weight)
		if draw < 0 {
			selected = row
			break
		}
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return e
	}
	coinDefinition, e := catalog.ResolveItem(uint32(coin.ItemID))
	if e != nil {
		return refuse(e)
	}
	now := s.gameplayNow()
	if e = validateCurrentBoosterSourceExpiration(coin, coinDefinition, now); e != nil {
		return refuse(fmt.Errorf("vending coin expired: %w", e))
	}
	definition, e := catalog.ResolveItem(uint32(selected.ID))
	if e != nil {
		return e
	}
	definition, e = currentPVFItemDefinitionForGrantAt(definition, now)
	if e != nil {
		return e
	}
	stack, e := buildCurrentDungeonPickupStack(definition.SlotStart, definition, uint32(selected.Count))
	if e != nil {
		return e
	}
	stack.Extra["source"] = "black_diamond_vending"
	reward := dungeon.CardItemReward{Stack: stack, Stackable: definition.Kind == dungeonDropItemStackable, SlotStart: definition.SlotStart, SlotEnd: definition.SlotEnd, ExpireAt: definition.ExpirationDate}
	policy := currentPVFItemPlacementPolicy(definition).ForGrant()
	reward.AllowQuickSlots, reward.PreferQuickSlots, reward.StackLimit = policy.AllowQuickSlots, policy.PreferQuickSlots, definition.StackLimit
	if !reward.Stackable && reward.Stack.Count != 1 {
		return refuse(fmt.Errorf("non-stackable vending count unsupported"))
	}
	result, e := vending.Use(ctx, repos, vending.Command{AccountID: s.accountIDForSession(session), CharacterID: characterID, CoinID: coin.ItemID, CoinCount: cost, CoinSlot: slot, Reward: reward, Now: now, ValidateCoin: func(stack repo.ItemStack, at time.Time) error {
		return validateCurrentBoosterSourceExpiration(stack, coinDefinition, at)
	}, Project: func(stack repo.ItemStack, expiry time.Time) (repo.ItemStack, error) {
		stack, _ = applyCurrentStackableExpirationAt(stack, expiry, now)
		return stack, nil
	}})
	if e != nil {
		return refuse(e)
	}
	entry := currentItemListEntryFromStack(0, result.RewardSlot, result.RewardStack)
	ack := make([]byte, 25+1+len(entry.data))
	ack[0] = 1
	binary.LittleEndian.PutUint16(ack[1:], uint16(slot))
	binary.LittleEndian.PutUint32(ack[3:], uint32(result.CoinRemaining))
	binary.LittleEndian.PutUint32(ack[7:], uint32(selected.ID))
	binary.LittleEndian.PutUint32(ack[11:], uint32(selected.Count))
	binary.LittleEndian.PutUint16(ack[23:], 1)
	copy(ack[26:], entry.data[:])
	if e = s.sendGameUpperRawClass(session, opcode, ack, 1); e != nil {
		return e
	}
	s.logGameEvent(session, "game-black-diamond-vending-used", "coin_id", coin.ItemID, "item_id", selected.ID, "count", selected.Count, "reward_slot", result.RewardSlot)
	return s.sendSelectedIncrementalItemSlotRefreshes(session, "black_diamond_vending_after_ack", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}, {ListType: 0, SlotIndex: result.RewardSlot}})
}
