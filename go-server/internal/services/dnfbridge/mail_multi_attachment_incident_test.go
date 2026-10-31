package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	mailIncidentEvidenceDirEnv = "DNFBRIDGE_MAIL_INCIDENT_EVIDENCE_DIR"
	mailIncidentAccountID      = "synthetic-mail-multi-attachment"
	mailIncidentCharacterKey   = "97"
	mailIncidentCharacterID    = uint16(97)
	mailIncidentMailKey        = "1"
	mailIncidentClaimID0       = uint32(16)
	mailIncidentClaimID1       = uint32(20)
	mailIncidentClaimID2       = uint32(21)
)

var mailIncidentItems = []struct {
	itemID    uint32
	count     int64
	stackable string
}{
	{itemID: 490701739, count: 1, stackable: "[booster selection]"},
	{itemID: 490701763, count: 2, stackable: "[booster selection]"},
	{itemID: 490701742, count: 1, stackable: "[coupon]"},
}

var mailIncidentClaimIDs = []uint32{mailIncidentClaimID0, mailIncidentClaimID1, mailIncidentClaimID2}

type mailIncidentFixture struct {
	service *Service
	repos   dnfrepo.Group
	session *gameSession
	conn    *bufferConn
}

// This fixture uses synthetic identities with three distinct durable claim IDs.
// Current-client single-attachment claim sends count=1 plus one claim ID;
// both single and bulk acceptance must preserve the native mailbox managers.
func newMailIncidentFixture(t *testing.T, catalog *pvfDungeonDropCatalog) *mailIncidentFixture {
	t.Helper()
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: mailIncidentAccountID, State: "active"}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: mailIncidentCharacterKey, AccountID: mailIncidentAccountID, Slot: 0, Name: "mail-incident", Job: "0", Level: 90,
		Stats: map[string]int64{"gold": 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: mailIncidentCharacterKey, Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	service := &Service{
		options: options{
			gameUpperHeader:          gameUpperHeaderServer16,
			gameUpperBodyCodec:       gameUpperBodyCodecPlain,
			gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain,
			currentClientVersion:     "official-24.9",
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil || resolver == nil {
		t.Fatalf("create real-PVF mailbox resolver err=%v", err)
	}

	attachments := make([]dnfrepo.MailAttachment, 0, len(mailIncidentItems))
	for _, expected := range mailIncidentItems {
		definition, err := catalog.ResolveItem(expected.itemID)
		if err != nil {
			t.Fatalf("resolve real-PVF item %d: %v", expected.itemID, err)
		}
		if definition.Kind != dungeonDropItemStackable ||
			!strings.EqualFold(strings.TrimSpace(definition.StackableType), expected.stackable) {
			t.Fatalf("item %d kind/type=%q/%q, want stackable/%s", expected.itemID, definition.Kind, definition.StackableType, expected.stackable)
		}
		resolution, err := resolver(expected.itemID)
		if err != nil {
			t.Fatalf("resolve mailbox attachment %d: %v", expected.itemID, err)
		}
		extra := make(map[string]string, len(resolution.DefaultExtra)+8)
		for key, value := range resolution.DefaultExtra {
			extra[key] = value
		}
		extra["item_kind"] = resolution.Kind
		extra["pvf_path"] = resolution.PVFPath
		extra["stackable_type"] = definition.StackableType
		extra["mailbox_pvf_kind"] = resolution.Kind
		extra["mailbox_pvf_path"] = resolution.PVFPath
		extra["mailbox_equipment_type"] = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(definition.StackableType), "["), "]")
		extra["source"] = "cera_package"
		extra["last_grant_source"] = "cera_package"
		extra["cera_package_overflow_mail"] = "true"
		expireAt := resolution.ExpireAt
		if expireAt.IsZero() {
			expireAt = definition.ExpirationDate
		}
		if !expireAt.IsZero() {
			value := fmt.Sprintf("%d", expireAt.Unix())
			extra["expire_time"] = value
			extra["expire_unix"] = value
		}
		attachments = append(attachments, dnfrepo.MailAttachment{
			ItemID: int64(expected.itemID), Count: expected.count, ExpireAt: expireAt, Extra: extra,
		})
	}

	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: mailIncidentCharacterKey,
		Mails: map[string]dnfrepo.MailRecord{
			mailIncidentMailKey: {
				MailID:            mailIncidentMailKey,
				SenderCharacterID: "0",
				SenderName:        "系统",
				Title:             "背包已满：礼包奖励",
				Read:              true,
				CreatedAt:         time.Date(2026, 10, 24, 16, 11, 0, 593486000, time.UTC),
				Attachments:       attachments,
				Metadata: map[string]string{
					"attachment_grant_anchor": "system_mail_created_at_v1",
					"mailbox_claim_highwater": "22",
					"mailbox_claim_ids":       "16,20,21",
					"source":                  "cera_package_main_inventory_full",
					"system_mail":             "true",
					"unlimited":               "true",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save synthetic incident mailbox: %v", err)
	}

	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "mail-multi-attachment-incident",
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		accountID:           mailIncidentAccountID,
		accountFromToken:    true,
		loginPacketSeen:     true,
		selectedCharacterID: mailIncidentCharacterID,
	}
	service.bindGameSessionCharacter(session, mailIncidentCharacterID)
	return &mailIncidentFixture{service: service, repos: repositories, session: session, conn: connection}
}

func (f *mailIncidentFixture) mailboxOpen(t *testing.T, evidenceName string) []dnfproto.ChannelPacket {
	t.Helper()
	f.conn.write.Reset()
	handled, err := f.service.handleAlignedGameCommand(
		f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMailboxOpen), nil,
	)
	if err != nil || !handled {
		t.Fatalf("mailbox open handled=%t err=%v", handled, err)
	}
	packets := splitProfessionTrialServer16Packets(t, append([]byte(nil), f.conn.write.Bytes()...))
	mailIncidentWriteEvidence(t, evidenceName, packets)
	return packets
}

