package dnfbridge

import (
	"bytes"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/config"
	"testing"
	"time"
)

// Actual 8a7af614... EXE reads mode-2 rows at VA 0x0200b250.
// Independently walk every row so a correct first row cannot hide later drift.
func TestOfficialGameplayWith90NativeRoster(t *testing.T) {
	t.Setenv("DNFBRIDGE_CURRENT_CLIENT_VERSION", "official-24.9")
	t.Setenv("DNFBRIDGE_NATIVE_CLIENT_PROFILE", "90cn")
	s := residentNoticeTestService(t)
	s.options = optionsFromConfig(config.ServiceConfig{})
	if s.options.currentClientVersion != "official-24.9" {
		t.Fatal("gameplay profile changed")
	}
	chars := []dnfrepo.CharacterRecord{
		{CharacterID: "1", Name: "旭旭宝宝", Slot: 0, Job: "0", Level: 90},
		{CharacterID: "3", Name: "嚣张", Slot: 1, Job: "0", Level: 90},
		{CharacterID: "2", Name: "222", Slot: 2, Job: "13", Level: 1},
		{CharacterID: "4", Name: "111", Slot: 3, Job: "11", Level: 1},
	}
	body := s.buildCurrentRosterBody(chars)
	if binary.LittleEndian.Uint16(body[13:15]) != 4 {
		t.Fatal("lost roles")
	}
	cursor := 15
	for _, c := range chars {
		if int(binary.LittleEndian.Uint16(body[cursor:])) != c.Slot {
			t.Fatal("slot drift", cursor)
		}
		cursor += 2
		n := int(binary.LittleEndian.Uint32(body[cursor:]))
		cursor += 4
		if n > 30 || !bytes.Equal(body[cursor:cursor+n], rosterRawNameBytes(c)) {
			t.Fatal("name drift", cursor)
		}
		cursor += n + 2
		if body[cursor] != byte(numericCharacterStat(c.Job)) || body[cursor+2] != byte(c.Level) {
			t.Fatal("job/level drift", cursor)
		}
		cursor += 5 + 8
		count := int(body[cursor])
		cursor++
		for i := 0; i < count; i++ {
			cursor += 5
			n := int(binary.LittleEndian.Uint32(body[cursor:]))
			cursor += 4 + n + 10
		}
		if body[cursor+24] != 3 {
			t.Fatal("normal role status drift", cursor)
		}
		cursor += 38
	}
	if len(body)-cursor != 10 {
		t.Fatalf("row boundary drift: cursor=%d len=%d", cursor, len(body))
	}
	ch, _ := s.currentCatalog().Channel(10)
	notice, err := s.buildCurrentChannelResidentNotice(ch, time.Unix(1721020000, 0))
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := consumeChannelInfoProto(t, notice[4:])
	if fields[30004][0] != 1519 || fields[30005][0] != 1468 {
		t.Fatal("native packet count mismatch")
	}
}
