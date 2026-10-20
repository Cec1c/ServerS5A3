package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func prepareMonthlyBridge(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn) {
	t.Helper()
	s, a, b, c, _ := prepareAccountFriendTest(t)
	loadCurrentAdmissionArchive(t, s)
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, a, b, c
}
func seedMonthlyBridgeDays(t *testing.T, s *Service, id string, days int) {
	t.Helper()
	g, _ := s.repositoryGroup()
	a, found, err := g.Account.Load(context.Background(), id)
	if err != nil || !found {
		t.Fatal(err)
	}
	now := s.gameplayNow()
	for d := days - 1; d >= 0; d-- {
		for i := 0; i < 2; i++ {
			if err := onlineevent.RecordMonthlyClear(&a, now.AddDate(0, 0, -d)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := g.Account.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}
func monthlyMailCount(t *testing.T, s *Service, id string) int {
	t.Helper()
	g, _ := s.repositoryGroup()
	m, _, err := g.Mailbox.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(m.Mails)
}
func TestMonthlyBridgeActualRewardClaimAndOpaqueReplay(t *testing.T) {
	s, a, b, c := prepareMonthlyBridge(t)
	seedMonthlyBridgeDays(t, s, "dnf:1", 7)
	if err := s.handleGameCommand(a, 1, 1294, bytes.Repeat([]byte{0xff}, 13)); err != nil {
		t.Fatal(err)
	}
	if n := monthlyMailCount(t, s, "1"); n != 8 {
		t.Fatalf("legal opaque claim mails=%d want8", n)
	}
	if n := monthlyMailCount(t, s, "5"); n != 0 {
		t.Fatalf("foreign mails=%d", n)
	}
	g, _ := s.repositoryGroup()
	m, _, _ := g.Mailbox.Load(context.Background(), "1")
	for _, mail := range m.Mails {
		for _, item := range mail.Attachments {
			if item.ItemID < 490010051 || item.ItemID > 490010058 || item.Count != 1 || !item.ExpireAt.After(s.gameplayNow()) {
				t.Fatalf("actual grant=%+v", item)
			}
		}
	}
	raw := c.write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, raw)
	if ack.Header.MsgID != 1294 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("ack=%+v", ack)
	}
	state, _ := splitGameServerUpperPacket(t, rest)
	if state.Header.MsgID != 1206 || len(state.Body) != 37 {
		t.Fatalf("state=%+v", state)
	}
	c.write.Reset()
	frame, frameErr := proto.BuildChannelPacket(1294, make([]byte, 13), 1, 1)
	if frameErr != nil {
		t.Fatal(frameErr)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "1") != 8 {
		t.Fatal("replay duplicated reward")
	}
	if err := s.handleGameCommand(b, 1, 1294, make([]byte, 13)); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "5") != 0 {
		t.Fatal("foreign account gained reward")
	}
}
func TestMonthlyBridgeRequestScopeAndShapeRejectWithoutAssets(t *testing.T) {
	for _, name := range []string{"short", "long", "wrongclass", "untrusted", "foreigncharacter", "unbound", "disabled", "resourcesmissing"} {
		t.Run(name, func(t *testing.T) {
			s, a, _, _ := prepareMonthlyBridge(t)
			seedMonthlyBridgeDays(t, s, "dnf:1", 7)
			body := make([]byte, 13)
			class := byte(1)
			switch name {
			case "short":
				body = body[:12]
			case "long":
				body = append(body, 0)
			case "wrongclass":
				class = 0
			case "untrusted":
				a.accountFromToken = false
			case "foreigncharacter":
				a.accountID = "dnf:2"
			case "unbound":
				s.unbindGameSession(a)
			case "disabled":
				s.activityControls = nil
			case "resourcesmissing":
				s.initialEquipmentArchive = nil
				s.initialEquipmentLoadErr = errors.New("missing real PVF resources")
			}
			_, _ = s.handleCurrentMonthlyAttendanceClaim(a, 1294, class, body)
			if monthlyMailCount(t, s, "1") != 0 || monthlyMailCount(t, s, "5") != 0 {
				t.Fatal("rejected request mutated mailbox")
			}
		})
	}
}

