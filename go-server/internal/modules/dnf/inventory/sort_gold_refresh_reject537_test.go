package inventory

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// failingCharacterLoad537 fails only Load. The sort transaction runs on the
// memory settlement store, so nothing but the pre-sort wallet read is affected.
type failingCharacterLoad537 struct {
	dnfrepo.CharacterRepository
	err error
}

func (f failingCharacterLoad537) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	return dnfrepo.CharacterRecord{}, false, f.err
}

func slotSignature537(slots map[string]dnfrepo.ItemStack) map[string]string {
	out := make(map[string]string, len(slots))
	for key, stack := range slots {
		out[key] = strconv.FormatInt(stack.ItemID, 10) + ":" + strconv.FormatInt(stack.Count, 10)
	}
	return out
}

// The wallet is read BEFORE the sort commits. A read failure must reject the
// request with no inventory movement and no ACK, instead of sorting first and
// then answering with a wallet-less full refresh.
func TestSort537WalletReadFailureRejectsBeforePersist(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 4321}})
	seeded := map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 120): {ItemID: 3277, Count: 2},
		slotKey(listTypeMain, 125): {ItemID: 1002, Count: 2},
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: seeded})
	realCharacter := repos.Character
	repos.Character = failingCharacterLoad537{CharacterRepository: realCharacter, err: errors.New("transient character read failure")}
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 20, Body: []byte{0, 3}, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if result.ResponseAllowed || len(result.UpperResponses) != 0 {
		t.Fatalf("a failed wallet read must not answer: allowed=%t responses=%d", result.ResponseAllowed, len(result.UpperResponses))
	}
	stored := loadTestInventory(t, ctx, repos, "77")
	if !reflect.DeepEqual(slotSignature537(stored.Slots), slotSignature537(seeded)) {
		t.Fatalf("sort persisted before the wallet read succeeded: before=%v after=%v", slotSignature537(seeded), slotSignature537(stored.Slots))
	}
	character, _, err := realCharacter.Load(ctx, "77")
	if err != nil || character.Stats["gold"] != 4321 {
		t.Fatalf("wallet changed across the rejected sort: %+v err=%v", character.Stats, err)
	}
}
