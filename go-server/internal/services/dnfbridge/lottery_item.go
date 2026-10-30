package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentLotteryPendingTTL = 30 * time.Second

func currentLotterySupportedType(stackableType string) bool {
	return stackableType == "[random upgradable legacy]" || stackableType == "[upgradable legacy]" || stackableType == "[monster tarot card]"
}

func decodeCurrentLotteryRequest(body []byte) (bool, int16, error) {
	if len(body) < 4 {
		return false, 0, fmt.Errorf("lottery body length %d is below 4", len(body))
	}
	mode := binary.LittleEndian.Uint16(body[:2])
	slot := int16(binary.LittleEndian.Uint16(body[2:4]))
	if mode > 1 || slot < 0 {
		return false, 0, fmt.Errorf("lottery mode=%d slot=%d is invalid", mode, slot)
	}
	return mode == 1, slot, nil
}

func buildCurrentLotteryPhaseStartBody() []byte {
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint16(math.MaxUint16)
	writer.writeUint16(0)
	writer.writeUint32(0)
	writer.writeUint32(0)
	return writer.bytes()
}

func buildCurrentLotteryErrorBody() []byte {
	body := buildCurrentLotteryPhaseStartBody()
	body[0] = 0
	return body
}

// Latest CMD27 consumes a complete reward immediately; the legacy phase
// placeholder is not a valid successful packet for its 119-byte item reader.
func (s *Service) currentLotteryErrorBody() []byte {
	if s.options.currentClientVersion == "official-24.9" {
		return []byte{0, 0x13}
	}
	return buildCurrentLotteryErrorBody()
}

func (s *Service) handleCurrentLotteryItem(session *gameSession, body []byte) error {
	wantsDouble, slot, err := decodeCurrentLotteryRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-lottery-item-rejected", "body_len", len(body), "reason", err)
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), s.currentLotteryErrorBody(), dnfproto.DefaultChannelClassification)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), s.currentLotteryErrorBody(), dnfproto.DefaultChannelClassification)
	}
	request := currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: slot}
	definition, err := s.prepareCurrentBooster(ctx, session, catalog, request)
	if err != nil || !currentLotterySupportedType(definition.StackableType) {
		s.logGameEvent(session, "game-lottery-item-rejected",
			"slot", slot,
			"stackable_type", definition.StackableType,
			"reason", err)
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), s.currentLotteryErrorBody(), dnfproto.DefaultChannelClassification)
	}
	doubleReward := false
	now := time.Now().UTC()
	if wantsDouble && definition.StackableType != "[monster tarot card]" {
		_, account, character, premiumErr := s.currentPremiumServiceRecords(ctx, session)
		doubleReward = premiumErr == nil &&
			premium.Active(account, premium.DevilSlotType(premium.DevilSlotDoubleJar), now) &&
			premium.DailyUsage(character, premium.DevilSlotDoubleJar, now) < premium.DailyLimit(premium.DevilSlotDoubleJar)
	}
	session.lottery.mu.Lock()
	session.lottery.pending = true
	session.lottery.pendingSlot = slot
	session.lottery.pendingDouble = doubleReward
	session.lottery.pendingAt = now
	session.lottery.mu.Unlock()
	if s.options.currentClientVersion == "official-24.9" {
		return s.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0})
	}
	s.logGameEvent(session, "game-lottery-item-phase-start",
		"slot", slot,
		"source_item", definition.Source.ItemID,
		"requested_double", wantsDouble,
		"double_reward", doubleReward,
		"state_source", "current_exe_op27_u16_mode_i16_slot_and_runtime_pvf")
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketUseLotteryItem),
		buildCurrentLotteryPhaseStartBody(),
		dnfproto.DefaultChannelClassification,
	)
}

