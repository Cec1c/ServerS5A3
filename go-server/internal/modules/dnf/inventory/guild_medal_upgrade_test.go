package inventory

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Synthetic forced outcomes isolate transaction/ACK invariants; the bridge
// suite separately resolves the actual flag table from the immutable PVF.
func TestGuildMedalUpgradeAtomicHandler(t *testing.T) {
	for _, name := range []string{"success", "fail_no_change", "fail_downgrade", "late_bag_failure", "missing_transaction", "missing_policy", "destroy_rejected"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			raw := make([]byte, currentItemListEntrySize)
			binary.LittleEndian.PutUint32(raw[2:6], 100380051)
			raw[10] = 0xa5 // level5 plus unrelated upper packed bits
			raw[0x57] = 7
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 50}})
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
				"38:0":  {ItemID: 100380051, Count: 1, RawEntry: raw, Extra: map[string]string{"identity": "retained"}},
				"0:121": {ItemID: 10154439, Count: 30},
				"0:122": {ItemID: 10154439, Count: 999},
			}})
			before, _, _ := repos.Inventory.Load(ctx, "77")
			beforeChar, _, _ := repos.Character.Load(ctx, "77")
			policy := alignedcmd.GuildMedalUpgradePolicy{MaterialID: 10154439, MaterialCount: 18, MaterialSlotStart: 121, MaterialSlotEnd: 176, SuccessWeight: 100000, PenaltyType: 1}
			wantLevel, wantCode := byte(6), byte(upgradeResultCodeSuccess)
			if name == "fail_no_change" {
				policy.SuccessWeight = 0
				wantLevel, wantCode = 5, upgradeResultCodeFailNoChange
			}
			if name == "fail_downgrade" {
				policy.SuccessWeight = 0
				policy.PenaltyType = 2
				policy.PenaltyValue = 3
				wantLevel, wantCode = 2, upgradeResultCodeDowngrade
			}
			if name == "destroy_rejected" {
				policy.PenaltyType = 3
			}
			probe := &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
			if name == "late_bag_failure" {
				repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: probe}
			}
			if name == "missing_transaction" {
				repos.CharacterAssets = nil
			}
			body := make([]byte, 20)
			body[1] = 38
			binary.LittleEndian.PutUint32(body[4:8], 100380051)
			binary.LittleEndian.PutUint16(body[8:10], 121)
			binary.LittleEndian.PutUint16(body[10:12], 0xffff)
			binary.LittleEndian.PutUint32(body[16:20], 0xffffffff)
			req := alignedcmd.Request{Opcode: 80, Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
				GuildMedalUpgradeResolver: func(id int64, level byte) (alignedcmd.GuildMedalUpgradePolicy, error) {
					if id != 100380051 || level != 5 {
						t.Fatalf("resolver identity %d level %d", id, level)
					}
					return policy, nil
				},
			}
			if name == "missing_policy" {
				req.GuildMedalUpgradeResolver = nil
			}
			got, err := NewHandler().Handle(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "77")
			afterChar, _, _ := repos.Character.Load(ctx, "77")
			if name == "late_bag_failure" || name == "missing_transaction" || name == "missing_policy" || name == "destroy_rejected" {
				if len(got.UpperResponses) != 1 || got.UpperResponses[0].Body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
					t.Fatalf("rejected/failed transaction leaked: %+v", got)
				}
				if name == "late_bag_failure" && (probe.bagSaveAttempts != 1 || !probe.goldSeen || probe.goldAfterApply != 50) {
					t.Fatalf("did not reach bag save failure / wallet changed: %+v", probe)
				}
				return
			}
			ack := got.UpperResponses[0].Body
			if len(ack) != 25 || ack[0] != 1 || ack[11] != 5 || ack[12] != wantCode || ack[13] != wantLevel || ack[14] != 38 || binary.LittleEndian.Uint32(ack[4:8]) != 12 {
				t.Fatalf("ACK=%x", ack)
			}
			row := after.Slots["38:0"]
			if row.RawEntry[10] != 0xa0|wantLevel || row.RawEntry[0x57] != 7 || row.Extra["identity"] != "retained" || after.Slots["0:121"].Count != 12 || !reflect.DeepEqual(before.Slots["0:122"], after.Slots["0:122"]) || !reflect.DeepEqual(beforeChar, afterChar) {
				t.Fatalf("outcome did not preserve identity / charge exactly once: %+v", after)
			}
		})
	}
}
