package wedding

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type LovePointCoupon struct {
	ItemID, Points, StackLimit int64
	MinimumLevel               int
	PVFPath                    string
	Village, SeriaRoom         bool
}

var ErrLovePointLimit = fmt.Errorf("%w: love-point balance would exceed the PVF maximum", ErrItem)

func loadLovePointItems(source pvf.Source, system *pvf.Document, maximum int64) (map[int64]LovePointCoupon, error) {
	values := system.Ints("love point item")
	if len(values) == 0 || len(values)%2 != 0 {
		return nil, fmt.Errorf("wedding: love-point item shape")
	}
	wanted := make(map[int64]int64, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		id, points := values[i], values[i+1]
		if id <= 0 || id > 0xffffffff || points <= 0 || points > maximum {
			return nil, fmt.Errorf("wedding: invalid love-point item")
		}
		if _, duplicate := wanted[id]; duplicate {
			return nil, fmt.Errorf("wedding: duplicate love-point item")
		}
		wanted[id] = points
	}
	text, err := source.ReadText("stackable/stackable.lst")
	if err != nil {
		return nil, err
	}
	index, err := pvf.Parse("stackable/stackable.lst", text)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]LovePointCoupon, len(wanted))
	for _, entry := range pvf.ParseList(index) {
		points, required := wanted[entry.ID]
		if !required {
			continue
		}
		if _, duplicate := result[entry.ID]; duplicate {
			return nil, fmt.Errorf("wedding: duplicate love-point item reference")
		}
		path := strings.ReplaceAll(entry.Path, "\\", "/")
		if !strings.HasPrefix(strings.ToLower(path), "stackable/") {
			path = "stackable/" + path
		}
		text, err := source.ReadText(path)
		if err != nil {
			return nil, err
		}
		doc, err := pvf.Parse(path, text)
		if err != nil {
			return nil, err
		}
		kind, _ := doc.Text("stackable type")
		action, _ := doc.Text("action type")
		arguments := doc.Ints("action type")
		level, _ := doc.Int("minimum level")
		limit, _ := doc.Int("stack limit")
		if strings.Trim(kind, "[] ") != "waste" || strings.Trim(action, "[] ") != "love point coupon" ||
			len(arguments) != 1 || arguments[0] != 1 || level <= 0 || level > 255 || limit <= 0 || limit > math.MaxInt32 {
			return nil, fmt.Errorf("wedding: invalid love-point coupon %d", entry.ID)
		}
		coupon := LovePointCoupon{ItemID: entry.ID, Points: points, StackLimit: limit, MinimumLevel: int(level), PVFPath: path}
		for _, place := range doc.Texts("action usable place") {
			switch strings.Trim(place, "[] ") {
			case "village":
				coupon.Village = true
			case "seria room":
				coupon.SeriaRoom = true
			default:
				return nil, fmt.Errorf("wedding: unsupported love-point coupon place %q", place)
			}
		}
		if !coupon.Village && !coupon.SeriaRoom {
			return nil, fmt.Errorf("wedding: love-point coupon places missing")
		}
		result[entry.ID] = coupon
	}
	if len(result) != len(wanted) {
		return nil, fmt.Errorf("wedding: love-point coupon definitions missing")
	}
	return result, nil
}

// The current native action sends only a bag slot, not a client item/instance
// ID. ExpectedItemID and Slots are resolved by the bridge from that owned slot
// and the paired PVF; the transaction rechecks them against the fresh row.
type LovePointUseRequest struct {
	Slot           int16
	ExpectedItemID int64
	Session        string // server-owned connection nonce, encoded as 32 bytes
	Sequence       uint64 // extended native connection serial
	Slots          itemplacement.SlotPolicy
	SeriaRoom      bool
}

type LovePointUseResult struct {
	PairResult
	ItemID    int64
	Remaining int64
	Replayed  bool
}