func (s *Service) handleCurrentLotteryOverflowConfirm(session *gameSession, body []byte) error {
	if session == nil || !bytes.Equal(body, []byte{1, 0x1b, 0}) {
		return nil
	}
	now := time.Now().UTC()
	session.lottery.mu.Lock()
	pending := session.lottery.pending && now.Sub(session.lottery.pendingAt) <= currentLotteryPendingTTL
	slot := session.lottery.pendingSlot
	doubleReward := session.lottery.pendingDouble
	session.lottery.pending = false
	session.lottery.pendingDouble = false
	session.lottery.mu.Unlock()
	if !pending {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	request := currentBoosterOpenRequest{
		AllowLotteryGold:    true,
		Kind:                currentBoosterRequestRandom,
		SourceSlot:          slot,
		RewardMultiplier:    1,
		ConsumePremiumDaily: doubleReward,
		PremiumDailySlot:    premium.DevilSlotDoubleJar,
	}
	if doubleReward {
		request.RewardMultiplier = 2
	}
	definition, err := s.prepareCurrentBooster(ctx, session, catalog, request)
	if err != nil || !currentLotterySupportedType(definition.StackableType) {
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), s.currentLotteryErrorBody(), dnfproto.DefaultChannelClassification)
	}
	result, err := s.commitCurrentBooster(ctx, session, catalog, definition, request)
	if err != nil {
		s.logGameEvent(session, "game-lottery-item-open-rejected", "slot", slot, "double_reward", doubleReward, "reason", err)
		return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), s.currentLotteryErrorBody(), dnfproto.DefaultChannelClassification)
	}
	resultBody, err := s.buildCurrentLotteryResultBody(ctx, session, catalog, result)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseLotteryItem), resultBody, dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	official := s.options.currentClientVersion == "official-24.9"
	if result.GoldAmount > 0 || (official && definition.LotteryGoldCost > 0) {
		var entry currentItemListEntry
		entry.patchCore(0, 0, result.GoldAfter)
		var writer packetWriter
		writer.writeByte(dnfrepo.MainInventoryListType)
		writer.writeUint16(1)
		writer.writeBytes(entry.data[:])
		if err := s.sendGameUpperRawClass(session, 14, writer.bytes(), 0); err != nil {
			return err
		}
	}
	containerLists := result.ChangedLists
	if official {
		// CMD27 automatically continues using its cached source object. A full
		// main-inventory NOTI13 destroys that object between consecutive opens.
		// Keep its identity with the committed source/material/reward deltas.
		plan := buildItemGrantNoticePlan(result.GrantSlots)
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "use_lottery_item", plan.Delta); err != nil {
			return err
		}
		containerLists = plan.ContainerLists
	} else if err := s.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"use_lottery_item",
		[]alignedcmd.ItemSlotRefresh{{ListType: dnfrepo.MainInventoryListType, SlotIndex: result.SourceSlot}},
	); err != nil {
		return err
	}
	for _, listType := range containerLists {
		listBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
		if !ok {
			return errCurrentBoosterOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), listBody); err != nil {
			return err
		}
	}
	if result.FateCurrencyChanged {
		// CMD27 first presents the committed grant delta. Replace the account
		// container count with the authoritative balance after that addition.
		if err := s.refreshCurrentFateSnapshot(session); err != nil {
			return err
		}
	}
	if doubleReward {
		if err := s.sendCurrentPremiumServiceState(session, "lottery_double_reward_after_commit"); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-lottery-item-open-committed",
		"slot", slot,
		"source_item", result.SourceItemID,
		"source_remaining", result.SourceRemaining,
		"double_reward", doubleReward,
		"gold_amount", result.GoldAmount,
		"gold_after", result.GoldAfter,
		"reward_count", len(result.Rewards),
		"rewards", fmt.Sprint(result.Rewards))
	return nil
}

