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
