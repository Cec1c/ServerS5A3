package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

type petDeltaInventorySpy struct {
	repository.InventoryRepository
	calls      int
	collection string
	keys       []string
	err        error
}

func (s *petDeltaInventorySpy) SaveItemStackSlotDelta(ctx context.Context, r repository.InventoryRecord, collection string, keys []string) error {
	s.calls++
	s.collection = collection
	s.keys = append([]string(nil), keys...)
	return s.err
}

type petDeltaEquipmentSpy struct {
	repository.EquipmentRepository
	calls int
	keys  []string
	err   error
}

func (s *petDeltaEquipmentSpy) SaveEquipmentEntryDelta(ctx context.Context, r repository.EquipmentRecord, keys []string) error {
	s.calls++
	s.keys = append([]string(nil), keys...)
	return s.err
}
func TestPetScopedInventoryPreservesDeltaAndOwnership(t *testing.T) {
	sentinel := errors.New("write failure")
	base := &petDeltaInventorySpy{err: sentinel}
	scoped := &characterPetScopedInventoryRepository{characterID: "owner", base: base}
	writer, ok := any(scoped).(repository.ItemStackSlotDeltaWriter)
	if !ok {
		t.Fatal("pet transaction wrapper lost slot delta capability")
	}
	if err := writer.SaveItemStackSlotDelta(context.Background(), repository.InventoryRecord{CharacterID: "foreign"}, "slots", []string{"1"}); err == nil || base.calls != 0 {
		t.Fatalf("foreign err=%v calls=%d", err, base.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.SaveItemStackSlotDelta(ctx, repository.InventoryRecord{CharacterID: "owner"}, "slots", []string{"1"}); !errors.Is(err, context.Canceled) || base.calls != 0 {
		t.Fatalf("canceled err=%v calls=%d", err, base.calls)
	}
	if err := writer.SaveItemStackSlotDelta(context.Background(), repository.InventoryRecord{CharacterID: "owner"}, "warehouse", []string{"1", "2"}); !errors.Is(err, sentinel) || base.calls != 1 || base.collection != "warehouse" || !reflect.DeepEqual(base.keys, []string{"1", "2"}) {
		t.Fatalf("delta err=%v spy=%+v", err, base)
	}
}
func TestPetScopedEquipmentPreservesDeltaAndOwnership(t *testing.T) {
	sentinel := errors.New("write failure")
	base := &petDeltaEquipmentSpy{err: sentinel}
	scoped := &characterPetScopedEquipmentRepository{characterID: "owner", base: base}
	writer, ok := any(scoped).(repository.EquipmentEntryDeltaWriter)
	if !ok {
		t.Fatal("pet transaction wrapper lost equipment delta capability")
	}
	if err := writer.SaveEquipmentEntryDelta(context.Background(), repository.EquipmentRecord{CharacterID: "foreign"}, []string{"weapon"}); err == nil || base.calls != 0 {
		t.Fatalf("foreign err=%v calls=%d", err, base.calls)
	}
	if err := writer.SaveEquipmentEntryDelta(context.Background(), repository.EquipmentRecord{CharacterID: "owner"}, []string{"weapon"}); !errors.Is(err, sentinel) || base.calls != 1 || !reflect.DeepEqual(base.keys, []string{"weapon"}) {
		t.Fatalf("delta err=%v spy=%+v", err, base)
	}
}
