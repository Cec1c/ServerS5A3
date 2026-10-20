package raid

import (
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"math"
)

type AdmissionRule struct {
	ItemID             uint32
	Count              int64
	WaitingTown        byte
	WaitingArea        byte
	MinimumMembers     int
	SoloMinimumMembers int
	MaximumMembers     int
}

// LoadAdmissionRule reads the actual raid configuration. Unsupported cost
// shapes fail rather than silently admitting without all configured items.
func LoadAdmissionRule(source dnfpvf.Source, path string) (AdmissionRule, error) {
	text, err := source.ReadText(path)
	if err != nil {
		return AdmissionRule{}, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return AdmissionRule{}, err
	}
	cost, room := doc.Ints("ENTRY COST"), doc.Ints("WAITING ROOM")
	minimum, _ := doc.Int("RAID START MINIMUM MEMBER")
	solo, _ := doc.Int("SINGLE RAID START MINIUM MEMBER")
	maximum, _ := doc.Int("RAID MEMBER MAX")
	if len(cost) != 2 || cost[0] <= 0 || cost[0] > math.MaxUint32 || cost[1] <= 0 || cost[1] > math.MaxUint16 || len(room) != 2 || room[0] <= 0 || room[0] > 255 || room[1] < 0 || room[1] > 255 || minimum <= 0 || solo <= 0 {
		return AdmissionRule{}, fmt.Errorf("raid admission configuration unsupported: %s", path)
	}
	return AdmissionRule{ItemID: uint32(cost[0]), Count: cost[1], WaitingTown: byte(room[0]), WaitingArea: byte(room[1]), MinimumMembers: int(minimum), SoloMinimumMembers: int(solo), MaximumMembers: int(maximum)}, nil
}

type EntryCostStatus struct {
	CharacterID uint16
	Ready       bool
	OwnedCount  uint16
}

// Current v94 EXE class0/598 -> 02005260 reads u32 count, then u16 key,
// u8 ready, u16 owned. The older reference's u32/u32 fields are incompatible.
func BuildEntryCostStatusBody(statuses []EntryCostStatus) []byte {
	w := packetWriter{}
	w.writeUint32(uint32(len(statuses)))
	for _, status := range statuses {
		w.writeUint16(status.CharacterID)
		ready := byte(0)
		if status.Ready {
			ready = 1
		}
		w.writeByte(ready)
		w.writeUint16(status.OwnedCount)
	}
	return w.bytes()
}
