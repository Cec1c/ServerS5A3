package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const rosterIndexTestAccount = "dnf:90001"

func seedRosterIndexCharacters(slots []int, rename bool) *fakeCharacterStore {
	store := &fakeCharacterStore{records: make(map[string]dnfrepo.CharacterRecord), nextID: 300}
	for _, slot := range slots {
		stats := map[string]int64{"delete_flag": 0}
		if rename {
			stats[characterdata.RenameEntitlementStat] = 1
			stats[characterdata.RenameMinimumLevelStat] = 1
		}
		record := dnfrepo.CharacterRecord{
			CharacterID: strconv.Itoa(200 + slot),
			AccountID:   rosterIndexTestAccount,
			Slot:        slot,
			Name:        "row" + strconv.Itoa(slot),
			Job:         "0",
			Level:       1,
			Stats:       stats,
		}
		store.records[record.CharacterID] = record
	}
	return store
}

func rememberRosterForTest(t *testing.T, service *Service, session *gameSession, records []dnfrepo.CharacterRecord) {
	t.Helper()
	service.options.currentClientVersion = "official-24.9"
	if err := service.sendCurrentCharacterSelectRoster(session, records); err != nil {
		t.Fatalf("send current selector roster: %v", err)
	}
	if conn, ok := session.conn.(*bufferConn); ok {
		conn.write.Reset()
	}
}

func deleteRosterIndexCharacter(t *testing.T, service *Service, session *gameSession, conn *bufferConn, slot int) {
	t.Helper()
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketDeleteCharacter), gameDeleteCharacterBody(uint16(slot), "row"+strconv.Itoa(slot))); err != nil {
		t.Fatalf("delete row slot %d: %v", slot, err)
	}
	reply := gameDeleteCharacterReply(t, conn)
	want := []byte{characterDeleteAckOK, 0x00, byte(slot), byte(slot >> 8)}
	if !bytes.Equal(reply.Body, want) {
		t.Fatalf("delete slot %d ack body = %x, want %x", slot, reply.Body, want)
	}
}

func assertRosterIndexSelection(t *testing.T, service *Service, session *gameSession, row int, wantID uint16, wantSlot int) {
	t.Helper()
	request := []byte{byte(row), byte(row >> 8)}
	charID, charName, selected, found, slot := service.resolveSelectedCharacter(context.Background(), session, request)
	if !found {
		t.Fatalf("roster row %d unresolved: char_id=%d name=%q slot=%d", row, charID, charName, slot)
	}
	if charID != wantID || selected.Slot != wantSlot || slot != wantSlot || selected.Name != "row"+strconv.Itoa(wantSlot) {
		t.Fatalf("roster row %d resolved id=%d name=%q record_slot=%d return_slot=%d, want id=%d name=row%d slot=%d", row, charID, selected.Name, selected.Slot, slot, wantID, wantSlot, wantSlot)
	}
}

// Deleting a middle SQL slot must not turn later visible row indexes back into
// SQL-slot lookups. The second visible row after deleting slot 1 is row2, not
// the still-existing SQL slot 2.
func TestCharacterDeleteCompactedRowsResolveBySentRoster(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := seedRosterIndexCharacters([]int{0, 1, 2, 3, 4, 5}, false)
	group := gameDeleteCharacterGroup(store)
	service, session, conn := newGameDeleteCharacterService(group, true)
	session.accountID = rosterIndexTestAccount

	initial, err := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	rememberRosterForTest(t, service, session, initial)
	deleteRosterIndexCharacter(t, service, session, conn, 1)

	deleted := store.records["201"]
	if deleted.Slot != -201 || fakeCharacterDeleteFlag(deleted) != characterDeleteDeletedFlag {
		t.Fatalf("deleted row slot=%d flag=%d, want tombstone -201 and flag 1", deleted.Slot, fakeCharacterDeleteFlag(deleted))
	}
	wantIDs := []string{"200", "202", "203", "204", "205"}
	if len(session.currentRenameRoster) != len(wantIDs) {
		t.Fatalf("in-session compacted roster count=%d, want %d", len(session.currentRenameRoster), len(wantIDs))
	}
	for index, wantID := range wantIDs {
		if got := session.currentRenameRoster[index].CharacterID; got != wantID {
			t.Fatalf("in-session roster row %d id=%s, want %s", index, got, wantID)
		}
	}

	remaining, err := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextCharacterSlot(remaining); got != 1 {
		t.Fatalf("first free SQL slot after middle delete=%d, want 1", got)
	}

	for row, wantSlot := range []int{0, 2, 3, 4, 5} {
		assertRosterIndexSelection(t, service, session, row, uint16(200+wantSlot), wantSlot)
	}

	t.Run("fresh login replays the same compacted roster", func(t *testing.T) {
		fresh, freshSession, _ := newGameDeleteCharacterService(group, true)
		freshSession.accountID = rosterIndexTestAccount
		rememberRosterForTest(t, fresh, freshSession, remaining)
		for row, wantSlot := range []int{0, 2, 3, 4, 5} {
			assertRosterIndexSelection(t, fresh, freshSession, row, uint16(200+wantSlot), wantSlot)
		}
	})
}

