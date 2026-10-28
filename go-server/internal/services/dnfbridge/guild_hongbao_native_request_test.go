package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildHongbaoNativeSenderPurchaseIssueClaimHistory(t *testing.T) {
	// Captured from the matched native sender plus its real raw writer. An
	// optional fresh export exercises the same contract without re-encoding it.
	fixture := []byte(`{"before":{"issue":{"opcode":1303,"body":"a5a5a5a5a5a5a5a5a5a5a5a5a542db341d05"},"claim":{"opcode":1304,"body":"a5a5a5a5a5a5a5a5a5a5a5a5a57b000000"},"history":{"opcode":1302,"body":"a5a5a5a5a5a5a5a5a5a5a5a5a57b000000"}},"after":{"issue":{"opcode":1303,"body":"42db341d05"},"claim":{"opcode":1304,"body":"7b000000"},"history":{"opcode":1302,"body":"7b000000"}}}`)
	if path := os.Getenv("DNF_HONGBAO_NATIVE_REQUESTS"); path != "" {
		var err error
		fixture, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	var capture map[string]map[string]struct {
		Opcode uint16
		Body   string
	}
	// The export also contains SHA strings, so decode only the two cases.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatal(err)
	}
	capture = make(map[string]map[string]struct {
		Opcode uint16
		Body   string
	})
	for _, version := range []string{"before", "after"} {
		var entries map[string]struct {
			Opcode uint16
			Body   string
		}
		if err := json.Unmarshal(envelope[version], &entries); err != nil {
			t.Fatal(err)
		}
		capture[version] = entries
	}
	s, peer, repos := hongbaoBridgeFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Hongbao = &guild.HongbaoState{NextID: 123, Records: map[uint32]guild.HongbaoRecord{}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if ack := hongbaoBridgeSend(t, s, peer, 64, currentCeraShopTestRequestBody(103941), 100); ack[0] != 1 {
		t.Fatalf("purchase=%x", ack)
	}
	seq := uint16(101)
	send := func(version, kind string) []byte {
		t.Helper()
		entry, found := capture[version][kind]
		if !found {
			t.Fatal("missing native body", version, kind)
		}
		body, err := hex.DecodeString(entry.Body)
		if err != nil {
			t.Fatal(err)
		}
		ack := hongbaoBridgeSend(t, s, peer, entry.Opcode, body, seq)
		seq++
		return ack
	}
	for _, kind := range []string{"issue", "claim", "history"} {
		before, _, _ := repos.Inventory.Load(ctx, "29")
		account, _, _ := repos.Account.Load(ctx, peer.accountID)
		registry, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
		if ack := send("before", kind); len(ack) < 2 || ack[0] != 0 {
			t.Fatalf("malformed %s accepted: %x", kind, ack)
		}
		after, _, _ := repos.Inventory.Load(ctx, "29")
		afterAccount, _, _ := repos.Account.Load(ctx, peer.accountID)
		afterRegistry, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, afterAccount) || !reflect.DeepEqual(registry, afterRegistry) {
			t.Fatal("malformed request changed assets")
		}
	}
	issue := send("after", "issue")
	if len(issue) != 13 || issue[0] != 1 || binary.LittleEndian.Uint32(issue[9:]) != 123 {
		t.Fatalf("issue=%x", issue)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "29")
	for _, item := range inv.Slots {
		if item.ItemID == 490003266 && item.Count > 0 {
			t.Fatal("issued item not consumed")
		}
	}
	list := hongbaoBridgeSend(t, s, peer, 1300, nil, seq)
	seq++
	if len(list) != 55 || binary.LittleEndian.Uint32(list[11:]) != 1 || binary.LittleEndian.Uint32(list[15:]) != 123 || list[53] != 5 {
		t.Fatalf("list=%x", list)
	}
	claim := send("after", "claim")
	if len(claim) != 13 || claim[0] != 1 {
		t.Fatalf("claim=%x", claim)
	}
	amount := binary.LittleEndian.Uint32(claim[9:])
	account, _, _ := repos.Account.Load(ctx, peer.accountID)
	if amount == 0 || cerashop.HappyTokenCeraBalance(account) != int64(amount) {
		t.Fatal("claim not credited")
	}
	history := send("after", "history")
	if len(history) != 41 || history[0] != 1 || binary.LittleEndian.Uint32(history[1:]) != 1 || binary.LittleEndian.Uint32(history[36:]) != amount {
		t.Fatalf("history=%x", history)
	}
	if ack := send("after", "claim"); !bytes.Equal(ack, []byte{0, 34}) {
		t.Fatalf("duplicate claim=%x", ack)
	}
	exportHongbaoProduction(t, "native-issue", issue)
	exportHongbaoProduction(t, "native-list", list)
	exportHongbaoProduction(t, "native-claim", claim)
	exportHongbaoProduction(t, "native-history", history)
}
