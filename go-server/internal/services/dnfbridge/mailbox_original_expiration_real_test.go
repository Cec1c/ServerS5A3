package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	p157RealMailItemID    = uint32(10304122)
	p157RealMailPVFSHA256 = "a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290"
)

// TestRealPVFMailboxOriginalExpiry drives the formal op95 command against the real a56
// PVF. It covers the original CreatedAt+[usable period] anchor in both expired
// and still-live states, explicit attachment deadlines taking precedence, a
// missing anchor failing without moving assets, real list0/op14 0x38/0x0B
// values, durable ExpireAt/Extra agreement, and repeated claim idempotency.
//
// A persisted RawEntry is optional; the wire row is read from the real packet.
//
// Every case runs with the default empty value, an invalid value that must also
// behave as default, and the legacy repair mode. The default pre-claim repair is
// part of the required path: a relative template must not replace an existing
// explicit attachment deadline before the claim domain sees it.
func TestRealPVFMailboxOriginalExpiry(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the active Script.pvf")
	}
	if sha := p157RealMailSHA256(t, pvfPath); sha != p157RealMailPVFSHA256 {
		t.Fatalf("PVF SHA256=%s, want a56=%s", sha, p157RealMailPVFSHA256)
	}

	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open a56 archive: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}
	definition, err := catalog.ResolveItem(p157RealMailItemID)
	if err != nil {
		t.Fatalf("resolve item %d: %v", p157RealMailItemID, err)
	}
	if definition.Kind != dungeonDropItemStackable || definition.StackLimit != 1 ||
		definition.UsablePeriodDays != 7 || !definition.ExpirationDate.IsZero() {
		t.Fatalf("unexpected a56 item definition: %+v", definition)
	}
	if definition.PVFPath != "stackable/10304001/10304122.stk" {
		t.Fatalf("canonical PVF path=%q", definition.PVFPath)
	}

	t.Logf("mail matrix input: pvf_sha=%s item=%d path=%s stack_limit=%d usable_period_days=%d absolute_expiry=%q",
		p157RealMailPVFSHA256, p157RealMailItemID, definition.PVFPath, definition.StackLimit,
		definition.UsablePeriodDays, definition.ExpirationDate.Format(time.RFC3339))

	// Keep the claimed-letter state deterministic across both trees. This is an
	// existing production switch; the test does not change runtime state.
	t.Setenv("DNFBRIDGE_MAILBOX_CLAIMED_LETTER", "finish")

	now := time.Now().UTC().Truncate(time.Second)
	const day = 24 * time.Hour
	cases := []p157RealMailCase{
		{
			name:        "created_anchor_expired_7d",
			createdAt:   now.Add(-8 * day),
			wantExpiry:  now.Add(-1 * day),
			wantUse:     0,
			wantClaimed: true,
		},
		{
			name:        "created_anchor_live_7d",
			createdAt:   now.Add(-2 * day),
			wantExpiry:  now.Add(5 * day),
			wantUse:     65535,
			wantClaimed: true,
		},
		{
			name:             "explicit_live_precedes_live_anchor",
			createdAt:        now.Add(-2 * day),
			explicitExpireAt: now.Add(3 * day),
			wantExpiry:       now.Add(3 * day),
			wantUse:          65535,
			wantClaimed:      true,
		},
		{
			name:             "explicit_live_precedes_expired_anchor",
			createdAt:        now.Add(-8 * day),
			explicitExpireAt: now.Add(3 * day),
			wantExpiry:       now.Add(3 * day),
			wantUse:          65535,
			wantClaimed:      true,
		},
		{
			name:             "explicit_expired_precedes_live_anchor",
			createdAt:        now.Add(-1 * day),
			explicitExpireAt: now.Add(-1 * time.Hour),
			wantExpiry:       now.Add(-1 * time.Hour),
			wantUse:          0,
			wantClaimed:      true,
		},
		{
			name:        "extra_only_valid_deadline",
			createdAt:   now.Add(-2 * day),
			extra:       p157RealMailGMOverrideExtra(now.Add(4 * day)),
			wantExpiry:  now.Add(4 * day),
			wantUse:     65535,
			wantClaimed: true,
			gmValues:    []string{"", "on"},
		},
		{
			name:             "extra_conflicts_with_expire_at",
			createdAt:        now.Add(-2 * day),
			explicitExpireAt: now.Add(6 * day),
			extra:            p157RealMailGMOverrideExtra(now.Add(4 * day)),
			wantExpiry:       now.Add(4 * day),
			wantUse:          65535,
			wantClaimed:      true,
			gmValues:         []string{"", "on"},
		},
		{
			name:        "missing_created_anchor_rejected",
			createdAt:   time.Time{},
			wantClaimed: false,
		},
		{
			name: "player_missing_instance_rejected", createdAt: now.Add(-day),
			sender: "42", source: "op94", wantClaimed: false,
		},
		{
			name: "auction_missing_instance_rejected", createdAt: now.Add(-day),
			sender: "0", source: "auction_listing_return", wantClaimed: false,
		},
		{
			name: "auction_original_expired_preserved", createdAt: now.Add(-day),
			sender: "0", source: "auction_purchase_delivery",
			explicitExpireAt: now.Add(-time.Hour), wantExpiry: now.Add(-time.Hour),
			wantUse: 0, wantClaimed: true,
		},
	}

	modes := []struct {
		name  string
		value string
	}{
		{name: "default_empty", value: ""},
		{name: "default_invalid", value: "not-a-supported-mode"},
		{name: "legacy", value: mailboxClaimTypeRepairValueOff},
	}
	for _, mode := range modes {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			t.Setenv(mailboxClaimTypeRepairEnv, mode.value)
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					gmValues := tc.gmValues
					if len(gmValues) == 0 {
						gmValues = []string{""}
					}
					for _, gmValue := range gmValues {
						gmName := "gm_off"
						if strings.EqualFold(strings.TrimSpace(gmValue), "on") {
							gmName = "gm_on"
						}
						t.Run(gmName, func(t *testing.T) {
							t.Setenv(currentGMExpirationOverrideEnv, gmValue)
							p157RealMailRunCase(t, catalog, mode.name, gmName, tc)
						})
					}
				})
			}
		})
	}
}

