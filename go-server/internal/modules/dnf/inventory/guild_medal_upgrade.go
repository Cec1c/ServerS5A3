package inventory

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Dedicated current-EXE branch (0x25fe330); do not interpret list38 as a high
// mode byte or route it through ordinary equipment inventory. This sender
// has no protection-ticket input; passive rate-buff applicability remains
// unverified, so this candidate uses the flag table base rate only.
// No persistent remapping through list0 occurs.
func handleGuildMedalUpgrade(ctx context.Context, req alignedcmd.Request) alignedcmd.Result {
	reject := func(code byte, reason string) alignedcmd.Result {
		return alignedcmd.Result{Handled: true, ResponseAllowed: true, Operation: "guild_medal_upgrade", Reason: reason, UpperResponses: []alignedcmd.UpperResponse{class1Response(req.Opcode, buildUpgradeItemErrorAck(code))}}
	}
	b := req.Body
	if len(b) < 20 || b[0] != 0 || b[1] != 38 {
		return reject(upgradeErrorInvalidTarget, "flag header invalid")
	}
	nameLength := binary.LittleEndian.Uint32(b[12:16])
	if nameLength > 256 || uint64(nameLength)+20 != uint64(len(b)) || binary.LittleEndian.Uint16(b[10:12]) != 0xffff || binary.LittleEndian.Uint32(b[len(b)-4:]) != 0xffffffff {
		return reject(upgradeErrorInvalidTarget, "flag packet length/ticket/trailer invalid")
	}
	normalized := append([]byte(nil), b...)
	normalized[1] = 0
	parsed, err := DecodeUpgradeItemRequest(normalized)
	if err != nil || parsed.TargetSlotIndex < 0 || parsed.TargetSlotIndex > 48 || parsed.MaterialSlotIndex < 121 || parsed.MaterialSlotIndex > 232 || parsed.TargetItemTemplateID <= 0 {
		return reject(upgradeErrorInvalidTarget, "flag endpoint invalid")
	}
	cmd := NewUpgradeCommand(req, parsed)
	cmd.TargetListType = 38
	owner, err := NewOwner(req.Repositories)
	if err != nil {
		return reject(upgradeErrorInvalidTarget, err.Error())
	}
	result, err := owner.upgradeGuildMedal(ctx, cmd, req.GuildMedalUpgradeResolver)
	if err != nil {
		return reject(upgradeErrorInvalidMaterial, "flag transaction failed: "+err.Error())
	}
	if !result.Success {
		return reject(result.ErrorCode, "flag target/material/policy rejected")
	}
	ack := buildUpgradeItemSuccessAck(result)
	// 0x1fe03df reads this byte; 0x1fe0706..71b selects target list+slot.
	ack[14] = 38
	// Native 0x1fe0e3b..0x1fe0e53 consumes this common slot/count tail.
	// This sender has no NPC-conversion material: neutral slot -1, count 0.
	ack = append(ack, 0xff, 0xff, 0, 0, 0, 0)
	main := []commonItemListEntry{{slot: cmd.MaterialSlotIndex, stack: result.MaterialUpdatedStack}}
	responses := []alignedcmd.UpperResponse{class1Response(req.Opcode, ack), class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(0, main)), class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(38, []commonItemListEntry{{slot: cmd.TargetSlotIndex, stack: result.TargetUpdatedStack}}))}
	return alignedcmd.Result{Handled: true, ResponseAllowed: true, Operation: "guild_medal_upgrade", Reason: fmt.Sprintf("flag committed slot=%d level=%d->%d outcome=%d material_remaining=%d gold=%d", cmd.TargetSlotIndex, result.OldLevel, result.NewLevel, result.ResultCode, result.MaterialRemainingStackCount, result.GoldCost), UpperResponses: responses}
}