func mailIncidentAssertClaimPackets(t *testing.T, packets []dnfproto.ChannelPacket, ids []uint32) {
	t.Helper()
	if len(packets) == 0 || packets[0].Header.MsgID != 95 || packets[0].Header.Classification != 1 {
		t.Fatalf("missing claim ACK: %+v", packets)
	}
	ack := packets[0].Body
	if len(ack) != 5+12*len(ids) || ack[0] != 1 || binary.LittleEndian.Uint32(ack[1:5]) != uint32(len(ids)) {
		t.Fatalf("claim ACK body=%x ids=%v", ack, ids)
	}
	for i, id := range ids {
		row := ack[5+i*12 : 17+i*12]
		if binary.LittleEndian.Uint32(row[:4]) != 1 || binary.LittleEndian.Uint32(row[4:8]) != id || binary.LittleEndian.Uint32(row[8:]) != 0 {
			t.Fatalf("claim %d identity changed: %x", id, row)
		}
	}
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == 97 {
			t.Fatal("claim rebuilt native mailbox managers while detail view could retain a parent")
		}
	}
}

func (f *mailIncidentFixture) claimIDs(t *testing.T, evidenceName string, ids ...uint32) []dnfproto.ChannelPacket {
	t.Helper()
	if len(ids) == 0 {
		t.Fatal("claim request has no IDs")
	}
	f.conn.write.Reset()
	handled, err := f.service.handleAlignedGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMailboxExtractItem),
		mailIncidentIDListBody(ids...),
	)
	if err != nil || !handled {
		t.Fatalf("mailbox claim handled=%t ids=%v err=%v", handled, ids, err)
	}
	packets := splitProfessionTrialServer16Packets(t, append([]byte(nil), f.conn.write.Bytes()...))
	mailIncidentWriteEvidence(t, evidenceName, packets)
	mailIncidentAssertClaimPackets(t, packets, ids)
	return packets
}

func TestRealPVFMailMultiAttachmentIncidentSingleVersusAllAccept(t *testing.T) {
	_, catalog := realPVF535(t)

	t.Run("single_attachment_requests_then_reopen", func(t *testing.T) {
		f := newMailIncidentFixture(t, catalog)
		f.mailboxOpen(t, "single-open")

		f.claimIDs(t, "single-claim-16", mailIncidentClaimID0)
		mailIncidentAssertState(t, f, []uint32{490701763, 490701742}, []int64{2, 1}, false, map[uint32]int64{
			490701739: 1, 490701763: 0, 490701742: 0,
		})

		f.claimIDs(t, "single-claim-20", mailIncidentClaimID1)
		mailIncidentAssertState(t, f, []uint32{490701742}, []int64{1}, false, map[uint32]int64{
			490701739: 1, 490701763: 2, 490701742: 0,
		})

		f.claimIDs(t, "single-claim-21", mailIncidentClaimID2)
		mailIncidentAssertState(t, f, nil, nil, true, map[uint32]int64{
			490701739: 1, 490701763: 2, 490701742: 1,
		})

		// Only explicit reopening reconstructs the mailbox manager.
		before := mailIncidentLoadMailbox(t, f.repos)
		f.mailboxOpen(t, "single-reopen")
		after := mailIncidentLoadMailbox(t, f.repos)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("mailbox reopen changed durable state")
		}
	})

	t.Run("all_accept_comparison", func(t *testing.T) {
		f := newMailIncidentFixture(t, catalog)
		f.mailboxOpen(t, "all-open")
		packets := f.claimIDs(t, "all-claim", mailIncidentClaimIDs...)
		for _, packet := range packets {
			sum := sha256.Sum256(packet.Body)
			t.Logf("all-accept response msg=%d class=%d body_sha256=%x", packet.Header.MsgID, packet.Header.Classification, sum[:])
		}
		// The same ACK-only lifecycle applies to bulk acceptance.
		mailIncidentAssertState(t, f, nil, nil, true, map[uint32]int64{
			490701739: 1, 490701763: 2, 490701742: 1,
		})
	})
}

