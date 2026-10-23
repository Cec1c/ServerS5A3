package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
)

// The 2026-10-22 21:43 live request was legacy framing, cmd=1/type1340,
// four payload bytes. Legacy framing must not be confused with class0.
func TestR195AikenOptionLegacyFrame(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class byte
		body  []byte
		valid bool
	}{
		{"select", 1, []byte{9, 0, 0, 13}, true},
		{"notification_class", 0, []byte{9, 0, 0, 13}, false},
		{"truncated", 1, []byte{9, 0, 0}, false},
		{"extra_bytes", 1, []byte{9, 0, 0, 13, 0}, false},
		{"invalid_position", 1, []byte{9, 0, 2, 13}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, group, logs := aikenR160OptionFixture(t)
			before := aikenR142Load(t, group)
			raw := make([]byte, proto.LegacyGameHeaderSize+len(tc.body))
			raw[0] = tc.class
			binary.LittleEndian.PutUint16(raw[1:3], 1340)
			binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
			binary.LittleEndian.PutUint16(raw[11:13], 1)
			copy(raw[proto.LegacyGameHeaderSize:], tc.body)
			if err := s.handleLegacyGamePacket(session, raw); err != nil {
				t.Fatal(err)
			}
			after := aikenR142Load(t, group)
			if !tc.valid {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected frame changed inventory")
				}
				if tc.class == 0 {
					if session.conn.(*bufferConn).write.Len() != 0 {
						t.Fatal("class0 generated reply")
					}
				} else {
					aikenR160ExpectRejected(t, session)
				}
				return
			}
			want := append([]byte(nil), before.Slots["0:9"].RawEntry...)
			want[0x72] = 13
			if !bytes.Equal(after.Slots["0:9"].RawEntry, want) || after.Slots["0:130"].Count != 77 {
				t.Fatalf("option/material update missing: logs=%s", logs.String())
			}
			first, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			ack, rest := splitGameServerUpperPacket(t, rest)
			if first.Header.MsgID != 14 || first.Header.Classification != 0 || ack.Header.MsgID != 1340 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
				t.Fatalf("expected committed inventory before success ACK: %+v %+v", first, ack)
			}
		})
	}
}