func (o *Owner) upgradeGuildMedal(ctx context.Context, cmd Command, resolve alignedcmd.GuildMedalUpgradeResolver) (UpgradeResult, error) {
	result := newUpgradeResult(cmd, upgradeErrorInvalidTarget)
	if o == nil || o.repo == nil || o.characters == nil || cmd.SelectedCharacterID == 0 || strings.TrimSpace(cmd.AccountID) == "" {
		return result, ErrOwnerUnavailable
	}
	id := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	if !o.inItemTransaction {
		err := o.withinCharacterAssetTransaction(ctx, id, func(tx *Owner) error {
			var err error
			result, err = tx.upgradeGuildMedal(ctx, cmd, resolve)
			return err
		})
		return result, err
	}
	character, ok, err := o.characters.Load(ctx, id)
	if err != nil {
		return result, err
	}
	if !ok || character.AccountID != cmd.AccountID {
		return result, nil
	}
	inv, ok, err := o.repo.Load(ctx, id)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, nil
	}
	inv = dnfrepo.CloneInventory(inv)
	key := slotKey(38, cmd.TargetSlotIndex)
	target, ok := inv.Slots[key]
	if !ok || target.Count != 1 || target.ItemID != int64(cmd.TargetItemTemplateID) || len(target.RawEntry) != currentItemListEntrySize {
		return result, nil
	}
	if int64(binary.LittleEndian.Uint32(target.RawEntry[2:6])) != target.ItemID {
		return result, nil
	}
	if isEquipmentLocked(target) {
		result.ErrorCode = upgradeErrorLocked
		return result, nil
	}
	if !target.ExpireAt.IsZero() && !target.ExpireAt.After(time.Now()) {
		return result, nil
	}
	old := upgradeLevelOf(target)
	result.OldLevel = old
	result.NewLevel = old
	result.ErrorCode = upgradeErrorInvalidMaterial
	if resolve == nil {
		return result, nil
	}
	policy, err := resolve(target.ItemID, old)
	if err != nil {
		return result, err
	}
	if policy.MaterialID <= 0 || policy.MaterialCount <= 0 || policy.SuccessWeight < 0 || policy.SuccessWeight > 100000 || policy.PenaltyType < 0 || policy.PenaltyType > 2 || policy.PenaltyValue < 0 || old >= 31 {
		return result, nil
	}
	// The NPC sender writes an absolute main-bag index. Certificates are
	// quest items (177..232), not ordinary material-page items (121..176).
	// Accept only the page resolved from the authoritative material policy.
	validPage := (policy.MaterialSlotStart == 121 && policy.MaterialSlotEnd == 176) ||
		(policy.MaterialSlotStart == 177 && policy.MaterialSlotEnd == 232)
	if !validPage || cmd.MaterialSlotIndex < policy.MaterialSlotStart || cmd.MaterialSlotIndex > policy.MaterialSlotEnd {
		return result, nil
	}
	materialKey := slotKey(0, cmd.MaterialSlotIndex)
	material, ok := inv.Slots[materialKey]
	if !ok || material.ItemID != policy.MaterialID || material.Count < policy.MaterialCount || (!material.ExpireAt.IsZero() && !material.ExpireAt.After(time.Now())) {
		return result, nil
	}
	succeeded := rand.Intn(100000) < policy.SuccessWeight
	next := old
	code := byte(upgradeResultCodeFailNoChange)
	if succeeded {
		next++
		code = upgradeResultCodeSuccess
	} else if policy.PenaltyType == 2 {
		next = byte(max(0, int(old)-policy.PenaltyValue))
		code = upgradeResultCodeDowngrade
	}
	target = cloneStack(target)
	setUpgradeLevel(&target, next)
	inv.Slots[key] = target
	material = cloneStack(material)
	material.Count -= policy.MaterialCount
	result.MaterialRemainingStackCount = material.Count
	result.MaterialUpdated = true
	if material.Count == 0 {
		delete(inv.Slots, materialKey)
	} else {
		updateStackRawAmount(&material)
		inv.Slots[materialKey] = material
		result.MaterialUpdatedStack = material
	}
	inv.UpdatedAt = time.Now()
	if err := dnfrepo.SaveInventoryFields(ctx, o.repo, inv, dnfrepo.InventoryFieldSlots); err != nil {
		return result, err
	}
	result.Success = true
	result.ErrorCode = 0
	result.Changed = true
	result.NewLevel = next
	result.ResultCode = code
	result.UpgradeSucceeded = succeeded
	result.TargetUpdatedStack = target
	result.UpdatedGold = character.Stats["gold"]
	return result, nil
}
