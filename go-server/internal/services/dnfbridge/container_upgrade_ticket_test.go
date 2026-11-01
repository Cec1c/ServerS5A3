package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestUpgradeTicketDefinitionSafety(t *testing.T) {
	base := "[stackable type] `[etc]` 0\n[action type] `[upgrade cargo]`\n[/action type]\n[minimum level] 1\n[usable job] `[all]`\n[/usable job]\n"
	for _, tc := range []struct {
		tail  string
		valid bool
	}{
		{"", true},
		{"[need material] 10149222 3\n[daily purchase limit] `account` 2\n", true},
		{"[action usable place] `[village]` `[seria room]`\n", true},
		{"[impossible contents] `gift`\n[impossible contents] `ban redeemitem`\n", true},
		{"[action type] `[upgrade inventory]`\n", false},
		{"[minimum level] 50\n", false},
		{"[action usable place] `[dungeon]`\n", false},
		{"[action usable condition] `[guildmember]`\n", false},
		{"[cool time] 1000\n", false},
		{"[maximum level] 50\n", false},
		{"[action usable place] 1 `[village]`\n", false},
		{"[impossible contents] `unknown`\n", false},
	} {
		doc, e := dnfpvf.Parse("stackable/ticket.stk", base+tc.tail)
		if e != nil {
			t.Fatal(e)
		}
		_, e = currentContainerUpgradeTicketDefinition(1, doc.Path, doc)
		if (e == nil) != tc.valid {
			t.Fatalf("tail %q error=%v", tc.tail, e)
		}
	}
	for _, text := range []string{"[upgrade info] 1 0 0 0 0 0 8 0 0 0 0 0", "[upgrade info] 1 0 0 0 0 0 8 `ignored` 0 0 0 0 0"} {
		_, e := currentContainerUpgradeAccountTiers(dungeonDropCatalogTestSource{"etc/accountcargo.etc": text})
		wantValid := text == "[upgrade info] 1 0 0 0 0 0 8 0 0 0 0 0"
		if (e == nil) != wantValid {
			t.Fatalf("account table accepted malformed tokens: %v", e)
		}
	}
}

func TestUpgradeTicketRemainingCountProjection(t *testing.T) {
	raw := make([]byte, 119)
	binary.LittleEndian.PutUint16(raw, 87)
	binary.LittleEndian.PutUint32(raw[2:], 490701691)
	binary.LittleEndian.PutUint32(raw[6:], 2)
	stack := repo.ItemStack{ItemID: 490701691, Count: 2, RawEntry: raw, Extra: map[string]string{"raw_entry_hex": hex.EncodeToString(raw), "amount": "2", "count": "2"}}
	before := repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": stack}}).Slots["x"]
	after, e := currentPetFeedSetCount(stack, 1)
	if e != nil {
		t.Fatal(e)
	}
	if after.Count != 1 || binary.LittleEndian.Uint32(after.RawEntry[6:]) != 1 || after.Extra["count"] != "1" || after.Extra["amount"] != "1" {
		t.Fatalf("remaining projection %+v", after)
	}
	projected, e := hex.DecodeString(after.Extra["raw_entry_hex"])
	if e != nil || binary.LittleEndian.Uint32(projected[6:]) != 1 {
		t.Fatal("cached row count stale")
	}
	if !reflect.DeepEqual(before, stack) {
		t.Fatal("count projector aliased pretransaction snapshot")
	}
}
