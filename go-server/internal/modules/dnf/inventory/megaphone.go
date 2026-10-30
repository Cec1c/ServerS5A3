package inventory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/itemusage"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrMegaphone = errors.New("megaphone source or eligibility is invalid")
var ErrMegaphoneReplay = errors.New("megaphone request already consumed or stale")

// The native CMD17 type selects a specific item, not any available horn.
func MegaphoneItem(mode byte) (int64, string) {
	switch mode {
	case 11:
		return 36, "stackable/cash/megaphone_heartbeat.stk"
	case 13:
		return 37, "stackable/cash/megaphone_echo.stk"
	}
	return 0, ""
}

type MegaphoneDefinition struct {
	ItemID                 int64
	PVFPath, StackableType string
	MinimumLevel           int
	Slots                  itemplacement.SlotPolicy
	Usage                  *itemusage.Definition
	Project                func(r.ItemStack, time.Time) r.ItemStack
}

type MegaphoneCommand struct {
	AccountID   string
	CharacterID uint16
	Mode        byte
	Slot        int16
	Session     string // opaque connection nonce; never client-supplied
	Sequence    uint64
	Now         time.Time
	Definition  MegaphoneDefinition
	Revalidate  func() bool
}

type MegaphoneResult struct {
	Character r.CharacterRecord
	Stack     r.ItemStack
	Remaining int64
}

// ConsumeMegaphone pins source identity, receipt and inventory in one asset
// transaction. A bounded connection watermark rejects retries without treating
// a later identical message as a duplicate. Delivery is best-effort after commit.
func (o *Owner) ConsumeMegaphone(ctx context.Context, cmd MegaphoneCommand) (MegaphoneResult, error) {
	if o == nil || o.characters == nil || o.repo == nil || o.assets == nil {
		return MegaphoneResult{}, ErrCharacterAssetTxnRequired
	}
	d := cmd.Definition
	item, path := MegaphoneItem(cmd.Mode)
	nonce, nonceErr := hex.DecodeString(cmd.Session)
	if item == 0 || d.ItemID != item || d.PVFPath != path || d.StackableType != "[waste]" ||
		d.MinimumLevel < 1 || cmd.CharacterID == 0 || strings.TrimSpace(cmd.AccountID) == "" || cmd.Now.IsZero() ||
		cmd.Sequence == 0 || cmd.Sequence > math.MaxInt64 || nonceErr != nil || len(nonce) != 32 ||
		cmd.Slot < 3 || cmd.Slot > 288 || d.Slots.ListType != r.MainInventoryListType {
		return MegaphoneResult{}, ErrMegaphone
	}
	// Both approved item descriptions require an account with a level-86 actor.
	// Read the account directory before entering the single-character UOW.
	characters, err := o.characters.ListByAccount(ctx, cmd.AccountID, 0)
	if err != nil {
		return MegaphoneResult{}, err
	}
	eligible := false
	for _, c := range characters {
		eligible = eligible || c.AccountID == cmd.AccountID && c.Level >= 86
	}
	if !eligible {
		return MegaphoneResult{}, ErrMegaphone
	}
	id := strconv.Itoa(int(cmd.CharacterID))
	var result MegaphoneResult
	err = o.withinCharacterAssetTransaction(ctx, id, func(tx *Owner) error {
		if cmd.Revalidate != nil && !cmd.Revalidate() {
			return ErrMegaphone
		}
		c, found, err := tx.characters.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || c.AccountID != cmd.AccountID || c.Level < d.MinimumLevel || c.Stats["chat_ban_end_time"] > cmd.Now.Unix() {
			return ErrMegaphone
		}
		a, b := int64(binary.LittleEndian.Uint64(nonce[:8])), int64(binary.LittleEndian.Uint64(nonce[8:16]))
		if c.Stats["megaphone_session_a"] == a && c.Stats["megaphone_session_b"] == b &&
			c.Stats["megaphone_sequence"] >= int64(cmd.Sequence) {
			return ErrMegaphoneReplay
		}
		record, found, err := tx.loadInventoryWithContainerState(ctx, id)
		if err != nil {
			return err
		}
		if !found || !d.Slots.ForInventory(&record).Contains(r.MainInventoryListType, cmd.Slot) {
			return ErrSlotNotFound
		}
		record = r.CloneInventory(record)
		key := slotKey(r.MainInventoryListType, cmd.Slot)
		stack, found := record.Slots[key]
		if !found || stack.ItemID != item || stack.Count <= 0 || stack.Count > math.MaxInt32 || itemlock.IsLockedAt(stack, cmd.Now) {
			return ErrMegaphone
		}
		if d.Project != nil {
			stack = d.Project(cloneStack(stack), cmd.Now)
		}
		expires := firstExtraInt(stack.Extra, 0, "expire_time", "expire_unix")
		if (!stack.ExpireAt.IsZero() && !cmd.Now.Before(stack.ExpireAt)) || expires > 0 && expires <= cmd.Now.Unix() {
			return ErrMegaphone
		}
		c = r.CloneCharacter(c)
		if c.Stats == nil {
			c.Stats = make(map[string]int64)
		}
		if d.Usage != nil {
			if int64(d.Usage.ItemID) != item {
				return ErrMegaphone
			}
			if err := itemusage.Consume(&c, *d.Usage, cmd.Now); err != nil {
				return err
			}
		}
		c.Stats["megaphone_session_a"], c.Stats["megaphone_session_b"], c.Stats["megaphone_sequence"] = a, b, int64(cmd.Sequence)
		stack.Count--
		if stack.Count == 0 {
			delete(record.Slots, key)
		} else {
			updateStackRawAmount(&stack)
			record.Slots[key] = stack
		}
		c.UpdatedAt, record.UpdatedAt = cmd.Now, cmd.Now
		if err := r.SaveCharacterFields(ctx, tx.characters, c, r.CharacterFieldStats); err != nil {
			return err
		}
		if err := r.SaveInventoryFields(ctx, tx.repo, record, r.InventoryFieldSlots); err != nil {
			return err
		}
		if cmd.Revalidate != nil && !cmd.Revalidate() {
			return ErrMegaphone
		}
		result = MegaphoneResult{Character: c, Stack: stack, Remaining: stack.Count}
		return nil
	})
	if err != nil {
		return MegaphoneResult{}, err
	}
	return result, nil
}
