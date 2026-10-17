package alignedcmd

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

type combinedResultSnapshotHandler struct{ result Result }

func (combinedResultSnapshotHandler) Domain() dnfenum.AlignedDomain {
	return dnfenum.AlignedDomainMail
}

func (h combinedResultSnapshotHandler) Handle(context.Context, Request) (Result, error) {
	return h.result, nil
}

func TestRegistryRoutePreservesIndependentRefreshAndConsumptionSnapshots(t *testing.T) {
	lists := []byte{0, 38}
	fact := &StackableConsumption{
		CharacterID: "19", ListType: 0, SlotIndex: 7, ItemID: 1066,
		PreviousCount: 1, RemainingCount: 0,
	}
	registry := NewRegistry(combinedResultSnapshotHandler{result: Result{
		Handled: true, ResponseAllowed: true,
		AuthoritativeListRefreshes: lists, StackableConsumption: fact,
	}})
	request := Request{Opcode: uint16(dnfenum.CmdPacketMailboxOpen)}
	first, ok, err := registry.Route(context.Background(), request)
	if err != nil || !ok || len(first.AuthoritativeListRefreshes) != 2 || first.StackableConsumption == nil {
		t.Fatalf("combined result lost metadata: result=%+v ok=%t err=%v", first, ok, err)
	}
	// A reused handler result must not alter already routed refreshes or proof.
	lists[0], fact.RemainingCount = 7, 1
	if first.AuthoritativeListRefreshes[0] != 0 || first.StackableConsumption.RemainingCount != 0 {
		t.Fatal("handler mutation changed an already routed result")
	}
	// Nor may one consumer's changes corrupt a later route from that handler.
	first.AuthoritativeListRefreshes[1] = 1
	first.StackableConsumption.CharacterID = "other"
	second, ok, err := registry.Route(context.Background(), request)
	if err != nil || !ok || len(second.AuthoritativeListRefreshes) != 2 || second.StackableConsumption == nil {
		t.Fatalf("second route lost metadata: result=%+v ok=%t err=%v", second, ok, err)
	}
	if second.AuthoritativeListRefreshes[0] != 7 || second.AuthoritativeListRefreshes[1] != 38 ||
		second.StackableConsumption.CharacterID != "19" || second.StackableConsumption.RemainingCount != 1 {
		t.Fatalf("consumer mutation leaked into a later route: %+v", second)
	}
}
