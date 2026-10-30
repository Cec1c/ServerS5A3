package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/festival"
	"sort"
	"unicode/utf16"
)

const privateFestivalOpcode uint16 = 0x7f02

func encodePrivateFestival(st festival.State, c *festival.Catalog, character uint16, generation uint64, nonce uint32, status uint16) []byte {
	// FES1, fixed six period slots, each with a 16-code-unit label and up to
	// 24 configured milestones. No client-selected account or reward IDs.
	b := make([]byte, 32+6*256)
	copy(b, "FES1")
	put16 := func(i int, v uint16) { binary.LittleEndian.PutUint16(b[i:], v) }
	put32 := func(i int, v uint32) { binary.LittleEndian.PutUint32(b[i:], v) }
	put16(4, 1)
	put16(6, uint16(len(b)))
	put16(8, status)
	put16(10, character)
	binary.LittleEndian.PutUint64(b[16:], generation)
	put32(24, nonce)
	if status != 0 || c == nil {
		return b
	}
	ids := []uint32{}
	for id := range c.Periods {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > 6 {
		put16(8, 5)
		return b
	}
	put16(12, uint16(len(ids)))
	for i, id := range ids {
		p := c.Periods[id]
		o := 32 + i*256
		put32(o, id)
		put32(o+4, st.Counts[id])
		put32(o+8, p.RepeatFrom)
		put32(o+12, p.Repeat.Item)
		put32(o+16, p.Repeat.Count)
		pending := uint32(0)
		for _, a := range st.Pending {
			if a.Period == id {
				pending++
			}
		}
		put32(o+20, pending)
		label := utf16.Encode([]rune(p.Name))
		if len(label) > 15 {
			label = label[:15]
		}
		for j, ch := range label {
			put16(o+32+j*2, ch)
		}
		tiers := []uint32{}
		for at := range p.Tiers {
			tiers = append(tiers, at)
		}
		sort.Slice(tiers, func(i, j int) bool { return tiers[i] < tiers[j] })
		if len(tiers) > 24 {
			put16(8, 5)
			return b
		}
		put32(o+24, uint32(len(tiers)))
		for j, at := range tiers {
			r := p.Tiers[at]
			x := o + 64 + j*8
			put16(x, uint16(at))
			b[x+2] = 0
			if st.Counts[id] >= at {
				b[x+2] = 2
			}
			for _, a := range st.Pending {
				if a.Period == id && a.At == at {
					b[x+2] = 1
				}
			}
			if r.Count > 255 {
				put16(8, 5)
				return b
			}
			b[x+3] = byte(r.Count)
			put32(x+4, r.Item)
		}
	}
	return b
}
func (s *Service) handlePrivateFestival(session *gameSession, classification byte, body []byte) error {
	if session == nil || classification != 1 || len(body) != 16 || string(body[:4]) != "FEQ1" || binary.LittleEndian.Uint16(body[4:]) != 1 || binary.LittleEndian.Uint16(body[6:]) != 0 || binary.LittleEndian.Uint32(body[12:]) != 0 {
		return nil
	}
	nonce := binary.LittleEndian.Uint32(body[8:])
	if nonce == 0 {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	st, c, e := s.settlePrivateFestival(ctx, session)
	status := uint16(0)
	if e != nil {
		status = 5
		s.logGameEvent(session, "private-festival-query-rejected", "error", e)
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	return s.sendGameUpperRawClass(session, privateFestivalOpcode, encodePrivateFestival(st, c, identity.character, identity.generation, nonce, status), 0)
}
