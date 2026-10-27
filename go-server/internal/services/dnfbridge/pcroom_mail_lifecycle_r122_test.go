package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type r122PCRoomLifecycleFixture struct {
	pvfSHA256 string
	service   *Service
	repos     dnfrepo.Group
	catalog   *currentSpendTimeRuntimeCatalog
	queue     *fakeCurrentDungeonDeathTimerQueue
	now       time.Time
}

func newR122PCRoomLifecycleFixture(t *testing.T) r122PCRoomLifecycleFixture {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires DNFBRIDGE_REAL_PVF_SMOKE")
	}
	pvfFile, err := os.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real PVF for hash: %v", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, pvfFile); err != nil {
		_ = pvfFile.Close()
		t.Fatalf("hash real PVF: %v", err)
	}
	if err := pvfFile.Close(); err != nil {
		t.Fatalf("close real PVF after hash: %v", err)
	}
	pvfSHA256 := hex.EncodeToString(hash.Sum(nil))
	if expected := os.Getenv("DNF90_PCROOM_EXPECT_PVF_SHA256"); expected != "" && pvfSHA256 != expected {
		t.Fatalf("real PVF sha256=%s, want %s", pvfSHA256, expected)
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real PVF: %v", err)
	}
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	accountID := "r122-pcroom-lifecycle"
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID, State: "active", Metadata: map[string]string{}}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	for _, id := range []string{"1", "2"} {
		if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: id, AccountID: accountID, Slot: numericCharacterIDString(id), Name: "r122-pcroom-" + id, Job: "0", Level: 90,
		}); err != nil {
			t.Fatalf("save character %s: %v", id, err)
		}
		if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatalf("save inventory %s: %v", id, err)
		}
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 19, 12, 0, 0, 0, dnfonlineevent.ChinaCalendar)
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			joustEventDisabled:   true,
		},
		gameplayTimers:          queue,
		initialEquipmentArchive: archive,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	catalog, available, err := service.currentSpendTimeRuntimeCatalogIfAvailable(ctx)
	if err != nil || !available || catalog == nil || catalog.nativePCRoom == nil {
		t.Fatalf("load real PC-room catalog available=%t err=%v catalog=%+v", available, err, catalog)
	}
	owner, err := dnfonlineevent.NewOwner(repositories)
	if err != nil {
		t.Fatalf("create online-event owner: %v", err)
	}
	if _, err := owner.Observe(ctx, dnfonlineevent.ObserveCommand{
		AccountID:    accountID,
		CharacterID:  "1",
		Definition:   catalog.definition,
		IntervalFrom: queue.now.Add(-time.Hour),
		IntervalTo:   queue.now,
	}); err != nil {
		t.Fatalf("observe one online hour: %v", err)
	}
	return r122PCRoomLifecycleFixture{pvfSHA256: pvfSHA256, service: service, repos: repositories, catalog: catalog, queue: queue, now: queue.now}
}

func (f r122PCRoomLifecycleFixture) session(t *testing.T, characterID uint16) (*gameSession, *bufferConn) {
	t.Helper()
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              fmt.Sprintf("r122-pcroom-%d", characterID),
		channel:             channelcatalog.Channel{ID: 19},
		accountID:           "r122-pcroom-lifecycle",
		selectedCharacterID: characterID,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	f.service.bindGameSessionCharacter(session, characterID)
	return session, connection
}

