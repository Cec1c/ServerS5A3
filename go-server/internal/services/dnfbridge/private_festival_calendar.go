package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/festival"
)

// Only presentation changes here. Keep native activity IDs and active parameters
// so hiding two calendar entries does not erase attendance or online rewards.
var privateFestivalCalendarPeriods = [...]uint32{201601, 201605, 201607, 201701, 201801, 201807}
var privateFestivalCalendarTitles = [...]string{"2016春节 · 问鼎天下", "2016五一 · 梦幻贵族", "2016夏日 · 盛晴狂欢", "2017春节 · 猎龙者", "2018春节 · 兽人守护神", "2018夏日 · 热舞一夏"}

func privateFestivalCalendarBody(body []byte, c *festival.Catalog) ([]byte, error) {
	if c == nil {
		return body, nil
	}
	if len(c.Periods) != 6 {
		return nil, fmt.Errorf("private calendar requires six configured periods")
	}
	for _, id := range privateFestivalCalendarPeriods {
		if _, ok := c.Periods[id]; !ok {
			return nil, fmt.Errorf("private calendar missing period %d", id)
		}
	}
	if len(body) < 3 {
		return nil, fmt.Errorf("short activity catalog")
	}
	at := 2
	count := int(binary.LittleEndian.Uint16(body))
	if count > 249 {
		return nil, fmt.Errorf("too many activity rows")
	}
	take := func(n int) bool {
		if n < 0 || at > len(body)-n {
			return false
		}
		at += n
		return true
	}
	skipString := func() bool {
		if !take(4) {
			return false
		}
		n := uint64(binary.LittleEndian.Uint32(body[at-4:]))
		return n <= 1<<20 && take(int(n))
	}
	var w packetWriter
	w.writeUint16(uint16(count + 6))
	for i := 0; i < count; i++ {
		start := at
		if !take(6) {
			return nil, fmt.Errorf("short activity row")
		}
		id := binary.LittleEndian.Uint16(body[start:])
		if id >= 60001 && id <= 60006 {
			return nil, fmt.Errorf("private calendar ID collision")
		}
		if !skipString() || !skipString() || !take(1) {
			return nil, fmt.Errorf("short activity text")
		}
		extended := body[at-1]
		if extended > 1 {
			return nil, fmt.Errorf("invalid activity extension")
		}
		if extended == 1 {
			if !take(2) || !skipString() || !skipString() || !skipString() || !take(8) || !skipString() || !skipString() || !take(1) {
				return nil, fmt.Errorf("short activity extension")
			}
		}
		if id == 228 || id == 2347 || id == 331 {
			// No PVF menu template exists for these three IDs in the paired archive.
			// Without the server-owned extension they remain active but have no row.
			w.writeBytes(body[start : start+6])
			w.writeRawDstr(nil)
			w.writeRawDstr(nil)
			w.writeByte(0)
		} else {
			w.writeBytes(body[start:at])
		}
	}
	if at >= len(body) {
		return nil, fmt.Errorf("missing active rows")
	}
	active := int(body[at])
	at++
	if active > 249 || len(body)-at != active*50 {
		return nil, fmt.Errorf("invalid active row size")
	}
	for i, pid := range privateFestivalCalendarPeriods {
		w.writeUint16(uint16(60001 + i))
		w.writeUint32(1)
		w.writeRawDstr(nil)
		w.writeRawDstr(nil)
		w.writeByte(1)
		w.writeByte(0)
		w.writeByte(4)
		writeClientNameDstr(&w, privateFestivalCalendarTitles[i]+" · 多买多送")
		w.writeRawDstr(nil)
		writeClientNameDstr(&w, "查看本账号购买套数及各档赠礼，达成后自动通过邮件发放。")
		w.writeUint32(0)
		w.writeUint32(0)
		w.writeAsciiDstr(fmt.Sprintf("UI/Event/PrivateFestival005/%d.xui/0/0", pid))
		w.writeRawDstr(nil)
		w.writeByte(1)
	}
	w.writeByte(byte(active + 6))
	w.writeBytes(body[at:])
	for i := range privateFestivalCalendarPeriods {
		w.writeUint16(uint16(60001 + i))
		for j := 0; j < 12; j++ {
			w.writeUint32(0)
		}
	}
	return w.bytes(), nil
}

func (s *Service) withPrivateFestivalCalendar(body []byte) ([]byte, error) {
	c, err := s.currentCeraShopCatalog()
	// Optional private extension; an upstream PVF without it keeps its catalog.
	if err != nil || c == nil || c.festivals == nil {
		return body, nil
	}
	return privateFestivalCalendarBody(body, c.festivals)
}
