package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestDailyEquipmentRealWornCommitRollback(t *testing.T) {
	f, q, slot, stack := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-worn-rollback")
	dailyEquipmentMove(t, f, slot, 3, 19)
	ctx := context.Background()
	before, _, e := f.group.Equipment.Load(ctx, "19")
	if e != nil || before.Entries["19"].ItemID != 400300075 {
		t.Fatalf("legal wear failed %v %v", before, e)
	}
	fault := f.group
	fault.AccountAssets = rejectRelativeExpiryCommit{fault.AccountAssets}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fault, true }
	f.service.armCurrentDailyDeleteClock(f.session)
	task := q.active["daily-delete:"+f.session.connID]
	if task == nil {
		t.Fatal("no clock")
	}
	q.now = stack.ExpireAt
	f.buffer.write.Reset()
	q.fire(task, false)
	after, _, _ := f.group.Equipment.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) || f.buffer.write.Len() != 0 || f.session.pendingItemExpiryProjection != nil {
		t.Fatal("aborted transaction removed equipment or sent clear")
	}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.group, true }
	task = q.active["daily-delete:"+f.session.connID]
	if task == nil || task.delay != time.Minute {
		t.Fatal("no retry")
	}
	q.now = q.now.Add(time.Minute)
	q.fire(task, false)
	after, _, _ = f.group.Equipment.Load(ctx, "19")
	if len(after.Entries) != 0 || f.buffer.write.Len() == 0 {
		t.Fatal("retry did not commit and project")
	}
}

func TestExpiryEquipmentZeroDurabilityKeepsCarrier(t *testing.T) {
	for _, kind := range []string{"equipment_zero", "equipment_worn", "stackable", "legacy_unknown"} {
		t.Run(kind, func(t *testing.T) {
			stack := dnfrepo.ItemStack{ItemID: 400300075, Count: 1, ExpireAt: time.Now().Add(time.Hour), Extra: map[string]string{}}
			want := uint16(0)
			switch kind {
			case "equipment_zero":
				stack.Extra["item_kind"] = "equipment"
			case "equipment_worn":
				stack.Extra["item_kind"] = "equipment"
				stack.Extra["durability"] = "45"
				want = 45
			case "stackable":
				stack.Extra["item_kind"] = "stackable"
			}
			row := currentItemListEntryFromStack(0, 9, stack)
			got := binary.LittleEndian.Uint16(row.data[0x0B:0x0D])
			if kind == "equipment_zero" || kind == "equipment_worn" {
				if got != want {
					t.Fatalf("durability got=%d want=%d", got, want)
				}
			} else if got < 3500 || got > 3600 {
				t.Fatalf("existing countdown lost: %d", got)
			}
		})
	}
}