type p157RealMailCase struct {
	name             string
	sender           string
	source           string
	createdAt        time.Time
	explicitExpireAt time.Time
	extra            map[string]string
	wantExpiry       time.Time
	wantUse          uint16
	wantClaimed      bool
	gmValues         []string
}

func p157RealMailSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open a56 PVF: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("hash a56 PVF: %v", err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func p157RealMailIDsBody(ids ...uint32) []byte {
	out := make([]byte, 0, 4+4*len(ids))
	var number [4]byte
	binary.LittleEndian.PutUint32(number[:], uint32(len(ids)))
	out = append(out, number[:]...)
	for _, id := range ids {
		binary.LittleEndian.PutUint32(number[:], id)
		out = append(out, number[:]...)
	}
	return out
}

func p157RealMailGMOverrideExtra(expiry time.Time) map[string]string {
	unix := strconv.FormatInt(expiry.Unix(), 10)
	return map[string]string{
		"expire_time":       unix,
		"expire_unix":       unix,
		"expiration_source": "gm_override",
		"gm_expire_days":    "4",
	}
}

func p157RealMailRunCase(t *testing.T, catalog *pvfDungeonDropCatalog, modeName string, gmName string, tc p157RealMailCase) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	const characterID = "77"
	accountID := "acc-p157-r109-" + modeName + "-" + gmName + "-" + tc.name
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Name:        "p157-" + modeName + "-" + gmName + "-" + tc.name,
		Job:         "0",
		Level:       90,
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	attachment := dnfrepo.MailAttachment{ItemID: int64(p157RealMailItemID), Count: 1}
	if !tc.explicitExpireAt.IsZero() {
		attachment.ExpireAt = tc.explicitExpireAt
	}
	if len(tc.extra) > 0 {
		attachment.Extra = make(map[string]string, len(tc.extra))
		for key, value := range tc.extra {
			attachment.Extra[key] = value
		}
	}
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: characterID,
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID:            "1",
				CreatedAt:         tc.createdAt,
				SenderCharacterID: tc.sender,
				Metadata:          map[string]string{"source": tc.source},
				Attachments:       []dnfrepo.MailAttachment{attachment},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	seeded, found, err := repositories.Mailbox.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load seeded mailbox found=%t err=%v", found, err)
	}
	seededAttachment := seeded.Mails["1"].Attachments[0]
	t.Logf("P157 repair=%s gm=%s case=%s fixture: created_at=%q explicit_expire_at=%q attachment_extra=%v raw_len=%d",
		modeName, gmName, tc.name, p157RealMailTimeText(tc.createdAt), p157RealMailTimeText(tc.explicitExpireAt),
		seededAttachment.Extra, len(seededAttachment.RawEntry))

	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                &bufferConn{},
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		accountID:           accountID,
		accountFromToken:    true,
		loginPacketSeen:     true,
		selectedCharacterID: 77,
	}
	service.bindGameSessionCharacter(session, 77)

	handled, err := p157RealMailClaim(t, service, session, 1)
	if err != nil {
		t.Fatalf("formal op95 claim: %v", err)
	}
	if !handled {
		t.Fatal("formal op95 claim was not handled")
	}
	ackOK, ackCode := p157RealMailAck(t, service, session)
	rows := p157RealMailList0Rows(t, service, session)
	inventory := p157RealMailLoadInventory(t, ctx, repositories, characterID)
	stacks, totalCount := p157RealMailItemStacks(inventory, p157RealMailItemID)
	mailbox := p157RealMailLoadMailbox(t, ctx, repositories, characterID)

	var stack dnfrepo.ItemStack
	if len(stacks) > 0 {
		stack = stacks[0]
	}
	t.Logf("P157 repair=%s gm=%s case=%s claim: ack_ok=%t ack_code=%d item_rows=%d item_total=%d persisted_expire_at=%q extra_expire_time=%q extra_expire_unix=%q raw_len=%d wire_rows=%d",
		modeName, gmName, tc.name, ackOK, ackCode, len(stacks), totalCount, p157RealMailTimeText(stack.ExpireAt),
		p157RealMailExtra(stack.Extra, "expire_time"), p157RealMailExtra(stack.Extra, "expire_unix"),
		len(stack.RawEntry), len(rows))
	if len(rows) == 1 {
		t.Logf("P157 repair=%s gm=%s case=%s claim wire: 0x38=%d 0x0b=%d row=%x", modeName, gmName, tc.name,
			binary.LittleEndian.Uint32(rows[0][currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4]),
			binary.LittleEndian.Uint16(rows[0][0x0B:0x0D]), rows[0])
	}

	if tc.wantClaimed {
		p157RealMailAssertClaimAccepted(t, tc, ackOK, ackCode, stacks, totalCount, stack, rows)
		p157RealMailAssertRepeatClaim(t, service, session, ctx, repositories, characterID, modeName, gmName, tc.name)
		return
	}

	p157RealMailAssertClaimRejected(t, tc, ackOK, ackCode, stacks, totalCount, rows, inventory, mailbox)
	p157RealMailAssertRepeatRejection(t, service, session, ctx, repositories, characterID, modeName, gmName, tc.name)
}

