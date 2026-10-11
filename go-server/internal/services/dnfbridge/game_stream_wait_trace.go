package dnfbridge

import (
	"encoding/binary"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// gameStreamWaitTrace observes parser progress without changing bytes, read
// deadlines, dispatch, or session state. One report is allowed per connection.
// It inspects only a possible channel header's public classification/opcode/size;
// checksums, sequences, bodies and authentication data are never logged.
type gameStreamWaitTrace struct {
	started  time.Time
	reported bool
}

type gameStreamWaitMetadata struct {
	pendingBytes     int
	headerShape      string
	hasChannelHeader bool
	classification   byte
	opcode           uint16
	declaredLength   uint32
}

func (trace *gameStreamWaitTrace) observe(now time.Time, remaining []byte, progressed bool, maxPacketBytes int) (gameStreamWaitMetadata, bool) {
	if trace.reported {
		return gameStreamWaitMetadata{}, false
	}
	if len(remaining) == 0 {
		trace.started = time.Time{}
		return gameStreamWaitMetadata{}, false
	}
	if trace.started.IsZero() || progressed {
		trace.started = now
		return gameStreamWaitMetadata{}, false
	}
	if now.Sub(trace.started) < 2*time.Second || len(remaining) < dnfproto.ChannelHeaderSize {
		return gameStreamWaitMetadata{}, false
	}
	metadata := gameStreamWaitMetadata{pendingBytes: len(remaining), headerShape: "unrecognized-prefix"}
	classification := remaining[0]
	opcode := binary.LittleEndian.Uint16(remaining[1:3])
	declaredLength := binary.LittleEndian.Uint32(remaining[3:7])
	if (classification == 0 || classification == dnfproto.DefaultChannelClassification) && opcode < 2048 && declaredLength >= dnfproto.ChannelHeaderSize && uint64(declaredLength) <= uint64(maxPacketBytes) {
		metadata.headerShape = "possible-channel-header"
		metadata.hasChannelHeader = true
		metadata.classification = classification
		metadata.opcode = opcode
		metadata.declaredLength = declaredLength
	}
	trace.reported = true
	return metadata, true
}