func mailIncidentAssertState(
	t *testing.T,
	f *mailIncidentFixture,
	wantItems []uint32,
	wantCounts []int64,
	wantClaimed bool,
	wantInventory map[uint32]int64,
) {
	t.Helper()
	mailbox := mailIncidentLoadMailbox(t, f.repos)
	mail, found := mailbox.Mails[mailIncidentMailKey]
	if !found {
		t.Fatal("incident mail disappeared")
	}
	if mail.Claimed != wantClaimed || len(mail.Attachments) != len(wantItems) {
		t.Fatalf("mail claimed/attachments=%t/%d, want %t/%d: %+v", mail.Claimed, len(mail.Attachments), wantClaimed, len(wantItems), mail)
	}
	for i, itemID := range wantItems {
		if mail.Attachments[i].ItemID != int64(itemID) || mail.Attachments[i].Count != wantCounts[i] {
			t.Fatalf("remaining attachment %d=%d x%d, want %d x%d", i, mail.Attachments[i].ItemID, mail.Attachments[i].Count, itemID, wantCounts[i])
		}
	}
	inventory, found, err := f.repos.Inventory.Load(context.Background(), mailIncidentCharacterKey)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	for itemID, want := range wantInventory {
		if got := mailIncidentInventoryCount(inventory, itemID); got != want {
			t.Fatalf("inventory item %d count=%d, want %d", itemID, got, want)
		}
	}
}

func mailIncidentIDListBody(ids ...uint32) []byte {
	body := make([]byte, 4+4*len(ids))
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(ids)))
	for i, id := range ids {
		binary.LittleEndian.PutUint32(body[4+4*i:8+4*i], id)
	}
	return body
}

func mailIncidentInventoryCount(inventory dnfrepo.InventoryRecord, itemID uint32) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(itemID) {
			count += stack.Count
		}
	}
	return count
}

func mailIncidentLoadMailbox(t *testing.T, repositories dnfrepo.Group) dnfrepo.MailboxRecord {
	t.Helper()
	mailbox, found, err := repositories.Mailbox.Load(context.Background(), mailIncidentCharacterKey)
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	return mailbox
}

func mailIncidentWriteEvidence(t *testing.T, scenario string, packets []dnfproto.ChannelPacket) {
	t.Helper()
	root := strings.TrimSpace(os.Getenv(mailIncidentEvidenceDirEnv))
	if root == "" {
		t.Logf("mail incident raw bodies not exported; set %s to an evidence directory", mailIncidentEvidenceDirEnv)
		return
	}
	dir := filepath.Join(root, scenario)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create mail incident evidence dir %q: %v", dir, err)
	}
	type manifestRow struct {
		File           string `json:"file"`
		MsgID          uint16 `json:"msg_id"`
		Classification byte   `json:"classification"`
		BodySize       int    `json:"body_size"`
		BodySHA256     string `json:"body_sha256"`
	}
	manifest := make([]manifestRow, 0, len(packets))
	for i, packet := range packets {
		if packet.Header.MsgID != 14 && packet.Header.MsgID != 95 && packet.Header.MsgID != 96 && packet.Header.MsgID != 97 {
			continue
		}
		name := fmt.Sprintf("%02d-msg%03d-class%d.body.bin", i, packet.Header.MsgID, packet.Header.Classification)
		body := append([]byte(nil), packet.Body...)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatalf("write mail incident body %q: %v", name, err)
		}
		sum := sha256.Sum256(body)
		manifest = append(manifest, manifestRow{
			File: name, MsgID: packet.Header.MsgID, Classification: packet.Header.Classification,
			BodySize: len(body), BodySHA256: fmt.Sprintf("%x", sum[:]),
		})
	}
	encoded, err := json.MarshalIndent(map[string]any{
		"schema": "mail-incident-raw-bodies-v1", "scenario": scenario,
		"pvf_env": "DNFBRIDGE_REAL_PVF_SMOKE", "bodies": manifest,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal mail incident manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(encoded, '\n'), 0644); err != nil {
		t.Fatalf("write mail incident manifest: %v", err)
	}
	t.Logf("mail incident raw bodies exported: %s", dir)
}
