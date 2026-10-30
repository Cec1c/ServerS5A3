package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
)

const (
	vendingEntitlementMachineID   uint32 = 1
	vendingEntitlementCoinID      int64  = 7454
	vendingEntitlementCoinSlot    int16  = 121
	vendingEntitlementCoinCount   int64  = 2
	vendingEntitlementAccountID          = "vending-entitlement-fixture"
	vendingEntitlementCharacterID        = "77"
)

type vendingEntitlementSnapshot struct {
	account        repo.AccountRecord
	accountFound   bool
	character      repo.CharacterRecord
	characterFound bool
	inventory      repo.InventoryRecord
	inventoryFound bool
	mailbox        repo.MailboxRecord
	mailboxFound   bool
}

type vendingEntitlementHarness struct {
	service *Service
	repos   repo.Group
	session *gameSession
	conn    *bufferConn
	logs    *bytes.Buffer
}

func TestRealPVFVendingBlackDiamondEntitlementContract(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cost := vendingEntitlementPVFCost(t, a)
	fixedNow := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		metadata map[string]string
	}{
		{name: "no_black_diamond"},
		{
			name: "exact_now",
			metadata: map[string]string{
				premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(fixedNow.Unix(), 10),
			},
		},
		{
			name: "past",
			metadata: map[string]string{
				premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(fixedNow.Add(-time.Hour).Unix(), 10),
			},
		},
		{
			name: "malformed",
			metadata: map[string]string{
				premium.MetadataKey(premium.TypeBlackDiamond): "not-a-unix-timestamp",
			},
		},
	} {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			h := newVendingEntitlementHarness(t, a, fixedNow, tc.metadata)
			before := vendingEntitlementLoad(t, h.repos)
			if !before.accountFound || !before.characterFound || !before.inventoryFound || before.mailboxFound {
				t.Fatalf("invalid baseline found flags: account=%t character=%t inventory=%t mailbox=%t",
					before.accountFound, before.characterFound, before.inventoryFound, before.mailboxFound)
			}

			if err := h.service.handleCurrentVendingMachine(h.session, vendingEntitlementRequest()); err != nil {
				t.Fatal(err)
			}
			after := vendingEntitlementLoad(t, h.repos)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected entitlement changed persisted records:\nbefore=%+v\nafter=%+v", before, after)
			}

			packets := splitAllGameServerUpperPackets(t, h.conn.write.Bytes())
			if len(packets) != 1 || packets[0].Header.Classification != 1 || packets[0].Header.MsgID != 215 || !bytes.Equal(packets[0].Body, []byte{0, 23}) {
				t.Fatalf("rejection packets=%+v, want one class1 CMD215 black-diamond error body 00 17", packets)
			}
			vendingEntitlementAssertReasonLog(t, h.logs)
		})
	}

	t.Run("future_entitlement_control", func(t *testing.T) {
		metadata := map[string]string{
			premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(fixedNow.Add(time.Hour).Unix(), 10),
		}
		h := newVendingEntitlementHarness(t, a, fixedNow, metadata)
		if err := h.service.handleCurrentVendingMachine(h.session, vendingEntitlementRequest()); err != nil {
			t.Fatal(err)
		}

		packets := splitAllGameServerUpperPackets(t, h.conn.write.Bytes())
		var commandAcks []byte
		ackCount := 0
		for _, packet := range packets {
			if packet.Header.Classification == 1 && packet.Header.MsgID == 215 {
				ackCount++
				commandAcks = packet.Body
			}
		}
		if ackCount != 1 || len(commandAcks) == 0 || commandAcks[0] != 1 {
			t.Fatalf("future entitlement did not succeed through the same PVF/coin path: acks=%d packets=%+v", ackCount, packets)
		}

		after := vendingEntitlementLoad(t, h.repos)
		if !after.accountFound || !after.characterFound || !after.inventoryFound {
			t.Fatalf("successful draw lost baseline records: account=%t character=%t inventory=%t",
				after.accountFound, after.characterFound, after.inventoryFound)
		}
		remaining := int64(0)
		if coin, ok := after.inventory.Slots["0:"+strconv.Itoa(int(vendingEntitlementCoinSlot))]; ok {
			remaining = coin.Count
		}
		if want := vendingEntitlementCoinCount - cost; remaining != want {
			t.Fatalf("successful draw coin remaining=%d want=%d inventory=%+v", remaining, want, after.inventory.Slots)
		}

		settled := 0
		for _, row := range vendingEntitlementLogRows(t, h.logs) {
			if row["msg"] == "dnfbridge black diamond vending settled" {
				settled++
			}
		}
		if settled != 1 {
			t.Fatalf("successful draw logged %d settlements, logs=%s", settled, h.logs.String())
		}
	})
}

