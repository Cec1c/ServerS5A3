package raid

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestMetadataNativeHeaderAndRealLeader(t *testing.T) {
	info := Metadata{RaidKey: 0x170006, NameBytes: []byte{'A', 0}, Route: 1, TailFlag: 2, Leader: MemberRecord{CharKey: 6, Name: "test78", GroupIndex: 1, Field66BoolSource: 1}}
	info.Members = []MemberRecord{info.Leader, {CharKey: 4, Name: "t3", GroupIndex: 1, SlotOrder: 1}}
	for _, mode := range []uint32{0, 2} {
		body, err := BuildMetadataBody(info, mode)
		if err != nil {
			t.Fatal(err)
		}
		prefix, _ := hex.DecodeString("0600170000000000060017000200000041000100000000000002")
		prefix[4] = byte(mode)
		if !bytes.HasPrefix(body, prefix) {
			t.Fatalf("native header mismatch: %x", body)
		}
		var leader packetWriter
		if err := writeMemberRecord(&leader, info.Leader); err != nil {
			t.Fatal(err)
		}
		expected := leader.bytes()
		if mode == 0 {
			expected = append(expected, byte(len(info.Members)))
			for _, member := range info.Members {
				var row packetWriter
				if err := writeMemberRecord(&row, member); err != nil {
					t.Fatal(err)
				}
				expected = append(expected, row.bytes()...)
			}
		}
		if !bytes.Equal(body[len(prefix):], expected) {
			t.Fatal("metadata leader does not match roster leader")
		}
	}
	if _, err := BuildMetadataBody(Metadata{}, 0); err == nil {
		t.Fatal("missing real owner accepted")
	}
}

func TestSoloCreateFlagDoesNotBecomeNativeRaidStage(t *testing.T) {
	for _, solo := range []byte{0, 1} {
		for _, started := range []bool{false, true} {
			body, err := BuildMetadataBody(Metadata{RaidKey: 7, NameBytes: []byte("raid"), TailFlag: solo, Started: started, Leader: MemberRecord{CharKey: 1}}, 2)
			if err != nil {
				t.Fatal(err)
			}
			pos := 16 + int(binary.LittleEndian.Uint32(body[12:16]))
			state := byte(0)
			if started {
				state = 1
			}
			if body[pos+1] != state || body[pos+2] != 0 || body[pos+7] != solo {
				t.Fatalf("native phase/solo fields conflated: %x", body[pos:pos+8])
			}
			if !started && body[pos+1] != 0 && body[pos+1] != 5 && body[pos+1] != 6 {
				t.Fatal("native 13B2820 will reject START_RAID before sending")
			}
		}
	}
}