func TestPCRoom632MailClaimLifecycleAndSameDayAccountMask(t *testing.T) {
	t.Setenv(dnfmail.MailboxClaimedLetterEnv, string(dnfmail.ClaimedLetterFinish))
	f := newR122PCRoomLifecycleFixture(t)
	ctx := context.Background()
	session, connection := f.session(t, 1)
	if err := f.service.sendCurrentSpendTimeInitialStateOnce(session, "r122_lifecycle_initial"); err != nil {
		t.Fatalf("initialize native PC-room panel: %v", err)
	}
	connection.write.Reset()

	request632 := []byte{1, 255}
	if err := f.service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, request632); err != nil {
		t.Fatalf("formal 632 request: %v", err)
	}
	state561, alarm := r122PCRoom632Packets(t, connection.write.Bytes())
	if state561.Header.MsgID != 561 || state561.Header.Classification != 0 || len(state561.Body) != 17 || state561.Body[8] != 1 {
		t.Fatalf("561 state=%+v body=%x", state561.Header, state561.Body)
	}
	if !bytesEqual(alarm.Body, []byte{1, 0}) {
		t.Fatalf("mailbox alarm body=%x, want 0100", alarm.Body)
	}

	box, found, err := f.repos.Mailbox.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load character mailbox found=%t err=%v", found, err)
	}
	mailIDs, publicCounts := r122PCRoomMails(box)
	if len(mailIDs) != 1 {
		t.Fatalf("632 produced mailIDs=%v, want one PC-room mail", mailIDs)
	}
	r122WritePCRoomCorpus(t, f, request632, state561, mailIDs, publicCounts)

	resolver, err := f.service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil || resolver == nil {
		t.Fatalf("formal op95 resolver err=%v", err)
	}
	mailID := mailIDs[0]
	claim := r122FormalMailClaim(t, f.repos, resolver, 1, mailID)
	if claim.Operation != "mailbox_extract_ack" || len(claim.UpperResponses) == 0 || len(claim.UpperResponses[0].Body) == 0 || claim.UpperResponses[0].Body[0] != 1 {
		t.Fatalf("formal op95 result=%+v", claim)
	}
	r122AssertMailConsumed(t, f.repos, "1", mailID)
	firstMail := box.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if len(firstMail.Attachments) != 1 {
		t.Fatalf("first PC-room mail attachments=%+v", firstMail.Attachments)
	}
	r122AssertInventoryItemCount(t, f.repos, "1", uint32(firstMail.Attachments[0].ItemID), firstMail.Attachments[0].Count)

	duplicate := r122FormalMailClaim(t, f.repos, resolver, 1, mailID)
	if duplicate.Operation != "mailbox_extract_rejected" || !r122MailFailureCode(duplicate, 4) {
		t.Fatalf("duplicate claim result=%+v", duplicate)
	}
	r122AssertInventoryItemCount(t, f.repos, "1", uint32(firstMail.Attachments[0].ItemID), firstMail.Attachments[0].Count)

	connection.write.Reset()
	if err := f.service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{2, 255}); err != nil {
		t.Fatalf("formal second 632 request: %v", err)
	}
	secondState, _ := r122PCRoom632Packets(t, connection.write.Bytes())
	if secondState.Body[8] != 3 {
		t.Fatalf("second daily mask=%d, want 3", secondState.Body[8])
	}
	box, _, err = f.repos.Mailbox.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	secondIDs, _ := r122PCRoomMails(box)
	if len(secondIDs) != 1 {
		t.Fatalf("second 632 new mail IDs=%v, want one", secondIDs)
	}
	secondMailID := secondIDs[0]
	secondMail := box.Mails[strconv.FormatUint(uint64(secondMailID), 10)]
	if len(secondMail.Attachments) != 1 {
		t.Fatalf("second PC-room attachments=%+v", secondMail.Attachments)
	}
	resolution, err := resolver(uint32(secondMail.Attachments[0].ItemID))
	if err != nil || resolution.TargetListType != dnfrepo.MainInventoryListType {
		t.Fatalf("second reward resolution=%+v err=%v", resolution, err)
	}
	beforeFull, _, err := f.repos.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	beforeFull = dnfrepo.CloneInventory(beforeFull)
	full := dnfrepo.CloneInventory(beforeFull)
	for slot := int16(3); slot <= 359; slot++ {
		full.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 990000000, Count: 1}
	}
	if err := f.repos.Inventory.Save(ctx, full); err != nil {
		t.Fatalf("fill inventory: %v", err)
	}
	failed := r122FormalMailClaim(t, f.repos, resolver, 1, secondMailID)
	if failed.Operation != "mailbox_extract_rejected" || !r122MailFailureCode(failed, 6) {
		t.Fatalf("full-bag claim result=%+v", failed)
	}
	box, _, err = f.repos.Mailbox.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	retained := box.Mails[strconv.FormatUint(uint64(secondMailID), 10)]
	if retained.Claimed || retained.Deleted || len(retained.Attachments) != 1 || !reflect.DeepEqual(retained.Attachments[0], secondMail.Attachments[0]) {
		t.Fatalf("full-bag claim changed retained mail=%+v", retained)
	}
	r122AssertInventoryItemCount(t, f.repos, "1", uint32(secondMail.Attachments[0].ItemID), 0)
	if err := f.repos.Inventory.Save(ctx, beforeFull); err != nil {
		t.Fatalf("restore inventory space: %v", err)
	}
	if ok := r122FormalMailClaim(t, f.repos, resolver, 1, secondMailID); ok.Operation != "mailbox_extract_ack" {
		t.Fatalf("claim after making space=%+v", ok)
	}
	r122AssertInventoryItemCount(t, f.repos, "1", uint32(secondMail.Attachments[0].ItemID), secondMail.Attachments[0].Count)
	r122AssertMailConsumed(t, f.repos, "1", secondMailID)

	// The mask and delivery receipt are account-scoped: another character of
	// the same account cannot turn the already claimed daily bit into mail.
	secondCharacter, secondConnection := f.session(t, 2)
	if err := f.service.sendCurrentSpendTimeInitialStateOnce(secondCharacter, "r122_same_day_second_character"); err != nil {
		t.Fatalf("initialize second character: %v", err)
	}
	secondConnection.write.Reset()
	if err := f.service.handleGameCommand(secondCharacter, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{1, 255}); err != nil {
		t.Fatalf("second-character 632: %v", err)
	}
	char2State, char2Alarm := r122PCRoom632Packets(t, secondConnection.write.Bytes())
	if char2State.Body[8] != 3 || char2Alarm.Header.MsgID != 0 {
		t.Fatalf("second-character state/alarm mask=%d alarm=%+v", char2State.Body[8], char2Alarm)
	}
	char2Box, found, err := f.repos.Mailbox.Load(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if found && len(r122AllMailIDs(char2Box)) != 0 {
		t.Fatalf("same-day second character received mail: %+v", char2Box.Mails)
	}
}