func (o Owner) ConsumeLovePointCoupon(ctx context.Context, req PairRequest, use LovePointUseRequest) (LovePointUseResult, error) {
	nonce, nonceErr := hex.DecodeString(use.Session)
	if o.Rules == nil || o.Rules.MaxLovePoint <= 0 || use.Slot < 3 || use.Slot > 288 || use.ExpectedItemID <= 0 ||
		use.Sequence == 0 || use.Sequence > math.MaxInt64 || nonceErr != nil || len(nonce) != 32 || use.Slots.ListType != r.MainInventoryListType {
		return LovePointUseResult{}, ErrItem
	}
	coupon, known := o.Rules.LovePointItems[use.ExpectedItemID]
	if !known || coupon.ItemID != use.ExpectedItemID || coupon.Points <= 0 || coupon.Points > o.Rules.MaxLovePoint ||
		use.SeriaRoom && !coupon.SeriaRoom || !use.SeriaRoom && !coupon.Village {
		return LovePointUseResult{}, ErrItem
	}
	sessionA, sessionB := int64(binary.LittleEndian.Uint64(nonce[:8])), int64(binary.LittleEndian.Uint64(nonce[8:16]))
	var result LovePointUseResult
	pair, err := o.pair(ctx, req, func(a, b *r.CharacterRecord, inventory, _ *r.InventoryRecord) error {
		state, _, err := ValidatePair(*a, *b)
		if err != nil || state.Phase != Married || state.LovePoints > o.Rules.MaxLovePoint || a.Level < coupon.MinimumLevel {
			return ErrState
		}
		sameSession := a.Stats[statPrefix+"love_use_session_a"] == sessionA && a.Stats[statPrefix+"love_use_session_b"] == sessionB
		last := a.Stats[statPrefix+"love_use_sequence"]
		if sameSession && last >= int64(use.Sequence) {
			if last != int64(use.Sequence) || a.Stats[statPrefix+"love_use_slot"] != int64(use.Slot) || a.Stats[statPrefix+"love_use_item"] != use.ExpectedItemID {
				return ErrReplay
			}
			result.Replayed = true
			result.ItemID = use.ExpectedItemID
			if current := inventory.Slots[strconv.Itoa(int(r.MainInventoryListType))+":"+strconv.Itoa(int(use.Slot))]; current.ItemID == use.ExpectedItemID {
				result.Remaining = current.Count
			}
			return nil
		}
		if err := r.BindInventoryContainerState(ctx, o.Repositories.Settings, inventory); err != nil {
			return err
		}
		if !use.Slots.ForInventory(inventory).Contains(r.MainInventoryListType, use.Slot) {
			return ErrItem
		}
		key := strconv.Itoa(int(r.MainInventoryListType)) + ":" + strconv.Itoa(int(use.Slot))
		stack, found := inventory.Slots[key]
		if !found || stack.ItemID != use.ExpectedItemID || stack.Count <= 0 || stack.Count > coupon.StackLimit || !usableAssetAt(stack, req.Now) {
			return ErrItem
		}
		if coupon.Points > o.Rules.MaxLovePoint-state.LovePoints {
			return ErrLovePointLimit
		}
		if err = consume(inventory, coupon.ItemID, 1, key, req.Now); err != nil {
			return err
		}
		state.LovePoints += coupon.Points
		write(a, state)
		a.Stats[statPrefix+"love_use_session_a"], a.Stats[statPrefix+"love_use_session_b"] = sessionA, sessionB
		a.Stats[statPrefix+"love_use_sequence"], a.Stats[statPrefix+"love_use_slot"], a.Stats[statPrefix+"love_use_item"] = int64(use.Sequence), int64(use.Slot), use.ExpectedItemID
		result.ItemID, result.Remaining = coupon.ItemID, stack.Count-1
		return nil
	})
	if err != nil {
		return LovePointUseResult{}, err
	}
	result.PairResult = pair
	return result, nil
}
