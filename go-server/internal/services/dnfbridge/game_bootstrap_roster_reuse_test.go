package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type rosterReuseCharacterRepository struct {
	dnfrepo.CharacterRepository
	accounts []string
	limits   []int
	err      error
}

type rosterReuseFailingSettings struct {
	dnfrepo.SettingsRepository
	err error
}

func (r *rosterReuseFailingSettings) Load(context.Context, string) (dnfrepo.SettingsRecord, bool, error) {
	return dnfrepo.SettingsRecord{}, false, r.err
}

func (r *rosterReuseCharacterRepository) ListByAccount(ctx context.Context, account string, limit int) ([]dnfrepo.CharacterRecord, error) {
	r.accounts = append(r.accounts, account)
	r.limits = append(r.limits, limit)
	if r.err != nil {
		return nil, r.err
	}
	return r.CharacterRepository.ListByAccount(ctx, account, limit)
}

func rosterReuseTestService(repositories dnfrepo.Group) *Service {
	return &Service{
		options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
		initialEquipmentLoadErr: errors.New("roster reuse test: no PVF fixture"),
	}
}

func saveRosterReuseDarkKnight(t *testing.T, repositories dnfrepo.Group, accountID, characterID string, slot int) []darkcombo.Page {
	t.Helper()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Name:        "reuse_" + characterID,
		Job:         "9",
		Level:       90,
		Slot:        slot,
	}); err != nil {
		t.Fatalf("save character %s: %v", characterID, err)
	}
	pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, characterID, pages, time.Unix(1, 0)); err != nil {
		t.Fatalf("save combo pages %s: %v", characterID, err)
	}
	return pages
}

func TestUpperGetUserInfoRosterListsCharactersOnceAndReusesRecordsForCombo(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	pages := saveRosterReuseDarkKnight(t, repositories, "account-1", "77", 3)
	counting := &rosterReuseCharacterRepository{CharacterRepository: repositories.Character}
	repositories.Character = counting

	connection := &bufferConn{}
	session := &gameSession{conn: connection, accountID: "account-1"}
	service := rosterReuseTestService(repositories)
	if err := service.sendUpperGetUserInfoRosterBootstrapEnvelope(session, "ordinary_upper"); err != nil {
		t.Fatal(err)
	}
	if len(counting.accounts) != 1 {
		t.Fatalf("ListByAccount calls=%d want 1", len(counting.accounts))
	}
	if counting.accounts[0] != "account-1" || counting.limits[0] != defaultCharacterSlots {
		t.Fatalf("ListByAccount args account=%q limit=%d", counting.accounts[0], counting.limits[0])
	}

	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packet count=%d want roster+op809", len(packets))
	}
	if packets[0].Header.Classification != 0 || packets[0].Header.MsgID != uint16(dnfenum.UpperMsgCharacterRoster) {
		t.Fatalf("roster header=%+v", packets[0].Header)
	}
	wantPreload, err := darkcombo.EncodePreloadNotificationBody([]darkcombo.PreloadEntry{{Slot: 3, Pages: pages}})
	if err != nil {
		t.Fatal(err)
	}
	if packets[1].Header.Classification != 0 || packets[1].Header.MsgID != currentDarkKnightComboPreloadMsgID ||
		!bytes.Equal(packets[1].Body, wantPreload) {
		t.Fatalf("op809 header=%+v body=%x want=%x", packets[1].Header, packets[1].Body, wantPreload)
	}

	// A second response must query again: the rows belong to the current
	// roster response and are never cached on the service.
	secondConnection := &bufferConn{}
	secondSession := &gameSession{conn: secondConnection, accountID: "account-1"}
	if err := service.sendUpperGetUserInfoRosterBootstrapEnvelope(secondSession, "ordinary_upper"); err != nil {
		t.Fatal(err)
	}
	if len(counting.accounts) != 2 {
		t.Fatalf("ListByAccount calls after two responses=%d want 2", len(counting.accounts))
	}
	secondPackets := splitAllCurrentGameServerUpperPackets(t, secondConnection.write.Bytes())
	if len(secondPackets) != 2 || secondPackets[1].Header.MsgID != currentDarkKnightComboPreloadMsgID ||
		!bytes.Equal(secondPackets[1].Body, wantPreload) {
		t.Fatalf("second response packets=%+v want roster+op809", secondPackets)
	}
}

