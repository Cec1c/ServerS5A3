package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

// ReviveCoinUseCommand 对应 op30：请求只有源格，没有 op44 的实例值。
// Definition 由桥接从实际 PVF 解析，不能拿背包 Extra 充当物品身份。
type ReviveCoinUseCommand struct {
	AccountID   string
	CharacterID uint16
	SlotIndex   int16
	Definition  alignedcmd.StackableUseProvenanceResolution
	Now         time.Time
}

// UseReviveCoinItem 每次仅消费一件42并增加一枚钱包币；不改角色数值和技能。
// 角色归属检查、扣源物品及钱包写入共用角色资产事务，失败整笔回滚。
func (o *Owner) UseReviveCoinItem(ctx context.Context, cmd ReviveCoinUseCommand) (UseStackableResult, error) {
	if o == nil || o.repo == nil || o.characters == nil || o.assets == nil {
		return UseStackableResult{}, ErrCharacterAssetTxnRequired
	}
	if cmd.CharacterID == 0 || strings.TrimSpace(cmd.AccountID) == "" || cmd.Now.IsZero() {
		return UseStackableResult{}, ErrCharacterRequired
	}
	definition := cmd.Definition
	identity := dnfrepo.ItemStack{ItemID: definition.ItemID, Extra: map[string]string{
		"pvf_path": definition.PVFPath, "stackable_type": definition.StackableType,
	}}
	if !definition.Found || !dnfrevivecoin.IsConsumable(identity) {
		return UseStackableResult{}, fmt.Errorf("复活币物品身份未经PVF确认: item=%d", definition.ItemID)
	}
	if cmd.SlotIndex < 3 || cmd.SlotIndex > 288 {
		return UseStackableResult{}, ErrSlotNotFound
	}
	id := strconv.FormatUint(uint64(cmd.CharacterID), 10)
	var result UseStackableResult
	err := o.withinCharacterAssetTransaction(ctx, id, func(tx *Owner) error {
		character, found, err := tx.characters.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != id || strings.TrimSpace(character.AccountID) != strings.TrimSpace(cmd.AccountID) {
			return fmt.Errorf("复活币使用角色归属不匹配: character=%s", id)
		}
		record, found, err := tx.loadInventoryWithContainerState(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		if !dnfplacement.SlotOpen(listTypeMain, cmd.SlotIndex, record.ContainerState) {
			return ErrMoveSlotLocked
		}
		record = dnfrepo.CloneInventory(record)
		key := slotKey(listTypeMain, cmd.SlotIndex)
		source, found := record.Slots[key]
		if !found || source.Count <= 0 {
			return ErrSlotNotFound
		}
		if source.ItemID != dnfrevivecoin.ConsumableItemID {
			return ErrItemMismatch
		}
		expire := firstExtraInt(source.Extra, 0, "expire_time", "expire_unix")
		if (!source.ExpireAt.IsZero() && !cmd.Now.Before(source.ExpireAt)) || (expire > 0 && expire <= cmd.Now.Unix()) {
			return fmt.Errorf("复活币源物品已过期: slot=%d", cmd.SlotIndex)
		}
		remaining := source.Count - 1
		if remaining == 0 {
			delete(record.Slots, key)
		} else {
			source = cloneStack(source)
			source.Count = remaining
			updateStackRawAmount(&source)
			record.Slots[key] = source
		}
		total, err := dnfrevivecoin.Grant(&record, 1, "current_exe_op30_coin_general")
		if err != nil {
			return err
		}
		record.UpdatedAt = cmd.Now
		if err := dnfrepo.SaveInventoryFields(ctx, tx.repo, record, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		result = UseStackableResult{CharacterID: id, AccountID: cmd.AccountID, ListType: listTypeMain, SlotIndex: cmd.SlotIndex,
			ItemID: source.ItemID, PreviousCount: remaining + 1, RemainingCount: remaining, Changed: true,
			PVFPath: definition.PVFPath, StackableType: definition.StackableType,
			ReviveCoinWalletUpdated: true, ReviveCoinWalletTotal: total}
		return nil
	})
	if err != nil {
		return UseStackableResult{}, err
	}
	return result, nil
}