func TestPCRoomAccountMaskResetsOnceOnNextServiceDay(t *testing.T) {
	t.Setenv(dnfmail.MailboxClaimedLetterEnv, string(dnfmail.ClaimedLetterFinish))
	f := newR122PCRoomLifecycleFixture(t)
	first, firstConn := f.session(t, 1)
	if err := f.service.sendCurrentSpendTimeInitialStateOnce(first, "r122_cross_day_first"); err != nil {
		t.Fatal(err)
	}
	firstConn.write.Reset()
	if err := f.service.handleGameCommand(first, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	firstState, _ := r122PCRoom632Packets(t, firstConn.write.Bytes())
	if firstState.Body[8] != 1 {
		t.Fatalf("first-day daily mask=%d", firstState.Body[8])
	}

	f.queue.now = f.queue.now.Add(24 * time.Hour)
	second, secondConn := f.session(t, 2)
	if err := f.service.sendCurrentSpendTimeInitialStateOnce(second, "r122_cross_day_reset"); err != nil {
		t.Fatal(err)
	}
	secondConn.write.Reset()
	if err := f.service.handleGameCommand(second, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	withoutOnline, noOnlineAlarm := r122PCRoom632Packets(t, secondConn.write.Bytes())
	if withoutOnline.Body[8] != 0 || noOnlineAlarm.Header.MsgID != 0 {
		t.Fatalf("new day without online time mask=%d alarm=%+v", withoutOnline.Body[8], noOnlineAlarm)
	}
	box, _, err := f.repos.Mailbox.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r122PCRoomMailsLoose(box)); got != 1 {
		t.Fatalf("new-day request without time changed first-day mails=%d", got)
	}

	owner, err := dnfonlineevent.NewOwner(f.repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Observe(context.Background(), dnfonlineevent.ObserveCommand{
		AccountID:    "r122-pcroom-lifecycle",
		CharacterID:  "2",
		Definition:   f.catalog.definition,
		IntervalFrom: f.queue.now.Add(-time.Hour),
		IntervalTo:   f.queue.now,
	}); err != nil {
		t.Fatal(err)
	}
	secondConn.write.Reset()
	if err := f.service.handleGameCommand(second, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	qualified, qualifiedAlarm := r122PCRoom632Packets(t, secondConn.write.Bytes())
	if qualified.Body[8] != 1 || !bytesEqual(qualifiedAlarm.Body, []byte{1, 0}) {
		t.Fatalf("next-day qualified state=%x alarm=%x", qualified.Body, qualifiedAlarm.Body)
	}
	beforeRepeat, _, err := f.repos.Mailbox.Load(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeRepeat.Mails) != 1 {
		t.Fatalf("next-day qualified claim mails=%d, want one", len(beforeRepeat.Mails))
	}
	secondConn.write.Reset()
	if err := f.service.handleGameCommand(second, byte(dnfenum.GameCmdCommand), currentPCRoomTimePointRequestID, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	repeat, repeatAlarm := r122PCRoom632Packets(t, secondConn.write.Bytes())
	afterRepeat, _, err := f.repos.Mailbox.Load(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Body[8] != 1 || repeatAlarm.Header.MsgID != 0 || len(afterRepeat.Mails) != len(beforeRepeat.Mails) {
		t.Fatalf("next-day replay mask=%d alarm=%+v mails before/after=%d/%d", repeat.Body[8], repeatAlarm, len(beforeRepeat.Mails), len(afterRepeat.Mails))
	}
}

func r122PCRoom632Packets(t *testing.T, stream []byte) (dnfproto.ChannelPacket, dnfproto.ChannelPacket) {
	t.Helper()
	var state, alarm dnfproto.ChannelPacket
	for _, packet := range splitAllCurrentUpperPackets(t, stream) {
		switch packet.Header.MsgID {
		case 561:
			if state.Header.MsgID != 0 {
				t.Fatal("duplicate 561 response")
			}
			state = packet
		case uint16(dnfmail.MailboxAlarmNotificationMessageID):
			if alarm.Header.MsgID != 0 {
				t.Fatal("duplicate mailbox alarm")
			}
			alarm = packet
		}
	}
	if state.Header.MsgID == 0 {
		t.Fatalf("632 emitted no 561 state: %x", stream)
	}
	if len(state.Body) != 17 {
		t.Fatalf("561 body length=%d, want 17", len(state.Body))
	}
	return state, alarm
}

func r122FormalMailClaim(
	t *testing.T,
	repositories dnfrepo.Group,
	resolver alignedcmd.MailboxItemResolver,
	characterID uint16,
	mailID uint32,
) alignedcmd.Result {
	t.Helper()
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, 1)
	binary.LittleEndian.PutUint32(body[4:], mailID)
	result, err := dnfmail.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Command:             byte(dnfenum.GameCmdCommand),
		CommandKnown:        true,
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		Body:                body,
		AccountID:           "r122-pcroom-lifecycle",
		SelectedCharacterID: characterID,
		Repositories:        repositories,
		MailboxItemResolver: resolver,
	})
	if err != nil {
		t.Fatalf("formal mailbox op95: %v", err)
	}
	if !result.Handled || len(result.UpperResponses) == 0 {
		t.Fatalf("formal mailbox op95 was not handled: %+v", result)
	}
	return result
}

func r122MailFailureCode(result alignedcmd.Result, want byte) bool {
	if len(result.UpperResponses) == 0 || len(result.UpperResponses[0].Body) < 2 {
		return false
	}
	return result.UpperResponses[0].Body[0] == 0 && result.UpperResponses[0].Body[1] == want
}

func r122AssertMailConsumed(t *testing.T, repositories dnfrepo.Group, characterID string, mailID uint32) {
	t.Helper()
	box, found, err := repositories.Mailbox.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load mailbox after claim found=%t err=%v", found, err)
	}
	mail, found := box.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if !found || !mail.Claimed || !mail.Deleted || mail.Gold != 0 || len(mail.Attachments) != 0 {
		t.Fatalf("claimed mail was not consumed: found=%t mail=%+v", found, mail)
	}
}

func r122AssertInventoryItemCount(t *testing.T, repositories dnfrepo.Group, characterID string, itemID uint32, want int64) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	var got int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(itemID) {
			got += stack.Count
		}
	}
	if got != want {
		t.Fatalf("inventory item %d count=%d, want %d", itemID, got, want)
	}
}