func TestUpperGetUserInfoRosterListErrorSendsEmptyRosterAndSkipsCombo(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	saveRosterReuseDarkKnight(t, repositories, "account-1", "77", 3)
	listErr := errors.New("roster list unavailable")
	counting := &rosterReuseCharacterRepository{CharacterRepository: repositories.Character, err: listErr}
	repositories.Character = counting

	connection := &bufferConn{}
	session := &gameSession{conn: connection, accountID: "account-1"}
	err := rosterReuseTestService(repositories).sendUpperGetUserInfoRosterBootstrapEnvelope(session, "ordinary_upper")
	if !errors.Is(err, listErr) {
		t.Fatalf("error=%v want %v", err, listErr)
	}
	if len(counting.accounts) != 1 {
		t.Fatalf("ListByAccount calls=%d want 1", len(counting.accounts))
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 {
		t.Fatalf("packet count=%d want empty roster only", len(packets))
	}
	if packets[0].Header.Classification != 0 || packets[0].Header.MsgID != uint16(dnfenum.UpperMsgCharacterRoster) {
		t.Fatalf("roster header=%+v", packets[0].Header)
	}
	if !bytes.Equal(packets[0].Body, buildCSharpEmptyRosterBody()) {
		t.Fatalf("roster body=%x want empty roster %x", packets[0].Body, buildCSharpEmptyRosterBody())
	}
	if session.currentRenameRosterPrepared != nil || session.currentRenameRoster != nil {
		t.Fatalf("failed roster retained records prepared=%d remembered=%d",
			len(session.currentRenameRosterPrepared), len(session.currentRenameRoster))
	}
	if !session.emptyRosterSlotProbePending {
		t.Fatal("empty roster probe was not remembered before the list error returned")
	}
}

func TestUpperGetUserInfoRosterComboLoadErrorReturnsAfterRosterSend(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	saveRosterReuseDarkKnight(t, repositories, "account-1", "77", 3)
	comboErr := errors.New("combo settings unavailable")
	repositories.Settings = &rosterReuseFailingSettings{SettingsRepository: repositories.Settings, err: comboErr}
	counting := &rosterReuseCharacterRepository{CharacterRepository: repositories.Character}
	repositories.Character = counting

	connection := &bufferConn{}
	session := &gameSession{conn: connection, accountID: "account-1"}
	err := rosterReuseTestService(repositories).sendUpperGetUserInfoRosterBootstrapEnvelope(session, "ordinary_upper")
	if !errors.Is(err, comboErr) {
		t.Fatalf("error=%v want %v", err, comboErr)
	}
	if len(counting.accounts) != 1 {
		t.Fatalf("ListByAccount calls=%d want 1", len(counting.accounts))
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != uint16(dnfenum.UpperMsgCharacterRoster) {
		t.Fatalf("packets=%+v want roster before combo error", packets)
	}
}

func TestUpperGetUserInfoRosterComboPreloadsAllRowsBeyondSelectableWidth(t *testing.T) {
	repositories := testRepositoryGroup()
	characterCount := defaultCharacterSlots + 2
	entries := make([]darkcombo.PreloadEntry, 0, characterCount)
	for slot := 0; slot < characterCount; slot++ {
		characterID := strconv.Itoa(700 + slot)
		pages := saveRosterReuseDarkKnight(t, repositories, "account-1", characterID, slot)
		entries = append(entries, darkcombo.PreloadEntry{Slot: byte(slot), Pages: pages})
	}
	counting := &rosterReuseCharacterRepository{CharacterRepository: repositories.Character}
	repositories.Character = counting

	connection := &bufferConn{}
	session := &gameSession{conn: connection, accountID: "account-1"}
	if err := rosterReuseTestService(repositories).sendUpperGetUserInfoRosterBootstrapEnvelope(session, "ordinary_upper"); err != nil {
		t.Fatal(err)
	}
	if len(counting.accounts) != 1 {
		t.Fatalf("ListByAccount calls=%d want 1", len(counting.accounts))
	}
	if len(session.currentRenameRosterPrepared) != defaultCharacterSlots {
		t.Fatalf("prepared roster=%d want %d", len(session.currentRenameRosterPrepared), defaultCharacterSlots)
	}

	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packet count=%d want roster+op809", len(packets))
	}
	roster := packets[0].Body
	if len(roster) < 15 || roster[1] != byte(defaultCharacterSlots) ||
		binary.LittleEndian.Uint16(roster[13:15]) != uint16(defaultCharacterSlots) {
		t.Fatalf("roster width len=%d count=%d entries=%d want %d",
			len(roster), roster[1], binary.LittleEndian.Uint16(roster[13:15]), defaultCharacterSlots)
	}
	wantPreload, err := darkcombo.EncodePreloadNotificationBody(entries)
	if err != nil {
		t.Fatal(err)
	}
	if packets[1].Header.MsgID != currentDarkKnightComboPreloadMsgID || !bytes.Equal(packets[1].Body, wantPreload) {
		t.Fatalf("op809 body=%x want all %d rows=%x", packets[1].Body, characterCount, wantPreload)
	}
}

func TestBuildUpperGetUserInfoRosterBodyWrapperKeepsLegacyShape(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	saveRosterReuseDarkKnight(t, repositories, "account-1", "77", 3)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, accountID: "account-1"}

	body := rosterReuseTestService(repositories).buildUpperGetUserInfoRosterBody(session)
	if len(body) < 15 {
		t.Fatalf("wrapper roster body len=%d want >=15", len(body))
	}
	if body[0] != 2 || body[1] != 1 || binary.LittleEndian.Uint16(body[13:15]) != 1 {
		t.Fatalf("wrapper roster body first=%x count=%x entries=%d", body[0], body[1], binary.LittleEndian.Uint16(body[13:15]))
	}
	if connection.write.Len() != 0 {
		t.Fatalf("body-only wrapper wrote %d bytes", connection.write.Len())
	}
	if len(session.currentRenameRosterPrepared) != 1 {
		t.Fatalf("wrapper prepared rows=%d want 1", len(session.currentRenameRosterPrepared))
	}
}