// Stage two mailbox entries then invalidate the session identity. The same UOW
// must roll back both staged mails and the account claim bits.
func TestMonthlyBridgeGenerationChangeRollsBackPendingWrites(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	seedMonthlyBridgeDays(t, s, "dnf:1", 7)
	g, _ := s.repositoryGroup()
	catalog, err := s.currentMonthlyCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Account.Load(context.Background(), "dnf:1")
	calls := 0
	_, err = onlineevent.SettleMonthlyAttendance(context.Background(), g, catalog, onlineevent.MonthlyCommand{AccountID: "dnf:1", CharacterID: "1", Now: s.gameplayNow(), ClaimBonus: true, Validate: func() bool { return calls < 2 }, Deliver: func(m repo.MailboxRepository, r onlineevent.AttendancePVFItemReward, key string) (string, error) {
		calls++
		return repo.AppendSystemMail(context.Background(), m, repo.SystemMailDelivery{RecipientCharacterID: "1", Title: "test", CreatedAt: time.Now(), Source: key, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}})
	if err == nil {
		t.Fatal("stale owner accepted")
	}
	after, _, _ := g.Account.Load(context.Background(), "dnf:1")
	if before.Metadata[onlineevent.MonthlyMetadataKey] != after.Metadata[onlineevent.MonthlyMetadataKey] || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("stale generation wrote assets")
	}
	_ = a
}
func TestMonthlyBridgeAcceptedSettlementReceiptCountsOnceAndCap(t *testing.T) {
	s, r, a, _, _ := prepareCompletedSettlementRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	loadCurrentAdmissionArchive(t, s)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	id := ch.AccountID
	a.accountID = id
	a.loginPacketSeen = true
	a.accountFromToken = true
	s.mu.Lock()
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	s.gameSessions[99] = a
	a.characterGeneration = 1
	s.mu.Unlock()
	if err := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: id, Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	ch.Level = 1
	ch.Stats["exp"] = 0
	if err := g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if err := g.Skill.Save(context.Background(), repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	r.Character = ch
	r.Dungeon.Metadata.RecommendedLevels = []int64{1, 90}
	r.ordinaryFinalRoomClearAccepted = true
	resources := currentDungeonSettlementTestResources(t)
	original := g
	g.CharacterSettlement = monthlyFailingSettlementUOW{g.CharacterSettlement}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	r.clearMapCompletionKey = "monthly-clear-failed"
	if _, err := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1); err == nil {
		t.Fatal("post-account-write settlement failure accepted")
	}
	failedAccount, _, _ := g.Account.Load(context.Background(), id)
	failedCharacter, _, _ := g.Character.Load(context.Background(), "99")
	if failedAccount.Metadata[onlineevent.MonthlyMetadataKey] != "" || failedCharacter.Stats["exp"] != 0 {
		t.Fatal("failed settlement committed count or experience")
	}
	g = original
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	for i, key := range []string{"monthly-clear-1", "monthly-clear-1", "monthly-clear-2", "monthly-clear-3"} {
		r.clearMapCompletionKey = key
		r.Character, _, _ = g.Character.Load(context.Background(), "99")
		result, err := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
		if err != nil {
			t.Fatal(err)
		}
		if (i == 1) != result.Replayed {
			t.Fatalf("receipt replay=%t i%d", result.Replayed, i)
		}
		cat := &onlineevent.MonthlyCatalog{}
		state, err := onlineevent.SettleMonthlyAttendance(context.Background(), g, cat, onlineevent.MonthlyCommand{AccountID: id, CharacterID: "99", Now: s.gameplayNow(), Deliver: func(m repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
			return repo.AppendSystemMail(context.Background(), m, repo.SystemMailDelivery{RecipientCharacterID: "99", Title: "count-test", Attachments: []repo.MailAttachment{{ItemID: 490010051, Count: 1}}, CreatedAt: s.gameplayNow(), Source: key})
		}})
		if err != nil {
			t.Fatal(err)
		}
		expected := uint32(1)
		if i >= 2 {
			expected = 2
		}
		if state.Clears != expected {
			t.Fatalf("clears=%d expected%d i%d", state.Clears, expected, i)
		}
	}
	account, _, _ := g.Account.Load(context.Background(), id)
	if account.Metadata["keep"] != "yes" {
		t.Fatal("foreign metadata lost")
	}
}

type monthlyFailingAccount struct{ repo.AccountRepository }

func (r monthlyFailingAccount) Save(ctx context.Context, a repo.AccountRecord) error {
	if err := r.AccountRepository.Save(ctx, a); err != nil {
		return err
	}
	return errors.New("monthly injected post-account-write")
}

type monthlyFailingClaimUOW struct {
	repo.MailboxAssetUnitOfWork
	repo.AccountMailboxUnitOfWork
}

func (u monthlyFailingClaimUOW) WithinAccountMailbox(ctx context.Context, a, c string, f func(repo.AccountRepository, repo.MailboxRepository) error) error {
	return u.AccountMailboxUnitOfWork.WithinAccountMailbox(ctx, a, c, func(ar repo.AccountRepository, mr repo.MailboxRepository) error {
		return f(monthlyFailingAccount{ar}, mr)
	})
}
func TestMonthlyBridgeClaimFailureAfterWriteRollsBackAndRetry(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	seedMonthlyBridgeDays(t, s, "dnf:1", 7)
	g, _ := s.repositoryGroup()
	before, _, _ := g.Account.Load(context.Background(), "dnf:1")
	original := g
	g.MailboxAssets = monthlyFailingClaimUOW{g.MailboxAssets, g.MailboxAssets.(repo.AccountMailboxUnitOfWork)}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	frame, frameErr := proto.BuildChannelPacket(1294, make([]byte, 13), 1, 1)
	if frameErr != nil {
		t.Fatal(frameErr)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Account.Load(context.Background(), "dnf:1")
	if after.Metadata[onlineevent.MonthlyMetadataKey] != before.Metadata[onlineevent.MonthlyMetadataKey] || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("failed claim committed mail/account")
	}
	s.repositoryProvider = func() (repo.Group, bool) { return original, true }
	frame, frameErr = proto.BuildChannelPacket(1294, make([]byte, 13), 1, 1)
	if frameErr != nil {
		t.Fatal(frameErr)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "1") != 8 {
		t.Fatal("retry failed")
	}
}
func TestMonthlyBridgeSameAccountDifferentCharacterCannotDuplicate(t *testing.T) {
	s, a, b, _ := prepareMonthlyBridge(t)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "5")
	ch.AccountID = "dnf:1"
	if err := g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	b.accountID = "dnf:1"
	seedMonthlyBridgeDays(t, s, "dnf:1", 7)
	frame, frameErr := proto.BuildChannelPacket(1294, make([]byte, 13), 1, 1)
	if frameErr != nil {
		t.Fatal(frameErr)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(b, 1, 1294, bytes.Repeat([]byte{0x7f}, 13)); err != nil {
		t.Fatal(err)
	}
	if monthlyMailCount(t, s, "1") != 8 || monthlyMailCount(t, s, "5") != 0 {
		t.Fatal("account-shared claim duplicated")
	}
}

type monthlyFailingSettlementUOW struct {
	repo.CharacterSettlementUnitOfWork
}

func (u monthlyFailingSettlementUOW) WithinCharacterSettlement(ctx context.Context, c string, f func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, c, func(g repo.Group) error { g.Account = monthlyFailingAccount{g.Account}; return f(g) })
}

func TestMonthlyBridgeEvent2370NativeCatalogAndConfig(t *testing.T) {
	body, err := buildCurrentActivityEventInfoBodyWithMonthly([]uint32{490006574}, 10800, false, false, false, false, false, 0, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 68 || binary.LittleEndian.Uint16(body) != 1 || binary.LittleEndian.Uint16(body[2:]) != 2370 || binary.LittleEndian.Uint32(body[4:]) != 1 || !bytes.Equal(body[8:17], make([]byte, 9)) || body[17] != 1 || binary.LittleEndian.Uint16(body[18:]) != 2370 || binary.LittleEndian.Uint32(body[20:]) != onlineevent.MonthlyRequiredClears || !bytes.Equal(body[24:], make([]byte, 44)) {
		t.Fatalf("native monthly catalog=%x", body)
	}
	disabled, err := buildCurrentActivityEventInfoBodyWithMonthly([]uint32{490006574}, 10800, false, false, false, false, false, 0, false, false)
	if err != nil || !bytes.Equal(disabled, []byte{0, 0, 0}) {
		t.Fatalf("disabled catalog=%x err=%v", disabled, err)
	}
}
