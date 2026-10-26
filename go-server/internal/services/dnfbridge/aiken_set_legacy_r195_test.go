package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
)

// The live cmd=1/type1341 request used legacy framing. Exercise the real
// dispatcher, including invalid variable-length requests, before asset writes.
func TestR195AikenSetLegacyFrame(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class byte
		body  []byte
		valid bool
	}{
		{"create", 1, []byte{1, 34, 3, 9, 0, 10, 0, 11, 0}, true},
		{"notification_class", 0, []byte{1, 34, 3, 9, 0, 10, 0, 11, 0}, false},
		{"truncated", 1, []byte{1, 34, 3, 9, 0, 10, 0, 11}, false},
		{"extra_bytes", 1, []byte{1, 34, 3, 9, 0, 10, 0, 11, 0, 0}, false},
		{"six_duplicate_targets", 1, []byte{1, 34, 6, 9, 0, 10, 0, 11, 0, 9, 0, 10, 0, 11, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g, repos := aikenR193Fixture(t)
			before := aikenR142Load(t, repos)
			raw := make([]byte, proto.LegacyGameHeaderSize+len(tc.body))
			raw[0] = tc.class
			binary.LittleEndian.PutUint16(raw[1:3], 1341)
			binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
			binary.LittleEndian.PutUint16(raw[11:13], 1)
			copy(raw[proto.LegacyGameHeaderSize:], tc.body)
			if err := s.handleLegacyGamePacket(g, raw); err != nil {
				t.Fatal(err)
			}
			after := aikenR142Load(t, repos)
			wire := g.conn.(*bufferConn).write.Bytes()
			if !tc.valid {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected frame changed assets")
				}
				if tc.class == 0 {
					if len(wire) != 0 {
						t.Fatal("class0 generated reply")
					}
					return
				}
				packets := splitAllGameServerUpperPackets(t, wire)
				if len(packets) != 1 || packets[0].Header.MsgID != 1341 || packets[0].Header.Classification != 1 || !bytes.Equal(packets[0].Body, []byte{0, 1}) {
					t.Fatalf("unexpected rejection: %+v", packets)
				}
				return
			}
			if after.Slots["0:158"].Count != before.Slots["0:158"].Count-118 {
				t.Fatal("set material charge missing")
			}
			for _, key := range []string{"0:9", "0:10", "0:11"} {
				record, found, err := currentAikenReadSet(after.Slots[key])
				if err != nil || !found || record.Slots[0] != 34 {
					t.Fatalf("set missing at %s: %+v %v", key, record, err)
				}
			}
			packets := splitAllGameServerUpperPackets(t, wire)
			if len(packets) != 3 || packets[0].Header.MsgID != 14 || packets[1].Header.MsgID != 1278 || packets[2].Header.MsgID != 1341 || packets[2].Header.Classification != 1 || !bytes.Equal(packets[2].Body, []byte{1}) {
				t.Fatalf("expected committed inventory and set before ACK: %+v", packets)
			}
		})
	}
}
