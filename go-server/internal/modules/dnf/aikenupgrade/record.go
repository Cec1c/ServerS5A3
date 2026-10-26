package aikenupgrade

import (
	"encoding/binary"
	"errors"
)

// ErrRecord rejects an op1278 notification whose body is not exactly one
// 12-byte record. The fixed v152 client handler 0x201FBA0 reads no count and
// consumes one record per notification.
var ErrRecord = errors.New("aiken record rejected")

// recordWireSize is the fixed width read by 0x201FBA0:
//
//	u32 selector
//	u8  record0
//	u8  record1
//	u8  slot0 .. slot4
//	u8  record18
const recordWireSize = 12

// Record is one op1278 record as it appears on the wire. The five slot bytes
// stay in their physical positions here; the client compacts the non-zero
// values into a separate list (record+4) and drops the zero positions, so a
// payload cannot be reconstructed with slot positions preserved.
type Record struct {
	Selector uint32
	Record0  uint8
	Record1  uint8
	Slots    [5]uint8
	Record18 uint8
}

// DecodeRecord decodes exactly one 12-byte op1278 record.
func DecodeRecord(body []byte) (Record, error) {
	if len(body) != recordWireSize {
		return Record{}, ErrRecord
	}
	var r Record
	r.Selector = binary.LittleEndian.Uint32(body[0:4])
	r.Record0 = body[4]
	r.Record1 = body[5]
	copy(r.Slots[:], body[6:11])
	r.Record18 = body[11]
	return r, nil
}

// Encode returns the 12-byte wire form. The selector keeps all 32 bits; the
// client's lookup only consumes the low 16 bits, but the remaining bits are
// still part of the delivered record.
func (r Record) Encode() []byte {
	body := make([]byte, recordWireSize)
	binary.LittleEndian.PutUint32(body[0:4], r.Selector)
	body[4] = r.Record0
	body[5] = r.Record1
	copy(body[6:11], r.Slots[:])
	body[11] = r.Record18
	return body
}

// Options mirrors the client's record+4 list: the non-zero slot values, in
// wire order. The client's consumer at 0x201FBA0 appends only values greater
// than zero and discards their physical position.
func (r Record) Options() []byte {
	out := make([]byte, 0, len(r.Slots))
	for _, value := range r.Slots {
		if value != 0 {
			out = append(out, value)
		}
	}
	return out
}

// SlotsFromOptions projects an ordered option list back onto the five physical
// wire slots, padding the remainder with zero. Zero option IDs are rejected:
// the client drops them on ingest, so they have no round-trip representation.
func SlotsFromOptions(options []byte) ([5]uint8, error) {
	var slots [5]uint8
	if len(options) > len(slots) {
		return slots, ErrRecord
	}
	for _, value := range options {
		if value == 0 {
			return slots, ErrRecord
		}
	}
	copy(slots[:], options)
	return slots, nil
}

// RecordIdentity is the client-side cache identity proven for op1278.
//
// selector is resolved through 0x296D730, a hash table keyed by a u16 and
// holding character objects; the cache key built by 0xE6A360 has record0 as
// its second u16 component. The wire carries record0 as one byte; the client
// zero-extends it before constructing that key. The client also appends the
// resolved character's name to the key string, which is local client state and
// is not part of the wire record.
//
// The numeric component recorded here is Record0. The available evidence
// proves it is a per-character record sub-key; it does not prove that it is an
// equipment slot index or a part-set index, so callers must store it as an
// opaque value.
type RecordIdentity struct {
	CharacterKey uint16
	SubKey       uint16
}

// Identity returns the wire-derived cache identity. CharacterKey is the low 16
// bits of Selector because 0x296D730 takes a u16 argument; SubKey is Record0.
// The upper selector bits are not part of this lookup key, but Encode preserves
// them because they remain part of the delivered record.
func (r Record) Identity() RecordIdentity {
	return RecordIdentity{
		CharacterKey: uint16(r.Selector),
		SubKey:       uint16(r.Record0),
	}
}
