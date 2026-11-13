package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestPrivateFeatureProtocolV2PreviewHotkeyClearAndIsolation(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	conn := &bufferConn{}
	p := &gameSession{conn: conn, selectedCharacterID: 77}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }, gameSessions: map[uint16]*gameSession{77: p}}
	preset := privateFeaturePreset{Version: 1, Items: []privateFeatureItem{{Slot: 14, ID: 123, Fingerprint: "0123456789012345678901234567890123456789012345678901234567890123"}}}
	raw, _ := json.Marshal(preset)
	if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: "native-client-preset-v1/77", Values: map[string]string{"2": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Settings.Save(ctx, repo.SettingsRecord{Scope: "native-client-preset-v1/78", Values: map[string]string{"2": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	call := func(version uint16, action byte, value uint32) []byte {
		t.Helper()
		conn.write.Reset()
		q := make([]byte, 24)
		copy(q, "CFQ1")
		binary.LittleEndian.PutUint16(q[4:], version)
		q[6] = action
		q[7] = 2
		binary.LittleEndian.PutUint32(q[8:], 91)
		binary.LittleEndian.PutUint32(q[12:], 37)
		binary.LittleEndian.PutUint32(q[16:], value)
		if err := s.handlePrivateClientFeatures(p, 1, q); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() == 0 {
			return nil
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != privateClientFeaturesOpcode || len(rest) != 0 {
			t.Fatal("wrong response")
		}
		return packet.Body
	}
	b := call(2, 4, 0)
	if len(b) != 512 || binary.LittleEndian.Uint32(b[8:]) != 91 || binary.LittleEndian.Uint32(b[12:]) != 37 || binary.LittleEndian.Uint16(b[40:]) != 1 || binary.LittleEndian.Uint32(b[256+13*4:]) != 123 {
		t.Fatalf("preview failed: len=%d body=%x", len(b), b)
	}
	b = call(2, 5, 0x20041)
	if len(b) != 512 || binary.LittleEndian.Uint32(b[392:]) != 0x20041 {
		t.Fatal("Ctrl+A not persisted")
	}
	b = call(2, 0, 0)
	if binary.LittleEndian.Uint32(b[392:]) != 0x20041 {
		t.Fatal("hotkey readback")
	}
	if b = call(1, 4, 0); b != nil {
		t.Fatal("v1 accepted v2 action")
	}
	if b = call(2, 5, 0x80041); b != nil {
		t.Fatal("invalid modifier accepted")
	}
	if b = call(2, 5, 0x20101); b != nil {
		t.Fatal("invalid key accepted")
	}
	if b = call(1, 0, 0); len(b) != 256 {
		t.Fatal("v1 compatibility")
	}
	b = call(2, 3, 0)
	if len(b) != 512 || binary.LittleEndian.Uint16(b[40:]) != 0 || binary.LittleEndian.Uint32(b[308:]) != 0 || binary.LittleEndian.Uint32(b[392:]) != 0 {
		t.Fatal("clear kept stale preview/key")
	}
	other, ok, err := g.Settings.Load(ctx, "native-client-preset-v1/78")
	if err != nil || !ok || other.Values["2"] != string(raw) {
		t.Fatal("clear crossed character boundary")
	}
	delete(s.gameSessions, 77)
	if b = call(2, 5, 0x20042); b != nil {
		t.Fatal("stale session mutated settings")
	}
}