func p157RealMailAssertClaimAccepted(
	t *testing.T,
	tc p157RealMailCase,
	ackOK bool,
	ackCode byte,
	stacks []dnfrepo.ItemStack,
	totalCount int64,
	stack dnfrepo.ItemStack,
	rows [][]byte,
) {
	t.Helper()
	if !ackOK {
		t.Errorf("P157 claim rejected with ack_code=%d; want success", ackCode)
	}
	if len(stacks) != 1 || totalCount != 1 {
		t.Errorf("claimed rows=%d total=%d, want exactly one item with count 1", len(stacks), totalCount)
	}
	if stack.ExpireAt.IsZero() || !stack.ExpireAt.Equal(tc.wantExpiry) {
		t.Errorf("persisted ExpireAt=%s want=%s", p157RealMailTimeText(stack.ExpireAt), p157RealMailTimeText(tc.wantExpiry))
	}
	wantText := strconv.FormatInt(tc.wantExpiry.Unix(), 10)
	if got := strings.TrimSpace(p157RealMailExtra(stack.Extra, "expire_time")); got != wantText {
		t.Errorf("Extra[expire_time]=%q want=%q", got, wantText)
	}
	if got := strings.TrimSpace(p157RealMailExtra(stack.Extra, "expire_unix")); got != wantText {
		t.Errorf("Extra[expire_unix]=%q want=%q", got, wantText)
	}
	if len(rows) != 1 {
		t.Errorf("claim emitted %d list0/op14 rows for item %d, want 1", len(rows), p157RealMailItemID)
		return
	}
	wireExpiry := binary.LittleEndian.Uint32(rows[0][currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4])
	wireUse := binary.LittleEndian.Uint16(rows[0][0x0B:0x0D])
	if want := uint32(tc.wantExpiry.Unix()); wireExpiry != want {
		t.Errorf("list0/op14 0x38=%d want=%d", wireExpiry, want)
	}
	if wireUse != tc.wantUse {
		t.Errorf("list0/op14 0x0b=%d want=%d", wireUse, tc.wantUse)
	}
}

