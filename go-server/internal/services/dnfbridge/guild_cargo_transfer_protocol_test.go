package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
	inventory "longheng.io/server/internal/modules/dnf/inventory"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoWire(t *testing.T, text string) []byte {
	t.Helper()
	value, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGuildCargoTransferNativeFieldWidthsAndOrder(t *testing.T) {
	// Native writer widths: list1, slot2, item4, quantity4, cargo2. A value
	// greater than 255 detects the decompiler's misleading char argument type.
	deposit, ok := parseCurrentGuildCargoDepositRequest(cargoWire(t, "07341278563412030201005600"))
	if !ok || deposit.SourceList != 7 || deposit.SourceSlot != 0x1234 || deposit.ItemID != 0x12345678 || deposit.Count != 0x010203 || deposit.CargoSlot != 0x56 || deposit.GuildID != 0 || deposit.Owner != (r.GuildOwner{}) || deposit.Revalidate != nil {
		t.Fatalf("deposit=%+v ok=%v", deposit, ok)
	}
	deposit.GuildID = 9
	body, err := buildCurrentGuildCargoDepositSuccessBody(deposit, inventory.GuildCargoResult{GuildID: 9, Moved: deposit.Count, Destination: inventory.GuildCargoLocation{Cargo: true, Slot: 0x56}})
	if err != nil || !bytes.Equal(body, cargoWire(t, "073412030201005600")) {
		t.Fatalf("deposit ACK=%x %v", body, err)
	}
	withdraw, ok := parseCurrentGuildCargoWithdrawalRequest(cargoWire(t, "5600785634120302010007"))
	if !ok || withdraw.CargoSlot != 0x56 || withdraw.ItemID != 0x12345678 || withdraw.Count != 0x010203 || withdraw.DestinationList != 7 || withdraw.GuildID != 0 || withdraw.Owner != (r.GuildOwner{}) || withdraw.Revalidate != nil {
		t.Fatalf("withdraw=%+v ok=%v", withdraw, ok)
	}
	withdraw.GuildID = 9
	body, err = buildCurrentGuildCargoWithdrawalSuccessBody(withdraw, inventory.GuildCargoResult{GuildID: 9, Moved: withdraw.Count, Destination: inventory.GuildCargoLocation{List: 7, Slot: 0x1234}})
	if err != nil || !bytes.Equal(body, cargoWire(t, "560007341203020100")) {
		t.Fatalf("withdraw ACK=%x %v", body, err)
	}
}

func TestGuildCargoTransferRejectsTruncationAndTrailingFields(t *testing.T) {
	for n := 0; n <= 16; n++ {
		if _, ok := parseCurrentGuildCargoDepositRequest(make([]byte, n)); ok != (n == 13) {
			t.Fatalf("deposit length%d accepted=%v", n, ok)
		}
		if _, ok := parseCurrentGuildCargoWithdrawalRequest(make([]byte, n)); ok != (n == 11) {
			t.Fatalf("withdrawal length%d accepted=%v", n, ok)
		}
	}
}

func TestGuildCargoTransferACKRequiresMatchingCommittedResult(t *testing.T) {
	d := inventory.GuildCargoDepositRequest{GuildID: 1, Count: 3, CargoSlot: 2}
	w := inventory.GuildCargoWithdrawalRequest{GuildID: 1, Count: 3, CargoSlot: 2, DestinationList: 7}
	for _, value := range []inventory.GuildCargoResult{{}, {GuildID: 2, Moved: 3}, {GuildID: 1, Moved: 2}, {GuildID: 1, Moved: 3, Destination: inventory.GuildCargoLocation{Cargo: true, Slot: 1}}} {
		if body, err := buildCurrentGuildCargoDepositSuccessBody(d, value); err == nil || body != nil {
			t.Fatal("invalid deposit result acknowledged")
		}
		if body, err := buildCurrentGuildCargoWithdrawalSuccessBody(w, value); err == nil || body != nil {
			t.Fatal("invalid withdrawal result acknowledged")
		}
	}
}

func TestGuildCargoTransferCodecWithCommittedInventory(t *testing.T) {
	s, _, repos, _ := cargoQueryFixture(t)
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	g, _, _ := reg.GuildForCharacter("29")
	ch, _, _ := repos.Character.Load(ctx, "29")
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's real inventory owns one 10153530. Destination cargo1 is
	// empty. Wire decoding cannot inject the account/guild/session identity.
	deposit, ok := parseCurrentGuildCargoDepositRequest(cargoWire(t, "0057003aee9a00010000000100"))
	if !ok {
		t.Fatal("native deposit request rejected")
	}
	deposit.Owner = r.GuildOwner{CharacterID: ch.CharacterID, AccountID: ch.AccountID}
	deposit.GuildID = g.ID
	deposit.Revalidate = func() bool { return true }
	rules := inventory.GuildCargoRules{MaxCapacity: 56, Validate: func(r.CharacterRecord, inventory.GuildCargoRequest, r.ItemStack, r.ItemStack) error { return nil }, WithdrawalRange: func(r.CharacterRecord, r.ItemStack) (inventory.GuildCargoSlotRange, error) {
		return inventory.GuildCargoSlotRange{List: 0, First: 87, Last: 89}, nil
	}}
	result, err := owner.DepositGuildCargo(ctx, deposit, rules, s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentGuildCargoDepositSuccessBody(deposit, result)
	if err != nil || !bytes.Equal(body, cargoWire(t, "005700010000000100")) {
		t.Fatalf("deposit ACK=%x %v", body, err)
	}
	withdraw, ok := parseCurrentGuildCargoWithdrawalRequest(cargoWire(t, "01003aee9a000100000000"))
	if !ok {
		t.Fatal("native withdrawal rejected")
	}
	withdraw.Owner, withdraw.GuildID, withdraw.Revalidate = deposit.Owner, g.ID, deposit.Revalidate
	result, err = owner.WithdrawGuildCargo(ctx, withdraw, rules, s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	body, err = buildCurrentGuildCargoWithdrawalSuccessBody(withdraw, result)
	if err != nil || !bytes.Equal(body, cargoWire(t, "010000570001000000")) {
		t.Fatalf("withdrawal ACK=%x %v", body, err)
	}
	reg, _ = guild.Load(ctx, repos.Settings)
	after, _, _ := repos.Inventory.Load(ctx, "29")
	if after.Slots["0:87"].Count != 1 || reg.Guilds[g.ID].Cargo.Slots[1].Count != 0 || len(reg.Guilds[g.ID].Cargo.History) != 3 {
		t.Fatal("wire/domain/ACK disagrees with persisted items/history")
	}
}
