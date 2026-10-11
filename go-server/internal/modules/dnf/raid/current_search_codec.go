package raid

import (
	"encoding/binary"
	"fmt"
)

const CurrentRaidSearchPageSize = 33

// CurrentSearchRequest is 01A56530's op831 writer. The raw eight bytes
// supplied by 01DF3290 are offset32, sort8, reverse8, CheckMyChannel8 and
// exceptProceeding8. Mode1 writes only its selector and requests NOTI590;
// mode0 expects NOTI753, whose completion clears the native pending flag.
type CurrentSearchRequest struct {
	Mode              byte
	Offset            uint32
	SortColumn        byte
	Reverse           bool
	OnlyMyChannel     bool
	ExcludeProceeding bool
	NameBytes         []byte
}

func DecodeCurrentSearchRequest(body []byte) (CurrentSearchRequest, error) {
	if len(body) == 1 && body[0] == 1 {
		return CurrentSearchRequest{Mode: 1}, nil
	}
	if len(body) < 13 || body[0] != 0 {
		return CurrentSearchRequest{}, fmt.Errorf("invalid current raid search selector or size")
	}
	n := uint64(binary.LittleEndian.Uint32(body[9:13]))
	if n+13 != uint64(len(body)) || body[5] > 6 || body[6] > 1 || body[7] > 1 || body[8] > 1 {
		return CurrentSearchRequest{}, fmt.Errorf("invalid current raid search fields")
	}
	return CurrentSearchRequest{
		Offset: binary.LittleEndian.Uint32(body[1:5]), SortColumn: body[5],
		Reverse: body[6] != 0, OnlyMyChannel: body[7] != 0,
		ExcludeProceeding: body[8] != 0, NameBytes: append([]byte(nil), body[13:]...),
	}, nil
}

// BuildCurrentSearchPageBody targets the actual class0/753 callback
// 0202D940: total32, offset32, count32, then current metadata objects and
// their member counts. Offset is this server's echoed page start, bounded
// by total as the native 01A54C30 setter does. It is not a roster or ACK831.
func BuildCurrentSearchPageBody(total, offset uint32, entries []CurrentMetadata) ([]byte, error) {
	if offset > total || len(entries) > CurrentRaidSearchPageSize || uint64(offset)+uint64(len(entries)) > uint64(total) {
		return nil, fmt.Errorf("invalid current raid search page bounds")
	}
	directory, err := BuildCurrentDirectoryBody(entries)
	if err != nil {
		return nil, err
	}
	var writer packetWriter
	writer.writeUint32(total)
	writer.writeUint32(offset)
	writer.writeRaw(directory)
	return writer.bytes(), nil
}