func p157RealMailAssertRepeatClaim(
	t *testing.T,
	service *Service,
	session *gameSession,
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	modeName string,
	gmName string,
	caseName string,
) {
	t.Helper()
	handled, err := p157RealMailClaim(t, service, session, 1)
	if err != nil || !handled {
		t.Fatalf("repeat op95 claim handled=%t err=%v", handled, err)
	}
	ackOK, ackCode := p157RealMailAck(t, service, session)
	rows := p157RealMailList0Rows(t, service, session)
	inventory := p157RealMailLoadInventory(t, ctx, repositories, characterID)
	_, totalCount := p157RealMailItemStacks(inventory, p157RealMailItemID)
	t.Logf("P157 repair=%s gm=%s case=%s repeat: ack_ok=%t ack_code=%d item_total=%d wire_rows=%d", modeName, gmName, caseName, ackOK, ackCode, totalCount, len(rows))
	if totalCount != 1 {
		t.Errorf("repeat claim changed item total to %d, want 1", totalCount)
	}
	if len(rows) != 0 {
		t.Errorf("repeat claim emitted %d item rows, want 0", len(rows))
	}
}

func p157RealMailAssertClaimRejected(
	t *testing.T,
	tc p157RealMailCase,
	ackOK bool,
	ackCode byte,
	stacks []dnfrepo.ItemStack,
	totalCount int64,
	rows [][]byte,
	inventory dnfrepo.InventoryRecord,
	mailbox dnfrepo.MailboxRecord,
) {
	t.Helper()
	if ackOK {
		t.Errorf("claim with no original anchor was accepted; want failure")
	}
	if len(stacks) != 0 || totalCount != 0 {
		t.Errorf("rejected claim created item rows=%d total=%d", len(stacks), totalCount)
	}
	if len(rows) != 0 {
		t.Errorf("rejected claim emitted %d item rows", len(rows))
	}
	if len(inventory.Slots) != 0 {
		t.Errorf("rejected claim changed inventory: %+v", inventory.Slots)
	}
	mail, found := mailbox.Mails["1"]
	if !found {
		t.Errorf("rejected claim removed mail 1")
		return
	}
	if mail.Claimed || mail.Deleted {
		t.Errorf("rejected claim changed mail state claimed=%t deleted=%t", mail.Claimed, mail.Deleted)
	}
	if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != int64(p157RealMailItemID) || mail.Attachments[0].Count != 1 {
		t.Errorf("rejected claim changed attachment: %+v", mail.Attachments)
		return
	}
	t.Logf("P157 rejected no-anchor preserved attachment: expire_at=%q extra=%v raw_len=%d",
		p157RealMailTimeText(mail.Attachments[0].ExpireAt), mail.Attachments[0].Extra, len(mail.Attachments[0].RawEntry))
	if !mail.Attachments[0].ExpireAt.IsZero() {
		t.Errorf("rejected claim invented attachment expiry %s", mail.Attachments[0].ExpireAt)
	}
	if !mail.CreatedAt.Equal(tc.createdAt) {
		t.Errorf("rejected claim changed mail CreatedAt=%s want=%s", p157RealMailTimeText(mail.CreatedAt), p157RealMailTimeText(tc.createdAt))
	}
}

func p157RealMailAssertRepeatRejection(
	t *testing.T,
	service *Service,
	session *gameSession,
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	modeName string,
	gmName string,
	caseName string,
) {
	t.Helper()
	handled, err := p157RealMailClaim(t, service, session, 1)
	if err != nil || !handled {
		t.Fatalf("repeat rejected op95 claim handled=%t err=%v", handled, err)
	}
	ackOK, ackCode := p157RealMailAck(t, service, session)
	rows := p157RealMailList0Rows(t, service, session)
	inventory := p157RealMailLoadInventory(t, ctx, repositories, characterID)
	_, totalCount := p157RealMailItemStacks(inventory, p157RealMailItemID)
	mailbox := p157RealMailLoadMailbox(t, ctx, repositories, characterID)
	mail, found := mailbox.Mails["1"]
	t.Logf("P157 repair=%s gm=%s case=%s repeat-rejected: ack_ok=%t ack_code=%d item_total=%d wire_rows=%d mail_found=%t claimable=%t",
		modeName, gmName, caseName, ackOK, ackCode, totalCount, len(rows), found, found && !mail.Claimed && !mail.Deleted)
	if ackOK {
		t.Errorf("repeat no-anchor claim was accepted; want rejection")
	}
	if totalCount != 0 || len(rows) != 0 {
		t.Errorf("repeat no-anchor claim created inventory rows=%d wire_rows=%d", totalCount, len(rows))
	}
	if !found || mail.Claimed || mail.Deleted || len(mail.Attachments) != 1 {
		t.Errorf("repeat no-anchor claim did not preserve mail: found=%t mail=%+v", found, mail)
	}
}

