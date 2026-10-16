package dnfbridge

import (
	"encoding/binary"
	"sort"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

// Matched v105 class1/262 (VA 0x1fe4f80): u32 count then 57-byte rows.
// This builds the success payload after status; no opcode is registered here.
func buildCurrentGuildCargoHistoryBody(history []dnfguild.CargoHistoryEntry) ([]byte, error) {
	// Validate before truncation; invalid hidden records must not be masked.
	for _, entry := range history {
		if err := dnfguild.ValidateCargoHistoryEntry(entry); err != nil {
			return nil, err
		}
	}
	rows := append([]dnfguild.CargoHistoryEntry(nil), history...)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	if len(rows) > dnfguild.CargoHistoryLimit {
		rows = rows[:dnfguild.CargoHistoryLimit]
	}
	body := make([]byte, 4+57*len(rows))
	binary.LittleEndian.PutUint32(body, uint32(len(rows)))
	for i, entry := range rows {
		row := body[4+i*57 : 4+(i+1)*57]
		binary.LittleEndian.PutUint32(row, uint32(entry.At.Unix()))
		row[4] = entry.Kind
		// The client scans for NUL from +5. Keep a full encoded character and
		// leave at least one zero byte before the item ID at +35.
		name := row[5:35]
		n := 0
		for _, r := range entry.CharacterName {
			encoded := rosterNameBytes(string(r))
			if n+len(encoded) > len(name)-1 {
				break
			}
			n += copy(name[n:], encoded)
		}
		if n == 0 {
			return nil, dnfguild.ErrCargoHistory
		}
		binary.LittleEndian.PutUint32(row[35:], uint32(entry.Item.ItemID))
		binary.LittleEndian.PutUint32(row[39:], uint32(entry.Item.Count))
		// Reuse only the 119-byte instance row, independent of inventory type,
		// quantity/quality seeds and live expiration-time projections.
		item := currentItemListEntryFromRaw(entry.Item.RawEntry, entry.Item.Extra)
		for j, offset := range [...]int{0x48, 0x4b, 0x4e, 0x49, 0x4c, 0x4f, 0x4a, 0x4d, 0x50, 0x51, 0x54, 0x55, 0x56, 0x53} {
			row[43+j] = item.data[offset]
		}
		// Native 261 substitutes an empty triplet when the extra option is -1.
		if item.data[0x52] == 0xff {
			clear(row[53:56])
		}
	}
	return body, nil
}