func TestCharacterDeleteCompactionFeedsCreateAndRenameRosterIndexes(t *testing.T) {
	t.Run("create reuses the deleted slot and replaces the sent row", func(t *testing.T) {
		t.Setenv(characterDeleteAckEnv, "")
		store := seedRosterIndexCharacters([]int{0, 1, 2, 3}, false)
		group := gameDeleteCharacterGroup(store)
		service, session, conn := newGameDeleteCharacterService(group, true)
		session.accountID = rosterIndexTestAccount
		prepareTestCharacterInitialization(service, 0)
		initial, _ := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
		rememberRosterForTest(t, service, session, initial)
		deleteRosterIndexCharacter(t, service, session, conn, 1)

		conn.write.Reset()
		if err := service.createCharacter(session, createCharacterRequest{job: 0, nameRaw: []byte("row1")}, true); err != nil {
			t.Fatalf("create after delete: %v", err)
		}
		updated, err := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
		if err != nil {
			t.Fatal(err)
		}
		if len(updated) != 4 || updated[1].Slot != 1 || updated[1].Name != "row1" || updated[2].CharacterID != "202" || updated[3].CharacterID != "203" {
			t.Fatalf("post-create roster=%+v", updated)
		}
		assertRosterIndexSelection(t, service, session, 1, uint16(numericCharacterID(updated[1])), 1)
		assertRosterIndexSelection(t, service, session, 2, 202, 2)
		assertRosterIndexSelection(t, service, session, 3, 203, 3)
	})

	t.Run("rename uses the compacted row instead of the SQL slot", func(t *testing.T) {
		t.Setenv(characterDeleteAckEnv, "")
		store := seedRosterIndexCharacters([]int{0, 1, 2, 3}, true)
		group := gameDeleteCharacterGroup(store)
		service, session, conn := newGameDeleteCharacterService(group, true)
		service.options.currentClientVersion = "official-24.9"
		session.accountID = rosterIndexTestAccount
		session.loginPacketSeen = true
		initial, _ := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
		rememberRosterForTest(t, service, session, initial)
		deleteRosterIndexCharacter(t, service, session, conn, 1)

		conn.write.Reset()
		if err := service.handleCurrentCharacterRename(session, bridge135Body(t, 1, "row2", "renamed", 1)); err != nil {
			t.Fatalf("rename compacted row 1: %v", err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(rest) != 0 || packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketChangeCharacName) || !bytes.Equal(packet.Body, []byte{1}) {
			t.Fatalf("rename ack header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
		}
		renamed := store.records["202"]
		if renamed.Name != "renamed" || renamed.Slot != 2 {
			t.Fatalf("rename changed id=202 to name=%q slot=%d, want renamed slot 2", renamed.Name, renamed.Slot)
		}
		if got := store.records["203"].Name; got != "row3" {
			t.Fatalf("rename hit the wrong SQL slot: id=203 name=%q", got)
		}
	})
}

func TestSelectRequestFormsKeepRowIndexSQLSlotAndExplicitCharIDDistinct(t *testing.T) {
	store := seedRosterIndexCharacters([]int{0, 2, 3}, false)
	group := gameDeleteCharacterGroup(store)
	service, session, _ := newGameDeleteCharacterService(group, true)
	session.accountID = rosterIndexTestAccount
	records, _ := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
	rememberRosterForTest(t, service, session, records)

	// The current two-byte selector is the compact client row. Row 1 is SQL slot 2.
	assertRosterIndexSelection(t, service, session, 1, 202, 2)

	// The protected/plain 16-byte request remains an explicit SQL-slot request.
	plain := make([]byte, 16)
	binary.LittleEndian.PutUint32(plain[:4], 2)
	charID, _, selected, found, slot := service.resolveSelectedCharacter(context.Background(), session, plain)
	if !found || charID != 202 || selected.Slot != 2 || slot != 2 {
		t.Fatalf("plain SQL-slot request resolved id=%d slot=%d found=%t, want id=202 slot=2", charID, selected.Slot, found)
	}

	// latest_type3 carries an explicit u32 character ID; it must win over its slot byte.
	latest := make([]byte, 11)
	binary.LittleEndian.PutUint32(latest[:4], 203)
	latest[4] = 0
	charID, _, selected, found, slot = service.resolveSelectedCharacter(context.Background(), session, latest)
	if !found || charID != 203 || selected.Slot != 3 || slot != 3 {
		t.Fatalf("latest explicit char-id request resolved id=%d slot=%d found=%t, want id=203 slot=3", charID, selected.Slot, found)
	}

	// A two-byte value outside the selector range is the legacy explicit char ID.
	legacyID := make([]byte, 2)
	binary.LittleEndian.PutUint16(legacyID, 203)
	charID, _, selected, found, slot = service.resolveSelectedCharacter(context.Background(), session, legacyID)
	if !found || charID != 203 || selected.Slot != 3 || slot != 3 {
		t.Fatalf("legacy explicit char-id request resolved id=%d slot=%d found=%t, want id=203 slot=3", charID, selected.Slot, found)
	}
}

func TestCharacterDeleteCompactedSelectionRejectsStaleBindings(t *testing.T) {
	bound := dnfrepo.CharacterRecord{CharacterID: "202", AccountID: rosterIndexTestAccount, Slot: 2}
	for _, name := range []string{"missing", "owner-changed", "slot-changed", "index-outside-roster", "explicit-id-missing"} {
		t.Run(name, func(t *testing.T) {
			current := bound
			characters := []dnfrepo.CharacterRecord{current}
			request := selectCharacterRequest{slot: 0, source: "legacy_slot_or_char_id"}
			switch name {
			case "missing":
				characters = nil
			case "owner-changed":
				characters[0].AccountID = "other-account"
			case "slot-changed":
				characters[0].Slot = 0
			case "index-outside-roster":
				request.slot = 2
			case "explicit-id-missing":
				request.source, request.charID, request.slot = "latest_type3", 999, 2
			}
			if _, _, found := selectCharacterRecord(characters, []dnfrepo.CharacterRecord{bound}, request); found {
				t.Fatal("stale or ambiguous selector fell back to a different identity")
			}
		})
	}
}

func TestCharacterDeleteRepeatedCompactionKeepsRemainingIdentities(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := seedRosterIndexCharacters([]int{0, 1, 2, 3}, false)
	group := gameDeleteCharacterGroup(store)
	service, session, conn := newGameDeleteCharacterService(group, true)
	session.accountID = rosterIndexTestAccount
	initial, _ := service.listCharacters(context.Background(), group, rosterIndexTestAccount)
	rememberRosterForTest(t, service, session, initial)
	deleteRosterIndexCharacter(t, service, session, conn, 1)
	conn.write.Reset()
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketDeleteCharacter), gameDeleteCharacterBody(1, "row2")); err != nil {
		t.Fatal(err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if !bytes.Equal(packet.Body, []byte{characterDeleteAckOK, 0, 1, 0}) || fakeCharacterDeleteFlag(store.records["202"]) != characterDeleteDeletedFlag {
		t.Fatal("second compacted deletion missed its bound character")
	}
	assertRosterIndexSelection(t, service, session, 0, 200, 0)
	assertRosterIndexSelection(t, service, session, 1, 203, 3)
}
