package wedding

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Keep business reasons separate from any client's protocol result numbers.
// Callers can still classify these as the pre-existing state/item errors.
var (
	ErrTreeMissing    = errors.New("wedding tree is not planted")
	ErrTreeExpired    = errors.New("wedding tree has expired")
	ErrTreeMature     = errors.New("wedding tree is ready to harvest")
	ErrTreeWaterQuota = errors.New("wedding tree daily watering quota exhausted")
	ErrTreeOccupied   = errors.New("wedding tree already planted")
	ErrTreeNotMature  = errors.New("wedding tree growth insufficient")
)

type TreeState struct {
	Index         int64 `json:"index,omitempty"`
	Expires       int64 `json:"expires,omitempty"`
	Growth        int64 `json:"growth,omitempty"`
	WaterDay      int64 `json:"water_day,omitempty"`
	WaterCount    int64 `json:"water_count,omitempty"`
	PlantSerial   int64 `json:"plant_serial,omitempty"`
	HarvestSerial int64 `json:"harvest_serial,omitempty"`
}
type TreeCommand struct {
	Opcode             uint16
	Slot               uint16
	ItemID, Count, Day int64
	Slots              itemplacement.SlotPolicy
	Receipt            ExtensionReceipt
	Draw               func(int64) (int64, error)
	Deliver            func(r.MailboxRepository, r.CharacterRecord, TreeReward, string) (string, error)
}
type TreeResult struct {
	PairResult
	Reward   TreeReward
	MailIDs  map[string]string
	Replayed bool
}

func CurrentTree(state TreeState, rules *ExtensionRules, now time.Time, day int64) (TreeState, error) {
	if rules == nil || now.IsZero() || day <= 0 || state.Index < 0 || state.Expires < 0 || state.Expires > 0xffffffff || state.Growth < 0 || state.WaterDay < 0 || state.WaterCount < 0 || state.WaterCount > 127 || state.PlantSerial < 0 || state.HarvestSerial < 0 {
		return TreeState{}, ErrState
	}
	if state.WaterDay != day {
		state.WaterDay, state.WaterCount = day, 0
	}
	if state.Index == 0 {
		if state.Expires != 0 || state.Growth != 0 {
			return TreeState{}, ErrState
		}
		return state, nil
	}
	tree, known := rules.Trees[state.Index]
	if !known || state.Expires == 0 || state.Growth > tree.MaximumGrowth || state.WaterCount > tree.DailyWater {
		return TreeState{}, ErrState
	}
	if state.Expires <= now.Unix() {
		state.Index, state.Expires, state.Growth = 0, 0, 0
	}
	return state, nil
}

