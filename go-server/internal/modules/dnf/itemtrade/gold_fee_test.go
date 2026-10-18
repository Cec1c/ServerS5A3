package itemtrade

import (
	"context"
	"errors"
	"math"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestExchangeReceivedGoldFeeAndAtomicRejection(t *testing.T) {
	for _, tc := range []struct {
		name                                                                               string
		first, second, offerFirst, offerSecond, wantFirst, wantSecond, feeFirst, feeSecond int64
		boundItem                                                                          bool
		wantErr                                                                            error
	}{
		{name: "one way", first: 100000, offerFirst: 100000, wantFirst: 0, wantSecond: 97000, feeSecond: 3000},
		{name: "two way gross", first: 100000, second: 40000, offerFirst: 100000, offerSecond: 10000, wantFirst: 9700, wantSecond: 127000, feeFirst: 300, feeSecond: 3000},
		{name: "below one fee", first: 33, offerFirst: 33, wantSecond: 33},
		{name: "first fee", first: 34, offerFirst: 34, wantSecond: 33, feeSecond: 1},
		{name: "fraction truncated", first: 9999, offerFirst: 9999, wantSecond: 9700, feeSecond: 299},
		{name: "insufficient gross", first: 99, offerFirst: 100, wantFirst: 99, wantErr: ErrGoldUnavailable},
		{name: "rejected item rolls fee back", first: 100000, offerFirst: 100000, boundItem: true, wantFirst: 100000, wantErr: ErrItemNotTradeable},
		{name: "net fits maximum", first: 100, second: math.MaxInt64 - 97, offerFirst: 100, wantSecond: math.MaxInt64, feeSecond: 3},
		{name: "net overflow rollback", first: 100, second: math.MaxInt64 - 96, offerFirst: 100, wantFirst: 100, wantSecond: math.MaxInt64 - 96, wantErr: ErrGoldOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			for id, gold := range map[string]int64{"1": tc.first, "2": tc.second} {
				if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, Stats: map[string]int64{"gold": gold}}); err != nil {
					t.Fatal(err)
				}
			}
			a, b := Participant{CharacterID: "1", Gold: tc.offerFirst}, Participant{CharacterID: "2", Gold: tc.offerSecond}
			if tc.boundItem {
				if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "2", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 1001, Count: 1, Bind: true}}}); err != nil {
					t.Fatal(err)
				}
				b.Offers = []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 9, Count: 1, ExpectedItem: 1001}}
			}
			o, _ := NewOwner(r)
			result, err := o.Exchange(ctx, a, b)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			for id, want := range map[string]int64{"1": tc.wantFirst, "2": tc.wantSecond} {
				v, _, loadErr := r.Character.Load(ctx, id)
				if loadErr != nil || v.Stats["gold"] != want {
					t.Fatalf("character %s gold=%d want=%d error=%v", id, v.Stats["gold"], want, loadErr)
				}
			}
			if err == nil && (result.GoldFees["1"] != tc.feeFirst || result.GoldFees["2"] != tc.feeSecond || result.ReceivedGold["1"] != tc.offerSecond || result.ReceivedGold["2"] != tc.offerFirst) {
				t.Fatalf("result fees=%v gross=%v", result.GoldFees, result.ReceivedGold)
			}
			if err != nil && (result.Characters != nil || result.GoldFees != nil) {
				t.Fatal("failed exchange published a settlement")
			}
		})
	}
}