func p157RealMailClaim(t *testing.T, service *Service, session *gameSession, mailID uint32) (bool, error) {
	t.Helper()
	conn, ok := session.conn.(*bufferConn)
	if !ok || conn == nil {
		t.Fatal("claim session has no buffer connection")
	}
	conn.write.Reset()
	return service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMailboxExtractItem),
		p157RealMailIDsBody(mailID),
	)
}

func p157RealMailAck(t *testing.T, service *Service, session *gameSession) (bool, byte) {
	t.Helper()
	conn := session.conn.(*bufferConn)
	data := append([]byte(nil), conn.write.Bytes()...)
	headerSize := service.gameUpperHeaderSize()
	success := false
	code := byte(0)
	found := false
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, data, headerSize)
		data = rest
		if packet.Header.MsgID != uint16(dnfenum.CmdPacketMailboxExtractItem) {
			continue
		}
		found = true
		if len(packet.Body) == 0 {
			t.Fatal("op95 ACK has an empty body")
		}
		if packet.Body[0] == 0x01 {
			success = true
			continue
		}
		success = false
		if len(packet.Body) > 1 {
			code = packet.Body[1]
		}
	}
	if !found {
		t.Fatal("claim response has no op95 ACK")
	}
	return success, code
}

func p157RealMailList0Rows(t *testing.T, service *Service, session *gameSession) [][]byte {
	t.Helper()
	conn := session.conn.(*bufferConn)
	data := append([]byte(nil), conn.write.Bytes()...)
	headerSize := service.gameUpperHeaderSize()
	rows := make([][]byte, 0, 1)
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, data, headerSize)
		data = rest
		if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			continue
		}
		body := packet.Body
		if len(body) < 3 || body[0] != dnfrepo.MainInventoryListType {
			continue
		}
		count := int(binary.LittleEndian.Uint16(body[1:3]))
		if count < 0 || count > (len(body)-3)/currentItemListEntryWireSize ||
			len(body) != 3+count*currentItemListEntryWireSize {
			t.Fatalf("malformed list0/op14 body: len=%d count=%d body=%x", len(body), count, body)
		}
		for index := 0; index < count; index++ {
			start := 3 + index*currentItemListEntryWireSize
			row := body[start : start+currentItemListEntryWireSize]
			if binary.LittleEndian.Uint32(row[0x02:0x06]) != p157RealMailItemID {
				continue
			}
			rows = append(rows, append([]byte(nil), row...))
		}
	}
	return rows
}

func p157RealMailLoadInventory(t *testing.T, ctx context.Context, repositories dnfrepo.Group, characterID string) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

func p157RealMailLoadMailbox(t *testing.T, ctx context.Context, repositories dnfrepo.Group, characterID string) dnfrepo.MailboxRecord {
	t.Helper()
	mailbox, found, err := repositories.Mailbox.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	return mailbox
}

func p157RealMailItemStacks(inventory dnfrepo.InventoryRecord, itemID uint32) ([]dnfrepo.ItemStack, int64) {
	stacks := make([]dnfrepo.ItemStack, 0, 1)
	var total int64
	for _, stack := range inventory.Slots {
		if stack.ItemID != int64(itemID) {
			continue
		}
		stacks = append(stacks, stack)
		total += stack.Count
	}
	return stacks, total
}

func p157RealMailExtra(extra map[string]string, key string) string {
	if extra == nil {
		return ""
	}
	return extra[key]
}

func p157RealMailTimeText(value time.Time) string {
	if value.IsZero() {
		return "zero"
	}
	return fmt.Sprintf("%s(%d)", value.UTC().Format(time.RFC3339), value.Unix())
}
