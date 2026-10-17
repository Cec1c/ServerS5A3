package itemlock

import (
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestUnlockStartsPersistent72HourDelay(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{"0:8": {ItemID: 1001, Count: 1, Extra: map[string]string{"equipment_lock_state": "1"}}}})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 268, Body: []byte{0, 8, 0}, SelectedCharacterID: 27, Repositories: repos})
	if err != nil || !got.ResponseAllowed {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	item := loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if item.Extra["equipment_lock_state"] != "2" || item.Extra["equipment_unlock_at"] == "" {
		t.Fatalf("unlock cleared protection immediately: %+v", item)
	}
	if len(got.UpperResponses) != 1 || binary.LittleEndian.Uint32(got.UpperResponses[0].Body[4:]) != 259200 {
		t.Fatalf("must acknowledge pending delay without early unlock notice: %+v", got.UpperResponses)
	}
}

func TestUnlockDeadlineRetryCancelAndExpiry(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Unix(1800000000, 0)
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{"0:8": {ItemID: 1001, Count: 1, Extra: map[string]string{"equipment_lock_state": "1", "instance": "keep"}}}})
	owner, _ := NewOwner(repos)
	owner.now = func() time.Time { return now }
	cmd := Command{SelectedCharacterID: 27, Operation: "request_item_unlock", ListType: 0, SlotIndex: 8}
	first, err := owner.Apply(ctx, cmd)
	if err != nil || first.RemainingSeconds != 259200 {
		t.Fatalf("first=%+v %v", first, err)
	}
	initial := loadTestInventory(t, ctx, repos, "27").Slots["0:8"].Extra["equipment_unlock_at"]
	now = now.Add(time.Hour)
	again, err := owner.Apply(ctx, cmd)
	if err != nil || again.Changed || again.RemainingSeconds != 255600 {
		t.Fatalf("retry=%+v %v", again, err)
	}
	item := loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if item.Extra["equipment_unlock_at"] != initial || !IsLockedAt(item, now) {
		t.Fatal("retry reset deadline or protection")
	}
	owner2, _ := NewOwner(repos)
	owner2.now = func() time.Time { return now }
	cmd.Operation = "request_item_unlock_cancel"
	if _, err := owner2.Apply(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	item = loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if item.Extra["equipment_lock_state"] != "1" || item.Extra["equipment_unlock_at"] != "" {
		t.Fatal("cancel failed")
	}
	cmd.Operation = "request_item_unlock"
	_, err = owner2.Apply(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(72 * time.Hour)
	item = loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if IsLockedAt(item, now) {
		t.Fatal("deadline did not expire")
	}
	end, err := owner2.Apply(ctx, cmd)
	if err != nil || end.State != "" {
		t.Fatalf("finish=%+v %v", end, err)
	}
	item = loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if item.Extra["instance"] != "keep" || item.Extra["equipment_unlock_at"] != "" {
		t.Fatal("instance lost or deadline stale")
	}
}
func TestUnlockMalformedDeadlineFailsClosed(t *testing.T) {
	for _, value := range []string{"", "bad", "0", "-1"} {
		item := dnfrepo.ItemStack{Extra: map[string]string{"equipment_lock_state": "2", "equipment_unlock_at": value}}
		if !IsLockedAt(item, time.Now()) {
			t.Fatalf("invalid deadline %q unlocked", value)
		}
	}
}
func TestUnlockSnapshotUsesAbsoluteDeadline(t *testing.T) {
	now := time.Now()
	stack := dnfrepo.ItemStack{ItemID: 1, Count: 1, Extra: map[string]string{"equipment_lock_state": "2", "equipment_unlock_at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}
	body := BuildLockListSnapshot(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:8": stack}})
	if len(body) != 10 || body[5] != 2 {
		t.Fatalf("snapshot %x", body)
	}
	seconds := binary.LittleEndian.Uint32(body[6:])
	if seconds < 3598 || seconds > 3600 {
		t.Fatalf("seconds=%d", seconds)
	}
	stack.Extra["equipment_unlock_at"] = strconv.FormatInt(now.Add(-time.Second).Unix(), 10)
	body = BuildLockListSnapshot(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:8": stack}})
	if len(body) != 2 || binary.LittleEndian.Uint16(body) != 0 {
		t.Fatalf("expired row replayed %x", body)
	}
}

type failLockSave struct{ dnfrepo.InventoryRepository }

func (f failLockSave) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("injected save failure")
}
func TestUnlockSaveFailureNeverAcknowledgesOrMutates(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{"0:8": {ItemID: 1, Count: 1, Extra: map[string]string{"equipment_lock_state": "1"}}}})
	bad := dnfrepo.Group{Inventory: failLockSave{repos.Inventory}}
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 268, Body: []byte{0, 8, 0}, SelectedCharacterID: 27, Repositories: bad})
	if err != nil || got.ResponseAllowed {
		t.Fatalf("ack on failure %+v %v", got, err)
	}
	item := loadTestInventory(t, ctx, repos, "27").Slots["0:8"]
	if item.Extra["equipment_lock_state"] != "1" || item.Extra["equipment_unlock_at"] != "" {
		t.Fatal("failed save mutated protection")
	}
}
func TestConcurrentUnlockPreservesBothSlots(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{"0:8": {ItemID: 1, Count: 1, Extra: map[string]string{"equipment_lock_state": "1"}}, "0:9": {ItemID: 2, Count: 1, Extra: map[string]string{"equipment_lock_state": "1"}}}})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, slot := range []int16{8, 9} {
		wg.Add(1)
		go func(slot int16) {
			defer wg.Done()
			owner, _ := NewOwner(repos)
			_, err := owner.Apply(ctx, Command{SelectedCharacterID: 27, Operation: "request_item_unlock", SlotIndex: slot})
			errs <- err
		}(slot)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	record := loadTestInventory(t, ctx, repos, "27")
	for _, key := range []string{"0:8", "0:9"} {
		if record.Slots[key].Extra["equipment_lock_state"] != "2" {
			t.Fatalf("lost concurrent lock update %s", key)
		}
	}
}
