package raid

import "fmt"

// Metadata preserves the accepted create request and real leader record.
// Native 13B8910 reads a repeated key, dstr name, fields +24/+28/+30,
// u32 +34, byte +94, then the same member record used by mode3.
// 13B2820 accepts preparation state +28=0 before sending START_RAID;
// +30 is a phase argument. 13B2450 reads +94 for solo availability.
// Field34 remains zero: its semantics have not been established.
type Metadata struct {
	RaidKey   uint32
	NameBytes []byte
	Route     byte
	TailFlag  byte
	Started   bool
	Leader    MemberRecord
	Members   []MemberRecord
}

func BuildMetadataBody(info Metadata, mode uint32) ([]byte, error) {
	if info.RaidKey == 0 || info.Leader.CharKey == 0 || (mode != 0 && mode != 2) {
		return nil, fmt.Errorf("raid metadata has no owner or invalid mode")
	}
	var writer packetWriter
	writer.writeUint32(info.RaidKey)
	writer.writeUint32(mode)
	writer.writeUint32(info.RaidKey)
	writer.writeRawDstr(info.NameBytes)
	writer.writeByte(info.Route)
	if info.Started {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	writer.writeByte(0) // phase 0, independent of the create solo flag
	writer.writeUint32(0)
	writer.writeByte(info.TailFlag) // +94 solo flag, never the phase state
	if err := writeMemberRecord(&writer, info.Leader); err != nil {
		return nil, err
	}
	// Native mode0 runs 13BF120 immediately after 13B8910. Mode2
	// reads metadata alone; its roster is supplied separately through mode3.
	if mode == 0 {
		roster, err := BuildMemberRefreshMode3Body(MemberRefresh{RaidKey: info.RaidKey, Members: info.Members})
		if err != nil {
			return nil, err
		}
		writer.writeRaw(roster[8:])
	}
	return writer.bytes(), nil
}
