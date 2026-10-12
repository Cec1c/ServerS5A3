package inventory

import (
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestSort537PreloadOptionalRepositoryAndNonMainLists(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 4321}})
	gold, err := loadSortRefreshGold(ctx, nil, listTypeMain, "77")
	if err != nil || gold.ready {
		t.Fatalf("nil repository must prepare nothing: %+v err=%v", gold, err)
	}
	for _, list := range []byte{listTypePersonalCargo, listTypeAvatar, listTypePet, listTypeAccountCargo} {
		gold, err = loadSortRefreshGold(ctx, repos.Character, list, "77")
		if err != nil || gold.ready {
			t.Fatalf("list=%d must prepare nothing: %+v err=%v", list, gold, err)
		}
	}
	gold, err = loadSortRefreshGold(ctx, repos.Character, listTypeMain, "77")
	if err != nil || !gold.ready || gold.amount != 4321 {
		t.Fatalf("main list gold=%+v err=%v", gold, err)
	}
}

func TestSort537PreloadClampsAndToleratesMissingGold(t *testing.T) {
	ctx := context.Background()
	hi := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, hi, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": math.MaxInt64}})
	if gold, err := loadSortRefreshGold(ctx, hi.Character, listTypeMain, "77"); err != nil || !gold.ready || gold.amount != math.MaxInt32 {
		t.Fatalf("max clamp gold=%+v err=%v", gold, err)
	}
	lo := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, lo, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": -5}})
	if gold, err := loadSortRefreshGold(ctx, lo.Character, listTypeMain, "77"); err != nil || !gold.ready || gold.amount != 0 {
		t.Fatalf("negative clamp gold=%+v err=%v", gold, err)
	}
	none := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, none, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"sp": 3}})
	if gold, err := loadSortRefreshGold(ctx, none.Character, listTypeMain, "77"); err != nil || gold.ready {
		t.Fatalf("missing gold must prepare nothing: %+v err=%v", gold, err)
	}
	if gold, err := loadSortRefreshGold(ctx, none.Character, listTypeMain, "999"); err != nil || gold.ready {
		t.Fatalf("missing character must prepare nothing: %+v err=%v", gold, err)
	}
}

func TestSort537ApplyOverwritesStaleKeepsConflictAndSkipsNonMain(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	stale := SortResult{ListType: listTypeMain, Refresh: map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 0):   {ItemID: 0, Count: 9, Extra: map[string]string{"amount_or_count": "9"}},
		slotKey(listTypeMain, 125): {ItemID: 3277, Count: 2},
	}}
	got := applySortRefreshGold(stale, sortRefreshGold{ready: true, amount: 4321})
	row := got.Refresh[slotKey(listTypeMain, 0)]
	if row.ItemID != 0 || row.Count != 4321 || len(row.Extra) != 0 {
		t.Fatalf("stale wallet row not overwritten: %+v", row)
	}
	body, ok := buildSortRefreshBody(context.Background(), repos.Settings, got)
	if !ok || len(body) < 15 || binary.LittleEndian.Uint32(body[11:15]) != 4321 {
		t.Fatalf("encoded wallet amount wrong: ok=%t", ok)
	}
	conflict := SortResult{ListType: listTypeMain, Refresh: map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 0): {ItemID: 3277, Count: 2},
	}}
	got = applySortRefreshGold(conflict, sortRefreshGold{ready: true, amount: 4321})
	if row := got.Refresh[slotKey(listTypeMain, 0)]; row.ItemID != 3277 || row.Count != 2 {
		t.Fatalf("conflicting item must be preserved: %+v", row)
	}
	personal := SortResult{ListType: listTypePersonalCargo, Refresh: map[string]dnfrepo.ItemStack{}}
	if got := applySortRefreshGold(personal, sortRefreshGold{ready: true, amount: 4321}); len(got.Refresh) != 0 {
		t.Fatalf("non-main list must not receive a wallet row: %+v", got.Refresh)
	}
	notReady := SortResult{ListType: listTypeMain, Refresh: map[string]dnfrepo.ItemStack{}}
	if got := applySortRefreshGold(notReady, sortRefreshGold{}); len(got.Refresh) != 0 {
		t.Fatalf("not-ready gold must not add a row: %+v", got.Refresh)
	}
	input := map[string]dnfrepo.ItemStack{slotKey(listTypeMain, 125): {ItemID: 3277, Count: 2}}
	before := cloneItemMap(input)
	_ = applySortRefreshGold(SortResult{ListType: listTypeMain, Refresh: input}, sortRefreshGold{ready: true, amount: 5})
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("projection mutated the input snapshot")
	}
}