func newVendingEntitlementHarness(t *testing.T, a *archive.Archive, now time.Time, metadata map[string]string) *vendingEntitlementHarness {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: vendingEntitlementAccountID, Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: vendingEntitlementCharacterID,
		AccountID:   vendingEntitlementAccountID,
		Level:       90,
		Stats:       map[string]int64{"gold": 7},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: vendingEntitlementCharacterID,
		Slots: map[string]repo.ItemStack{
			"0:" + strconv.Itoa(int(vendingEntitlementCoinSlot)): {
				ItemID: vendingEntitlementCoinID,
				Count:  vendingEntitlementCoinCount,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	logs := new(bytes.Buffer)
	service := &Service{
		options:                 options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"},
		initialEquipmentArchive: a,
		logger:                  slog.New(slog.NewJSONHandler(logs, nil)),
	}
	// Handler decisions and the persisted entitlement boundary use this one
	// fixed gameplay clock. No wall-clock tolerance is involved.
	service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	service.repositoryProvider = func() (repo.Group, bool) { return repos, true }
	service.dungeonChoice = func(limit int) (int, error) {
		if limit <= 0 {
			return 0, fmt.Errorf("empty vending group")
		}
		return 0, nil
	}

	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "vending-entitlement-fixture",
		selectedCharacterID: 77,
		accountID:           vendingEntitlementAccountID,
	}
	return &vendingEntitlementHarness{service: service, repos: repos, session: session, conn: conn, logs: logs}
}

func vendingEntitlementPVFCost(t *testing.T, a *archive.Archive) int64 {
	t.Helper()
	listText, err := a.ReadText("etc/vendingmachine.lst")
	if err != nil {
		t.Fatal(err)
	}
	list, err := doc.Parse("etc/vendingmachine.lst", listText)
	if err != nil {
		t.Fatal(err)
	}
	var machinePath string
	for _, entry := range doc.ParseList(list) {
		if entry.ID == int64(vendingEntitlementMachineID) {
			machinePath = "etc/" + entry.Path
			break
		}
	}
	if machinePath == "" {
		t.Fatalf("current PVF has no machine %d", vendingEntitlementMachineID)
	}
	machineText, err := a.ReadText(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := doc.Parse(machinePath, machineText)
	if err != nil {
		t.Fatal(err)
	}
	cost, rows, ok := parseVendingGroup(machine, 1, vendingEntitlementCoinID)
	if !ok || cost <= 0 || cost >= vendingEntitlementCoinCount || len(rows) == 0 {
		t.Fatalf("machine %d/%s does not contain valid coin %d group: cost=%d rows=%d ok=%t",
			vendingEntitlementMachineID, machinePath, vendingEntitlementCoinID, cost, len(rows), ok)
	}
	return cost
}

func vendingEntitlementRequest() []byte {
	body := make([]byte, 10)
	binary.LittleEndian.PutUint32(body, vendingEntitlementMachineID)
	binary.LittleEndian.PutUint32(body[4:], 1)
	binary.LittleEndian.PutUint16(body[8:], uint16(vendingEntitlementCoinSlot))
	return body
}

func vendingEntitlementLoad(t *testing.T, repos repo.Group) vendingEntitlementSnapshot {
	t.Helper()
	ctx := context.Background()
	account, accountFound, err := repos.Account.Load(ctx, vendingEntitlementAccountID)
	if err != nil {
		t.Fatal(err)
	}
	character, characterFound, err := repos.Character.Load(ctx, vendingEntitlementCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	inventory, inventoryFound, err := repos.Inventory.Load(ctx, vendingEntitlementCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	mailbox, mailboxFound, err := repos.Mailbox.Load(ctx, vendingEntitlementCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	return vendingEntitlementSnapshot{
		account:        account,
		accountFound:   accountFound,
		character:      character,
		characterFound: characterFound,
		inventory:      inventory,
		inventoryFound: inventoryFound,
		mailbox:        mailbox,
		mailboxFound:   mailboxFound,
	}
}

func vendingEntitlementAssertReasonLog(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	const reason = "black diamond required"
	// Existing diagnostics redact raw errors, so bind this row to the exact
	// business reason by its redacted length and digest.
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(reason)))
	seen := 0
	for _, row := range vendingEntitlementLogRows(t, logs) {
		if row["msg"] != "dnfbridge black diamond vending rejected" {
			continue
		}
		seen++
		if row["error"] != "redacted" || row["error_len"] != float64(len(reason)) || row["error_sha256"] != wantHash ||
			row["inventory_full"] != false || row["character_id"] != float64(77) {
			t.Fatalf("rejection log does not identify %q: %+v", reason, row)
		}
	}
	if seen != 1 {
		t.Fatalf("rejection reason logs=%d, want one; logs=%s", seen, logs.String())
	}
}

func vendingEntitlementLogRows(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	raw := strings.TrimSpace(logs.String())
	if raw == "" {
		return nil
	}
	rows := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(raw, "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("invalid vending log row %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	return rows
}