func r122PCRoomMails(box dnfrepo.MailboxRecord) ([]uint32, map[uint32]int64) {
	ids, counts := r122PCRoomMailsAndCounts(box, true)
	return ids, counts
}

func r122PCRoomMailsLoose(box dnfrepo.MailboxRecord) []uint32 {
	ids, _ := r122PCRoomMailsAndCounts(box, false)
	return ids
}

func r122PCRoomMailsAndCounts(box dnfrepo.MailboxRecord, claimableOnly bool) ([]uint32, map[uint32]int64) {
	ids := make([]uint32, 0)
	counts := make(map[uint32]int64)
	for key, mail := range box.Mails {
		if mail.Metadata["source"] != "pcroom_timepoint_228" || (claimableOnly && (mail.Claimed || mail.Deleted || len(mail.Attachments) == 0)) {
			continue
		}
		id, err := strconv.ParseUint(key, 10, 32)
		if err != nil || id == 0 {
			continue
		}
		ids = append(ids, uint32(id))
		for _, attachment := range mail.Attachments {
			if attachment.ItemID > 0 && attachment.Count > 0 {
				counts[uint32(attachment.ItemID)] += attachment.Count
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, counts
}

func r122AllMailIDs(box dnfrepo.MailboxRecord) []string {
	ids := make([]string, 0, len(box.Mails))
	for id := range box.Mails {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type r122PCRoomCorpus struct {
	Schema                   string            `json:"schema"`
	PVFSHA256                string            `json:"pvf_sha256"`
	ObservedAt               string            `json:"observed_at"`
	CharacterID              uint16            `json:"character_id"`
	Request632               r122CorpusBytes   `json:"request_632"`
	State561                 r122Corpus561     `json:"state_561"`
	MailIDs                  r122CorpusMailIDs `json:"mail_ids"`
	AttachmentPublicIDCounts map[string]int64  `json:"attachment_public_id_counts"`
}

type r122CorpusBytes struct {
	BodyLen int    `json:"body_len"`
	Hex     string `json:"hex"`
}

type r122Corpus561 struct {
	MsgID          uint16 `json:"msg_id"`
	Classification byte   `json:"classification"`
	BodyLen        int    `json:"body_len"`
	Hex            string `json:"hex"`
	Seconds        uint32 `json:"seconds"`
	Total          uint32 `json:"total"`
	DailyMask      byte   `json:"daily_mask"`
	PeriodMask     uint32 `json:"period_mask"`
}

type r122CorpusMailIDs struct {
	Count   int      `json:"count"`
	Decimal []uint32 `json:"decimal"`
}

func r122WritePCRoomCorpus(
	t *testing.T,
	f r122PCRoomLifecycleFixture,
	request []byte,
	state dnfproto.ChannelPacket,
	mailIDs []uint32,
	publicCounts map[uint32]int64,
) {
	t.Helper()
	path := os.Getenv("DNF90_PCROOM_CORPUS_PATH")
	if path == "" {
		return
	}
	public := make(map[string]int64, len(publicCounts))
	for itemID, count := range publicCounts {
		public[strconv.FormatUint(uint64(itemID), 10)] = count
	}
	record := r122PCRoomCorpus{
		Schema:      "r122-pcroom-632-561-mail-lifecycle-v1",
		PVFSHA256:   f.pvfSHA256,
		ObservedAt:  f.now.UTC().Format(time.RFC3339Nano),
		CharacterID: 1,
		Request632: r122CorpusBytes{
			BodyLen: len(request),
			Hex:     hex.EncodeToString(request),
		},
		State561: r122Corpus561{
			MsgID:          state.Header.MsgID,
			Classification: state.Header.Classification,
			BodyLen:        len(state.Body),
			Hex:            hex.EncodeToString(state.Body),
			Seconds:        binary.LittleEndian.Uint32(state.Body[0:4]),
			Total:          binary.LittleEndian.Uint32(state.Body[4:8]),
			DailyMask:      state.Body[8],
			PeriodMask:     binary.LittleEndian.Uint32(state.Body[13:17]),
		},
		MailIDs:                  r122CorpusMailIDs{Count: len(mailIDs), Decimal: append([]uint32(nil), mailIDs...)},
		AttachmentPublicIDCounts: public,
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("encode PC-room corpus: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create PC-room corpus directory: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write PC-room corpus: %v", err)
	}
	t.Logf("PC-room production corpus written: %s", path)
}

func numericCharacterIDString(id string) int {
	n, _ := strconv.Atoi(id)
	return n
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
