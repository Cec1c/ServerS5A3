package dnfbridge

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// V164/V166 class-0 NOTI741 -> VA 013A4990. This is unrelated to class-1
// CMD741 RequestGuildCreatePermit. The lifecycle sender projects persisted
// clear-count units and an absolute cycle end; unknown merchant fields stay 0.
const currentGuildDungeonSnapshotNotification uint16 = 741

// Field1 and Field3 deliberately remain wire names: their complete lifecycle
// semantics have not been established. EndUnix is an absolute Unix timestamp,
// consumed by VA 0371B339, not a remaining-duration field.
type currentGuildDungeonWireSnapshot struct {
	Flag        byte
	Field1      uint32
	EndUnix     uint32
	Field3      uint32
	ClearCounts map[uint32]uint32
}

func buildCurrentGuildDungeonSnapshotBody(snapshot currentGuildDungeonWireSnapshot) ([]byte, error) {
	if snapshot.Flag > 1 {
		return nil, errors.New("guild dungeon snapshot flag is not boolean")
	}
	ids := make([]uint32, 0, len(snapshot.ClearCounts))
	for id, count := range snapshot.ClearCounts {
		if id == 0 || id > math.MaxInt32 || count > math.MaxInt32 {
			return nil, errors.New("guild dungeon snapshot contains an invalid native ID/count")
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	body := make([]byte, 17+8*len(ids))
	body[0] = snapshot.Flag
	binary.LittleEndian.PutUint32(body[1:], snapshot.Field1)
	binary.LittleEndian.PutUint32(body[5:], snapshot.EndUnix)
	binary.LittleEndian.PutUint32(body[9:], snapshot.Field3)
	binary.LittleEndian.PutUint32(body[13:], uint32(len(ids)))
	for i, id := range ids {
		at := 17 + i*8
		binary.LittleEndian.PutUint32(body[at:], id)
		binary.LittleEndian.PutUint32(body[at+4:], snapshot.ClearCounts[id])
	}
	return body, nil
}
