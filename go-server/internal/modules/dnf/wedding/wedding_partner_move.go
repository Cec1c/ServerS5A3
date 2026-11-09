package wedding

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const PartnerMoveItemID int64 = 10002915

type PartnerMoveRules struct {
	ItemID, StackLimit, CooldownMilliseconds int64
	MinimumLevel                             int
	PVFPath                                  string
	Village, Seria                           bool
}
type PartnerMoveLocation struct{ Town, Area, X, Y, Direction int64 }

func (l PartnerMoveLocation) Valid() bool {
	return l.Town > 0 && l.Town <= 255 && l.Area >= 0 && l.Area <= 255 && l.X >= 0 && l.X <= 32767 && l.Y >= 0 && l.Y <= 32767 && l.Direction >= 0 && l.Direction <= 255
}

type PartnerMoveCommand struct {
	Slot               uint16
	FromTown, FromArea int64
	Destination        PartnerMoveLocation
	Slots              itemplacement.SlotPolicy
	Admit              func(r.Group, r.CharacterRecord, PartnerMoveLocation) error
}

func ValidatePartnerMoveVoucher(inventory *r.InventoryRecord, rules PartnerMoveRules, slot uint16, slots itemplacement.SlotPolicy, now time.Time) error {
	if inventory == nil || slot < 3 || slot > 288 || slots.ListType != r.MainInventoryListType || !slots.ForInventory(inventory).Contains(r.MainInventoryListType, int16(slot)) {
		return ErrItem
	}
	key := strconv.Itoa(int(r.MainInventoryListType)) + ":" + strconv.Itoa(int(slot))
	stack, found := inventory.Slots[key]
	if !found || stack.ItemID != rules.ItemID || stack.Count <= 0 || stack.Count > rules.StackLimit || !usableAssetAt(stack, now) {
		return ErrItem
	}
	return nil
}

func LoadPartnerMoveRules(source pvf.Source) (PartnerMoveRules, error) {
	text, e := source.ReadText("stackable/stackable.lst")
	if e != nil {
		return PartnerMoveRules{}, e
	}
	index, e := pvf.Parse("stackable/stackable.lst", text)
	if e != nil {
		return PartnerMoveRules{}, e
	}
	path := ""
	for _, entry := range pvf.ParseList(index) {
		if entry.ID == PartnerMoveItemID {
			if path != "" {
				return PartnerMoveRules{}, ErrItem
			}
			path = strings.ReplaceAll(entry.Path, "\\", "/")
		}
	}
	if path == "" {
		return PartnerMoveRules{}, ErrItem
	}
	if !strings.HasPrefix(strings.ToLower(path), "stackable/") {
		path = "stackable/" + path
	}
	text, e = source.ReadText(path)
	if e != nil {
		return PartnerMoveRules{}, e
	}
	doc, e := pvf.Parse(path, text)
	if e != nil {
		return PartnerMoveRules{}, e
	}
	kind, ok := doc.Text("stackable type")
	action, actionOK := doc.Text("action type")
	args := doc.Ints("action type")
	cool, coolOK := doc.Int("cool time")
	limit, limitOK := doc.Int("stack limit")
	minimum, minOK := doc.Int("minimum level")
	if !ok || strings.Trim(kind, "[] ") != "waste" || !actionOK || strings.Trim(action, "[] ") != "teleport to partner" || len(args) != 1 || args[0] != 1 || !coolOK || cool <= 0 || cool > 86400000 || !limitOK || limit <= 0 || !minOK || minimum <= 0 || minimum > 255 {
		return PartnerMoveRules{}, fmt.Errorf("wedding: invalid partner teleport definition")
	}
	rule := PartnerMoveRules{ItemID: PartnerMoveItemID, StackLimit: limit, CooldownMilliseconds: cool, MinimumLevel: int(minimum), PVFPath: path}
	for _, place := range doc.Texts("action usable place") {
		switch strings.Trim(place, "[] ") {
		case "village":
			rule.Village = true
		case "seria room":
			rule.Seria = true
		default:
			return PartnerMoveRules{}, ErrState
		}
	}
	if !rule.Village && !rule.Seria {
		return PartnerMoveRules{}, ErrState
	}
	return rule, nil
}

// MoveToPartner accepts a destination witnessed and revalidated by the bridge,
// never one authorized by a client's coordinates. One ordinary [waste] ticket
// is consumed with the committed trip, following the current PVF use family.
// Its trailing subtype zero is not a no-consumption flag. This is a resource
// interpretation; it is not a claim of an observed original-server debit.
func (o Owner) MoveToPartner(ctx context.Context, req PairRequest, rules PartnerMoveRules, cmd PartnerMoveCommand) (PairResult, error) {
	if rules.ItemID != PartnerMoveItemID || rules.CooldownMilliseconds <= 0 || rules.StackLimit <= 0 || req.Revalidate == nil || cmd.Admit == nil || req.Now.UnixMilli() <= 0 || !cmd.Destination.Valid() || cmd.Slot < 3 || cmd.Slot > 288 || cmd.Slots.ListType != r.MainInventoryListType {
		return PairResult{}, ErrState
	}
	result, _, e := o.extensionMutation(ctx, req, ExtensionReceipt{}, func(a, b *r.CharacterRecord, ai, _ *r.InventoryRecord, _ *Room, g r.Group) error {
		if a.Level < rules.MinimumLevel || a.Stats["town_id"] != cmd.FromTown || a.Stats["area_id"] != cmd.FromArea || b.Stats["town_id"] != cmd.Destination.Town || b.Stats["area_id"] != cmd.Destination.Area {
			return ErrState
		}
		if e := cmd.Admit(g, *a, cmd.Destination); e != nil {
			return e
		}
		if e := r.BindInventoryContainerState(ctx, g.Settings, ai); e != nil {
			return e
		}
		if !cmd.Slots.ForInventory(ai).Contains(r.MainInventoryListType, int16(cmd.Slot)) {
			return ErrItem
		}
		if e := ValidatePartnerMoveVoucher(ai, rules, cmd.Slot, cmd.Slots, req.Now); e != nil {
			return e
		}
		now := req.Now.UnixMilli()
		last := a.Stats[statPrefix+"partner_move_at_ms"]
		if last < 0 || last > now || last > 0 && now-last < rules.CooldownMilliseconds {
			return ErrState
		}
		key := strconv.Itoa(int(r.MainInventoryListType)) + ":" + strconv.Itoa(int(cmd.Slot))
		if e := consume(ai, rules.ItemID, 1, key, req.Now); e != nil {
			return e
		}
		a.Stats[statPrefix+"partner_move_at_ms"] = now
		a.Stats["town_id"], a.Stats["area_id"] = cmd.Destination.Town, cmd.Destination.Area
		a.Stats["pos_x"], a.Stats["pos_y"], a.Stats["direction"] = cmd.Destination.X, cmd.Destination.Y, cmd.Destination.Direction
		return nil
	})
	return result, e
}
