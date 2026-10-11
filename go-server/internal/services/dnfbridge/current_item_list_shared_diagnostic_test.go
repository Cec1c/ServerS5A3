package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type sharedDiagnosticFaultConn struct {
	bufferConn
	short bool
}

func (c *sharedDiagnosticFaultConn) Write(p []byte) (int, error) {
	if c.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestCurrentSharedInventoryDiagnosticOnlyLogsCompleteSocketWrite(t *testing.T) {
	for _, kind := range []string{"complete", "suppressed", "short", "failed"} {
		t.Run(kind, func(t *testing.T) {
			logger, path := testCharacterOptionPacketLogger(t)
			t.Cleanup(func() { _ = logger.Close() })
			service := &Service{packetLog: logger}
			session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, connID: "shared-wire-test"}
			t.Setenv(upperSendFilterEnv, "")
			if kind == "suppressed" {
				t.Setenv(upperSendFilterEnv, "0:13")
			}
			resetUpperSendFilterCache()
			defer resetUpperSendFilterCache()
			if kind == "short" || kind == "failed" {
				session.conn = &sharedDiagnosticFaultConn{short: kind == "short"}
			}
			var shared, ordinary currentItemListEntry
			shared.patchCore(354, 3033, 5)
			ordinary.patchCore(111, 36, 1)
			body := buildCurrentItemListBody(0, []currentItemListEntry{ordinary, shared}, dnfrepo.CharacterContainerState{MainSlotCount: 144})
			err := service.sendCurrentSceneItemList(session, 13, body)
			if (err != nil) != (kind == "failed") {
				t.Fatalf("send error %v for %s", err, kind)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			logged := readCharacterOptionPacketLog(t, path)
			has := strings.Contains(logged, "kind=game-current-shared-inventory-wire-sent")
			if has != (kind == "complete") {
				t.Fatalf("wire diagnostic present=%t for %s: %s", has, kind, logged)
			}
			if kind == "complete" && (!strings.Contains(logged, "slot=354 item_id=3033 count=5") || strings.Contains(logged, "item_id=36")) {
				t.Fatalf("wrong shared-row projection: %s", logged)
			}
		})
	}
}

func TestCurrentSharedInventoryDiagnosticReadsExactWireWithoutChangingBody(t *testing.T) {
	entries := []currentItemListEntry{{}, {}, {}}
	entries[0].patchCore(111, 36, 1)
	entries[1].patchCore(354, 3033, 5)
	entries[2].patchCore(365, 10158124, 71)
	body := buildCurrentItemListBody(0, entries, dnfrepo.CharacterContainerState{MainSlotCount: 144})
	before := append([]byte(nil), body...)
	rows, valid := currentSharedInventoryWireDiagnostics(body)
	if !valid || len(rows) != 2 || rows[0].Slot != 354 || rows[0].ItemID != 3033 || rows[0].Count != 5 || rows[1].Slot != 365 || rows[1].Count != 71 {
		t.Fatalf("diagnostic=%+v valid=%t", rows, valid)
	}
	sha := sha256.Sum256(body[5+currentItemListEntryWireSize : 5+2*currentItemListEntryWireSize])
	if rows[0].RowSHA256 != hex.EncodeToString(sha[:]) || !bytes.Equal(body, before) {
		t.Fatal("wrong wire digest or input mutated")
	}
	// Re-selection comparison must show a change in the exact outgoing count.
	binary.LittleEndian.PutUint32(body[5+currentItemListEntryWireSize+6:], 0)
	changed, valid := currentSharedInventoryWireDiagnostics(body)
	if !valid || changed[0].Count != 0 || changed[0].RowSHA256 == rows[0].RowSHA256 {
		t.Fatal("diagnostic concealed changed wire count")
	}
}

func TestCurrentSharedInventoryDiagnosticRejectsMalformedOrOtherContainer(t *testing.T) {
	var row currentItemListEntry
	row.patchCore(354, 3033, 5)
	body := buildCurrentItemListBody(0, []currentItemListEntry{row}, dnfrepo.CharacterContainerState{})
	other := append([]byte(nil), body...)
	other[0] = 1
	duplicate := buildCurrentItemListBody(0, []currentItemListEntry{row, row}, dnfrepo.CharacterContainerState{})
	for _, bad := range [][]byte{nil, body[:4], body[:len(body)-1], append(append([]byte(nil), body...), 0), other, duplicate} {
		if rows, valid := currentSharedInventoryWireDiagnostics(bad); valid || rows != nil {
			t.Fatal("malformed diagnostic reported valid rows")
		}
	}
}
