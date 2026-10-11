package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Consume the boundaries of native NOTI9 2016350, including the reads at
// 2016913 (actor byte), 2016995 (count), and 2016BC1..C85 (cross-context identity).
func readOfficial249PartyRecord(body []byte, localChannel int) (ids []uint16, err error) {
	position := 0
	take := func(n int) []byte {
		if err != nil || n < 0 || n > len(body)-position {
			err = fmt.Errorf("native read overrun at %d length %d", position, n)
			return make([]byte, n)
		}
		out := body[position : position+n]
		position += n
		return out
	}
	dstr := func() {
		n := binary.LittleEndian.Uint32(take(4))
		if n > 256 {
			err = fmt.Errorf("identity string overflow")
			return
		}
		take(int(n))
	}
	if binary.LittleEndian.Uint16(take(2)) != 1 || binary.LittleEndian.Uint16(take(2)) != 9999 {
		return nil, fmt.Errorf("record header")
	}
	take(2) // actor key
	outer := take(4)
	owner := outer[1]
	if outer[3] != 0 {
		return nil, fmt.Errorf("record kind")
	}
	take(2)
	dstr()
	take(14)
	count := int(take(1)[0])
	if count > 8 {
		return nil, fmt.Errorf("party count")
	}
	for slot := 0; slot < count; slot++ {
		if take(1)[0] != byte(slot) {
			return nil, fmt.Errorf("slot boundary")
		}
		ids = append(ids, binary.LittleEndian.Uint16(take(2)))
		if int(owner) != localChannel {
			take(5)
			dstr()
			dstr()
			take(1)
		}
		take(3)
	}
	take(4)
	if err == nil && position != len(body) {
		err = fmt.Errorf("unconsumed bytes: %d", len(body)-position)
	}
	return ids, err
}

func TestOfficial249PartyRecordNativeBoundaries(t *testing.T) {
	for _, channel := range []int{11, 0, 508} {
		for _, count := range []int{0, 1, 2, 8} {
			members := make([]currentSceneOp9PartyMemberProjection, count)
			for i := range members {
				members[i] = currentSceneOp9PartyMemberProjection{State: alignedcmd.PartyMemberState{UserID: uint16(i + 1)}, Name: "实际玩家名字", Job: 1, Level: 90, Grow: 1}
			}
			body := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(1, dnfrepo.CharacterRecord{}, false, "test1", 11, channel, members, 0, true)
			ids, err := readOfficial249PartyRecord(body, channel)
			if err != nil || len(ids) != count {
				t.Fatalf("channel=%d count=%d ids=%v error=%v", channel, count, ids, err)
			}
			for i, id := range ids {
				if id != uint16(i+1) {
					t.Fatal("member identity shifted")
				}
			}
			legacy := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(1, dnfrepo.CharacterRecord{}, false, "test1", 11, channel, members, 0, false)
			if _, err := readOfficial249PartyRecord(legacy, channel); err == nil {
				t.Fatal("old record unexpectedly fits native reader")
			}
		}
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}}
	if _, err := readOfficial249PartyRecord(s.buildCurrentSceneOp9ActorDisplayBodyInContext(1, dnfrepo.CharacterRecord{}, false, "test1", 11), 11); err != nil {
		t.Fatal(err)
	}
}