func (o Owner) MutateTree(ctx context.Context, req PairRequest, rules *ExtensionRules, cmd TreeCommand) (TreeResult, error) {
	if rules == nil || cmd.Opcode < 1047 || cmd.Opcode > 1051 || cmd.Day <= 0 || cmd.Receipt.Opcode != cmd.Opcode {
		return TreeResult{}, ErrState
	}
	var reward TreeReward
	mailIDs := map[string]string{}
	pair, replayed, e := o.extensionMutation(ctx, req, cmd.Receipt, func(a, b *r.CharacterRecord, ai, bi *r.InventoryRecord, room *Room, g r.Group) error {
		tree, e := CurrentTree(room.Tree, rules, req.Now, cmd.Day)
		if e != nil {
			return e
		}
		expired := room.Tree.Index != 0 && tree.Index == 0
		if expired {
			switch cmd.Opcode {
			case 1049:
				return errors.Join(ErrItem, ErrTreeExpired)
			case 1050:
				return errors.Join(ErrState, ErrTreeExpired)
			}
		}
		room.Tree = tree
		consumeSlot := func(id int64) error {
			if cmd.ItemID != id || cmd.Slot < 3 || cmd.Slot > 288 || cmd.Slots.ListType != r.MainInventoryListType {
				return ErrItem
			}
			if e := r.BindInventoryContainerState(ctx, g.Settings, ai); e != nil {
				return e
			}
			if !cmd.Slots.ForInventory(ai).Contains(r.MainInventoryListType, int16(cmd.Slot)) {
				return ErrItem
			}
			key := strconv.Itoa(int(r.MainInventoryListType)) + ":" + strconv.Itoa(int(cmd.Slot))
			stack, found := ai.Slots[key]
			if !found || stack.ItemID != id || stack.Count < 1 || !usableAssetAt(stack, req.Now) {
				return ErrItem
			}
			return consume(ai, id, 1, key, req.Now)
		}
		switch cmd.Opcode {
		case 1047:
			stack, found := room.Bag[cmd.Slot]
			if cmd.Slot >= RoomInventoryCapacity || !found || cmd.Count != 1 || stack.Count != 1 || stack.ItemID != cmd.ItemID || !usableAssetAt(stack, req.Now) {
				return ErrItem
			}
			delete(room.Bag, cmd.Slot)
		case 1048:
			index := rules.Seeds[cmd.ItemID]
			rule, known := rules.Trees[index]
			if tree.Index != 0 {
				return errors.Join(ErrState, ErrTreeOccupied)
			}
			if !known || req.Now.Unix() < 0 || rule.Days > (0xffffffff-req.Now.Unix())/86400 || tree.PlantSerial == 0x7fffffffffffffff {
				return ErrState
			}
			if e = consumeSlot(cmd.ItemID); e != nil {
				return e
			}
			tree.Index, tree.Expires, tree.Growth = index, req.Now.Unix()+rule.Days*86400, 0
			tree.PlantSerial++
		case 1049:
			rule, known := rules.Trees[tree.Index]
			gain := rule.Water[cmd.ItemID]
			if !known {
				return errors.Join(ErrItem, ErrTreeMissing)
			}
			if tree.Growth >= rule.MaximumGrowth {
				return errors.Join(ErrItem, ErrTreeMature)
			}
			if gain <= 0 {
				return ErrItem
			}
			if tree.WaterCount >= rule.DailyWater {
				return errors.Join(ErrState, ErrTreeWaterQuota)
			}
			if e = consumeSlot(cmd.ItemID); e != nil {
				return e
			}
			tree.Growth = min(rule.MaximumGrowth, tree.Growth+gain)
			tree.WaterCount++
		case 1050:
			rule, known := rules.Trees[tree.Index]
			if !known {
				return errors.Join(ErrState, ErrTreeMissing)
			}
			if tree.Growth < rule.MaximumGrowth {
				return errors.Join(ErrState, ErrTreeNotMature)
			}
			if cmd.Deliver == nil || tree.HarvestSerial == 0x7fffffffffffffff {
				return ErrState
			}
			var total int64
			for _, row := range rule.Rewards {
				if row.Weight <= 0 || total > 0x7fffffffffffffff-row.Weight {
					return ErrState
				}
				total += row.Weight
			}
			if total <= 0 {
				return ErrState
			}
			draw := cmd.Draw
			if draw == nil {
				draw = func(n int64) (int64, error) {
					v, e := rand.Int(rand.Reader, big.NewInt(n))
					if e != nil {
						return 0, e
					}
					return v.Int64(), nil
				}
			}
			n, e := draw(total)
			if e != nil {
				return e
			}
			if n < 0 || n >= total {
				return ErrState
			}
			for _, row := range rule.Rewards {
				if n < row.Weight {
					reward = row
					break
				}
				n -= row.Weight
			}
			state, e := Read(*a)
			if e != nil {
				return e
			}
			tree.HarvestSerial++
			key := fmt.Sprintf("wedding-tree:%s:%d:%d:%d", a.CharacterID+":"+b.CharacterID, state.MarriedAt, tree.PlantSerial, tree.HarvestSerial)
			for _, c := range []r.CharacterRecord{*a, *b} {
				id, e := cmd.Deliver(g.Mailbox, c, reward, key+":"+c.CharacterID)
				if e != nil {
					return e
				}
				if id == "" {
					return ErrUnavailable
				}
				mailIDs[c.CharacterID] = id
			}
			tree.Growth = 0
		case 1051:
			// The normalized projection hides an expired tree, but its durable
			// row still needs to accept the native clear operation.
			if tree.Index == 0 && !expired {
				return errors.Join(ErrState, ErrTreeMissing)
			}
			tree.Index, tree.Expires, tree.Growth = 0, 0, 0
		}
		room.Tree = tree
		return nil
	})
	if e != nil {
		return TreeResult{}, e
	}
	return TreeResult{PairResult: pair, Reward: reward, MailIDs: mailIDs, Replayed: replayed}, nil
}
