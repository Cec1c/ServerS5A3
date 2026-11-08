package equip

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAutoRepairRestoresAllWornInOneTransaction(t *testing.T) {
	for _, mode := range []string{"trigger", "not-triggered", "quota-exhausted", "invalid-peer-row", "explicit-auto-repair"} {
		t.Run(mode, func(t *testing.T) {
			current := uint16(1)
			if mode == "not-triggered" {
				current = 2
			}
			ctx, repos := seedDurabilityOwnerFixture(t, current)
			now := durabilityOwnerTestNow()
			if mode == "explicit-auto-repair" {
				now = time.Now().UTC()
			}
			account, _, _ := repos.Account.Load(ctx, durabilityOwnerTestAccount)
			premium.Upsert(&account, premium.DevilSlotType(premium.DevilSlotAutoRepair), 3600, 1, now)
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			character, _, _ := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
			if mode == "quota-exhausted" {
				for i := 0; i < 6; i++ {
					if !premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, now) {
						t.Fatal("seed quota")
					}
				}
				saveTestCharacter(t, ctx, repos, character)
			}
			record := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			types := map[int16]string{14: "[coat]", 15: "[shoulder]", 16: "[pants]", 17: "[shoes]", 18: "[waist]", 19: "[amulet]", 20: "[wrist]", 21: "[ring]", 24: "[support weapon]"}
			for slot := range types {
				id := int64(200000) + int64(slot)
				record.Entries[entryKey(slot)] = dnfrepo.EquipmentEntry{SlotIndex: slot, ItemID: id, RawEntry: durabilityOwnerTestRaw77(2, id, 3, 0x4f, 12345), Extra: map[string]string{"durability": "3", "current_durability": "3"}}
			}
			// Full and expired equipment remain untouched. Inventory is outside this operation.
			expired := record.Entries["21"]
			expired.ExpireAt = now.Add(-time.Hour)
			record.Entries["21"] = expired
			if mode == "invalid-peer-row" {
				bad := record.Entries["18"]
				bad.RawEntry = []byte{1}
				record.Entries["18"] = bad
			}
			saveTestEquipment(t, ctx, repos, record)
			before := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			resolver := func(id int64) (alignedcmd.RepairCostEvidence, error) {
				typ, max := "[weapon]", int64(48)
				if id != durabilityOwnerTestItemID {
					typ = types[int16(id-200000)]
					max = 60
				}
				evidence, _ := durabilityOwnerTestResolve(typ, max)(id)
				return evidence, nil
			}
			cmd := durabilityOwnerTestCommand(durabilityOwnerTestAccount, 12)
			cmd.Now = now
			result, err := newDurabilityOwner(t, repos).Decrease(ctx, cmd, resolver)
			if mode == "explicit-auto-repair" {
				// Reload the initial state to exercise the separate op23 entry point.
				saveTestEquipment(t, ctx, repos, before)
				saveTestCharacter(t, ctx, repos, character)
				owner, ownerErr := NewOwner(repos)
				if ownerErr != nil {
					t.Fatal(ownerErr)
				}
				_, err = owner.Repair(ctx, RepairCommand{AccountID: durabilityOwnerTestAccount, SelectedCharacterID: 77, SlotIndex: 12, AutoRepair: true}, resolver)
			}
			after := loadTestEquipment(t, ctx, repos, durabilityOwnerTestCharacter)
			saved, _, _ := repos.Character.Load(ctx, durabilityOwnerTestCharacter)
			if mode == "invalid-peer-row" {
				if err == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, saved) {
					t.Fatalf("failure did not roll back: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			shouldRepair := mode == "trigger" || mode == "explicit-auto-repair"
			if mode != "explicit-auto-repair" && result.AutoRepaired != shouldRepair {
				t.Fatalf("auto=%t", result.AutoRepaired)
			}
			for slot := range types {
				got := after.Entries[entryKey(slot)]
				original := before.Entries[entryKey(slot)]
				want := uint16(3)
				if shouldRepair && slot != 21 {
					want = 60
				}
				if binary.LittleEndian.Uint16(got.RawEntry[11:13]) != want || got.Extra["durability"] != strconv.Itoa(int(want)) {
					t.Fatalf("slot %d durability not %d", slot, want)
				}
				assertRawChangedOnlyAtDurability(t, original.RawEntry, got.RawEntry)
			}
			usage := int64(0)
			if shouldRepair {
				usage = 1
			}
			if mode == "quota-exhausted" {
				usage = 6
			}
			if premium.DailyUsage(saved, premium.DevilSlotAutoRepair, now) != usage || saved.Stats["gold"] != 5000 {
				t.Fatal("quota/wallet mismatch")
			}
		})
	}
}
