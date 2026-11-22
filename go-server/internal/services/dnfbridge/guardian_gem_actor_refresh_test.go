package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestP161ActorMedalSnapshotSurvivesFailureAndStaysWithOwner(t *testing.T) {
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	row := currentItemListEntry{}
	row.patchCore(32, 100380058, 1)
	binary.LittleEndian.PutUint16(row.data[101:103], 3)
	reader := &csharpLegacyUserInfoReader{session: session, characterID: "19"}
	reader.resetCurrentGuardianGemRow()
	reader.noteCurrentGuardianGemRow(row)
	// An unrelated peer projection cannot replace or clear the local row.
	peer := &csharpLegacyUserInfoReader{session: session, characterID: "20"}
	peer.resetCurrentGuardianGemRow()
	peer.noteCurrentGuardianGemRow(row)
	// The saved row is a value copy of the generation's equipment snapshot.
	binary.LittleEndian.PutUint16(row.data[101:103], 13)
	session.selectedCharacterID = 20
	if err := service.sendSelectedActorGuardianGemRowRefresh(session, "changed_owner"); err != nil || conn.write.Len() != 0 {
		t.Fatalf("foreign owner write: err=%v bytes=%d", err, conn.write.Len())
	}
	session.selectedCharacterID = 19
	session.conn = &titleEnchantFailConn{&bufferConn{}}
	if err := service.sendSelectedActorGuardianGemRowRefresh(session, "failed_write"); err == nil || session.pendingGuardianGemRow == nil {
		t.Fatalf("failed write lost pending row: %v", err)
	}
	session.conn = conn
	if err := service.sendSelectedActorGuardianGemRowRefresh(session, "retry"); err != nil {
		t.Fatal(err)
	}
	rows := p161WornMedalRows(t, conn.write.Bytes(), 100380058)
	if len(rows) != 1 || binary.LittleEndian.Uint16(rows[0].body[104:106]) != 3 {
		t.Fatal("retry did not retain the original generation snapshot")
	}
	size := conn.write.Len()
	if err := service.sendSelectedActorGuardianGemRowRefresh(session, "duplicate"); err != nil || conn.write.Len() != size || session.pendingGuardianGemRow != nil {
		t.Fatal("successful replay was not consumed exactly once")
	}
}

func TestP161NewEquipmentProjectionClearsOldMedalSnapshot(t *testing.T) {
	session := &gameSession{selectedCharacterID: 19, conn: &bufferConn{}}
	reader := &csharpLegacyUserInfoReader{session: session, characterID: "19"}
	row := currentItemListEntry{}
	row.patchCore(32, 100380058, 1)
	binary.LittleEndian.PutUint16(row.data[101:103], 3)
	reader.noteCurrentGuardianGemRow(row)
	if session.pendingGuardianGemRow == nil {
		t.Fatal("occupied medal not captured")
	}
	reader.resetCurrentGuardianGemRow()
	row.patchCore(13, 100380058, 1)
	reader.noteCurrentGuardianGemRow(row)
	row.patchCore(32, 100380058, 1)
	binary.LittleEndian.PutUint16(row.data[101:103], 0)
	reader.noteCurrentGuardianGemRow(row)
	if session.pendingGuardianGemRow != nil {
		t.Fatal("new empty/other-slot projection retained old gem state")
	}
}

func TestP161DeferredDungeonMedalReplayRetainsSnapshotOnFailure(t *testing.T) {
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16}}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	row := currentItemListEntry{}
	row.patchCore(32, 100380058, 1)
	binary.LittleEndian.PutUint16(row.data[currentGuardianGemRawSocketOffset:currentGuardianGemRawSocketOffset+currentGuardianGemRawSocketWidth], 3)
	session.pendingGuardianGemCharacterID = session.selectedCharacterID
	session.pendingGuardianGemRow = &row

	service.deferSelectedActorGuardianGemRowRefreshUntilFinishLoading(session, "dungeon_mode1")
	if !session.pendingGuardianGemAwaitFinishLoading {
		t.Fatal("dungeon medal replay was not deferred")
	}
	session.conn = &titleEnchantFailConn{&bufferConn{}}
	if err := service.sendDeferredSelectedActorGuardianGemRowRefresh(session, "finish_failed"); err == nil {
		t.Fatal("injected medal replay write unexpectedly succeeded")
	}
	if !session.pendingGuardianGemAwaitFinishLoading || session.pendingGuardianGemRow == nil {
		t.Fatal("failed deferred replay lost the exact mode1 medal snapshot")
	}

	session.conn = connection
	if err := service.sendDeferredSelectedActorGuardianGemRowRefresh(session, "finish_retry"); err != nil {
		t.Fatal(err)
	}
	if session.pendingGuardianGemAwaitFinishLoading || session.pendingGuardianGemRow != nil {
		t.Fatal("successful deferred replay was not consumed exactly once")
	}
	rows := p161WornMedalRows(t, connection.write.Bytes(), 100380058)
	if len(rows) != 1 {
		t.Fatalf("deferred replay rows=%d, want=1", len(rows))
	}
	p161RequireSocketWords(t, rows[0], [4]uint16{3, 0, 0, 0})
}
