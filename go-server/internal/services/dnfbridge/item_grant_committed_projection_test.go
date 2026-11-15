package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/packageitem"
)

// This adapter models the old handler contract while running the same durable
// owner. It deliberately has no CommittedItemGrantReporter capability.
type legacyMagicBoxGrantHandler struct{ alignedcmd.Handler }

func (h legacyMagicBoxGrantHandler) Handle(ctx context.Context, req alignedcmd.Request) (alignedcmd.Result, error) {
	v, err := h.Handler.Handle(ctx, req)
	v.ItemGrantProjection = nil
	return v, err
}

func TestCommittedMagicBoxProjectionRemovesSnapshotReadsAndPreservesWire(t *testing.T) {
	for _, mode := range []string{itemGrantNoticeDelta, itemGrantNoticeContainers} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(itemGrantNoticeEnv, mode)
			var wires [][]byte
			var reads []int
			for _, committed := range []bool{false, true} {
				fixture := newRefreshCostFixture(t)
				fixture.warmUp(t)
				ctx := context.Background()
				record, _, _ := fixture.group.Inventory.Load(ctx, "19")
				stack := record.Slots["0:44"]
				stack.ItemID, stack.Count = 10007368, 1
				record.Slots["0:44"] = stack
				if err := fixture.group.Inventory.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
				counted, _ := fixture.service.repositoryProvider()
				ownerRepos := fixture.group
				// The cost fixture's old counting adapter exposes only the base
				// asset interface. Use its real ledger-capable memory transaction.
				ownerRepos.AccountAssets = fixture.group.AccountAssets.(countingAccountAssets).inner
				h := packageitem.NewHandler()
				if !committed {
					h = legacyMagicBoxGrantHandler{h}
				}
				registry := alignedcmd.NewRegistry(h)
				fixture.counter.reset()
				snapshot := fixture.service.armItemGrantSnapshotForRegistry(fixture.session, counted, dnfenum.CmdPacketUseRandomboxItem, registry)
				result, ok, err := registry.Route(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUseRandomboxItem), Body: []byte{0, 44, 0}, AccountID: "dnf:1", SelectedCharacterID: 19, Repositories: ownerRepos,
					MagicBoxResolver: func(int64) (alignedcmd.MagicBoxResolution, error) {
						return alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 3227, Weight: 100, Count: 2}}}}}, nil
					},
					MagicBoxRewardItemResolver: func(id int64) (alignedcmd.MagicBoxRewardItem, error) {
						return alignedcmd.MagicBoxRewardItem{ItemID: id, Kind: "stackable", SlotStart: 121, SlotEnd: 152}, nil
					},
				})
				if err != nil || !ok || !result.ResponseAllowed || len(result.PostActions) == 0 {
					t.Fatalf("magic box route failed: %v %+v", err, result)
				}
				fixture.service.applyItemGrantSnapshot(fixture.session, counted, snapshot, &result)
				n := fixture.counter.total("Inventory.Load") + fixture.counter.total("AccountInventory.Load")
				reads = append(reads, n)
				if committed && n != 0 {
					t.Fatalf("committed projection reloaded inventories %d times", n)
				}
				fixture.buffer.write.Reset()
				if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
					t.Fatal(err)
				}
				wires = append(wires, append([]byte(nil), fixture.buffer.write.Bytes()...))
			}
			if !bytes.Equal(wires[0], wires[1]) {
				t.Fatal("committed and legacy slot projection changed the packet stream")
			}
			if mode == itemGrantNoticeDelta && reads[0] != 4 {
				t.Fatalf("legacy inventory snapshot reads=%d, want4", reads[0])
			}
			t.Logf("snapshot repository loads %d -> %d; packet bytes equal", reads[0], reads[1])
		})
	}
}

func TestCommittedItemGrantPreservesContainerExceptionsAndSelectedScope(t *testing.T) {
	t.Setenv(itemGrantNoticeEnv, itemGrantNoticeDelta)
	fixture := newRefreshCostFixture(t)
	result := magicBoxGrantResult()
	result.ItemGrantProjection = &alignedcmd.ItemGrantProjection{ChangedSlots: []alignedcmd.ItemSlotRefresh{{ListType: 38, SlotIndex: 0}, {ListType: 7, SlotIndex: 191}, {ListType: 0, SlotIndex: 44}, {ListType: 12, SlotIndex: 1}}}
	fixture.counter.reset()
	fixture.service.applyItemGrantSnapshot(fixture.session, fixture.group, nil, &result)
	want := []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: 44}, {ListType: 7, SlotIndex: 191}}
	if !reflect.DeepEqual(result.ItemSlotRefreshes, want) {
		t.Fatalf("delta rows=%v", result.ItemSlotRefreshes)
	}
	lists, armed := fixture.session.takeItemGrantNoticeContainers()
	if !armed || !reflect.DeepEqual(lists, []byte{38}) {
		t.Fatalf("container exceptions=%v armed=%t", lists, armed)
	}
	if fixture.counter.total("") != 0 {
		t.Fatal("committed projection performed a repository operation")
	}
}