func (s *Service) buildCurrentLotteryResultBody(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	result currentBoosterCommitResult,
) ([]byte, error) {
	if result.FateCurrencyChanged && s.options.currentClientVersion == "official-24.9" {
		// This is the same actual account-list slot used by the Fate snapshot,
		// not a main-inventory placeholder. CMD27 derives list44 from item ID.
		// Its +0x4C item method and result UI both consume the granted delta,
		// including zero when the weekly cap prevented any new acquisition.
		entry := currentFateItemRow(result.FateGranted)
		var writer packetWriter
		writer.writeByte(1)
		writer.writeUint16(uint16(result.SourceSlot))
		writer.writeBytes(entry.data[:])
		writer.writeUint32(0)
		return writer.bytes(), nil
	}
	if result.GoldAmount > 0 && s.options.currentClientVersion == "official-24.9" {
		// Native CMD27 reads source u16, a full raw77 reward row, then
		// u32 currency amount. A zero-template/zero-count row selects the
		// currency presentation branch; op14 follows with the absolute wallet.
		var entry currentItemListEntry
		entry.patchCore(0, 0, 0)
		var writer packetWriter
		writer.writeByte(1)
		writer.writeUint16(uint16(result.SourceSlot))
		writer.writeBytes(entry.data[:])
		writer.writeUint32(result.GoldAmount)
		return writer.bytes(), nil
	}
	if len(result.Rewards) == 0 {
		return s.currentLotteryErrorBody(), nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil || session == nil {
		return nil, errCurrentBoosterOwnerUnavailable
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		return nil, errCurrentBoosterOwnerUnavailable
	}
	rewards := append([]currentBoosterGrantedReward(nil), result.Rewards...)
	sort.Slice(rewards, func(i, j int) bool { return rewards[i].ItemID < rewards[j].ItemID })
	if s.options.currentClientVersion == "official-24.9" {
		display, delta, displayStack, selected := selectCurrentLotteryRewardDelta(rewards, result.RewardSlotDeltas, &inventory)
		if !selected {
			return s.currentLotteryErrorBody(), nil
		}
		definition, err := catalog.ResolveItem(display.ItemID)
		if err != nil {
			return nil, err
		}
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, delta.SlotIndex, displayStack)
		if definition.Kind == dungeonDropItemStackable {
			// The native reader ADDs this value. Use one real slot touched by this
			// grant and that slot's exact delta, never an aggregate across split slots.
			entry.patchCore(delta.SlotIndex, display.ItemID, delta.Delta)
		}
		var writer packetWriter
		writer.writeByte(1)
		writer.writeUint16(uint16(result.SourceSlot))
		writer.writeBytes(entry.data[:])
		writer.writeUint32(0)
		return writer.bytes(), nil
	}
	display := rewards[0]
	displaySlot := int16(-1)
	displayStack := dnfrepo.ItemStack{}
	for key, stack := range inventory.Slots {
		listType, slot, parsed := parseSceneInventorySlotKey(key)
		if parsed && listType == dnfrepo.MainInventoryListType && stack.ItemID == int64(display.ItemID) {
			if displaySlot < 0 || slot < displaySlot {
				displaySlot = slot
				displayStack = stack
			}
		}
	}
	if displaySlot < 0 {
		return s.currentLotteryErrorBody(), nil
	}
	definition, err := catalog.ResolveItem(display.ItemID)
	if err != nil {
		return nil, err
	}
	displayValue := display.Count
	if definition.Kind != dungeonDropItemStackable {
		displayValue = 0
	}
	durability := uint16(0)
	if raw := displayStack.Extra["durability"]; raw != "" {
		if value, parseErr := strconv.ParseUint(raw, 10, 16); parseErr == nil {
			durability = uint16(value)
		}
	}
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint16(uint16(result.SourceSlot))
	writer.writeUint16(uint16(displaySlot))
	writer.writeUint32(display.ItemID)
	writer.writeUint32(displayValue)
	writer.writeUint16(durability)
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeUint16(0)
	if definition.Kind != dungeonDropItemStackable {
		writer.writeByte(0xef)
		writer.writeUint32(25)
		writer.writeBytes(make([]byte, 25))
	}
	writer.writeBytes([]byte{0, 0, 0})
	return writer.bytes(), nil
}

// selectCurrentLotteryRewardDelta chooses the one CMD27 row that can be shown.
// When the grant split one reward across slots, the row corresponds to the
// largest actual slot delta (lowest slot on a tie). That is deliberately a real
// partial grant rather than a fabricated aggregate ADD to an arbitrary slot;
// the subsequent op14 rows still publish every touched slot's absolute total.
func selectCurrentLotteryRewardDelta(
	rewards []currentBoosterGrantedReward,
	deltas []currentBoosterRewardSlotDelta,
	inventory *dnfrepo.InventoryRecord,
) (currentBoosterGrantedReward, currentBoosterRewardSlotDelta, dnfrepo.ItemStack, bool) {
	if inventory == nil {
		return currentBoosterGrantedReward{}, currentBoosterRewardSlotDelta{}, dnfrepo.ItemStack{}, false
	}
	for _, reward := range rewards {
		var selected currentBoosterRewardSlotDelta
		var selectedStack dnfrepo.ItemStack
		found := false
		for _, delta := range deltas {
			if delta.RewardItemID != reward.ItemID || delta.ListType != dnfrepo.MainInventoryListType || delta.Delta == 0 {
				continue
			}
			key := currentCeraShopInventorySlotKey(delta.ListType, delta.SlotIndex)
			stack, present := inventory.Slots[key]
			if !present || stack.ItemID != int64(delta.RewardItemID) || stack.Count < int64(delta.Delta) {
				continue
			}
			if !found || delta.Delta > selected.Delta || (delta.Delta == selected.Delta && delta.SlotIndex < selected.SlotIndex) {
				selected, selectedStack, found = delta, stack, true
			}
		}
		if found {
			return reward, selected, selectedStack, true
		}
	}
	return currentBoosterGrantedReward{}, currentBoosterRewardSlotDelta{}, dnfrepo.ItemStack{}, false
}
