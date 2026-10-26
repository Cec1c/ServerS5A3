package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

// The current area40 WDM and LukeStatusBoard catalogue resolve the team dungeon
// identities, and the current absent-state constructor hides(2) every identity
// it cannot find. A single-Luke owner therefore has to project its own native
// row into the reviewed catalogue slot, keep every other catalogue slot hidden,
// and leave the six frozen native rows untouched.
//
// This pins that projection without touching the PVF reader, the mapping
// producer or any other product owner. The native-to-catalogue roles below are
// an independent transcription of the reviewed dungeon.lst and
// etc/raid/luke/luke.etc roster: light week 5323..5328, dark week 5329..5334.
const (
	lukeContractLightFirst uint32 = 5323
	lukeContractDarkFirst  uint32 = 5329
	lukeContractInfection  uint32 = 1307
)

// lukeContractCatalogueRole is the area40/LukeStatusBoard slot each single
// native dungeon projects onto. The light and dark birth sanctums share the one
// birth sanctum slot across weeks; inside one week no two natives share a slot.
var lukeContractCatalogueRole = map[uint32]uint32{
	5323: 5054,
	5324: 5055,
	5325: 5057,
	5326: 5059,
	5327: 5064,
	5328: 5061,
	5329: 5054,
	5330: 5056,
	5331: 5058,
	5332: 5060,
	5333: 5065,
	5334: 5062,
}

// lukeContractCatalogueSlots is the exact area40/LukeStatusBoard union the single
// weeks reuse: the 27 [dungeon] ids of worldmap_luke_raid.wdm plus the 32
// [DUNGEON INDEX] ids of lukestatusboard.etc, 42 unique ids. Every one of them
// must appear in every projection, and only the current week's slot may open.
// Source: parent/verify_luke_selector_catalog.py and its
// parent/luke-selector-native/receipt.json (wdm_dungeon_ids, board_dungeon_ids).
var lukeContractCatalogueSlots = []uint32{
	321, 322, 323, 324, 325, 326, 327, 328, 329, 330, 331, 332, 333, 334, 335,
	5054, 5055, 5056, 5057, 5058, 5059, 5060, 5061, 5062, 5063, 5064, 5065, 5066,
	5090, 5091, 5092, 5093, 5094, 5095, 5099,
	5300, 5310, 5311, 5312, 5313, 5340, 5341,
}

var lukeContractCatalogueSet = func() map[uint32]bool {
	set := make(map[uint32]bool, len(lukeContractCatalogueSlots))
	for _, slot := range lukeContractCatalogueSlots {
		set[slot] = true
	}
	return set
}()

func lukeContractWeek(first uint32) []uint32 {
	week := make([]uint32, 0, 6)
	for offset := uint32(0); offset < 6; offset++ {
		week = append(week, first+offset)
	}
	return week
}

// lukeContractBattle freezes one week at one progress index without a PVF or a
// service. Row i is the selected open(0) dungeon at the index and closed(1)
// otherwise, which is exactly the state the owned clear reducer leaves behind;
// walking past the last dungeon is the completed owner.
func lukeContractBattle(first uint32, index int) *runtimeRaidBattle {
	week := lukeContractWeek(first)
	b := &runtimeRaidBattle{
		Mode:           2,
		Single:         true,
		Phase:          0,
		SingleSequence: week,
		SingleIndex:    index,
		PhaseCompleted: index == len(week),
	}
	for i, native := range week {
		state := byte(1)
		if i == index {
			state = 0
		}
		b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: native, State: state})
	}
	return b
}

// lukeContractRows decodes the actual 585 body the projection returns.
func lukeContractRows(t *testing.T, body []byte) []raid.DungeonState {
	t.Helper()
	if len(body) < 2 {
		t.Fatalf("dungeon state body truncated: %d bytes", len(body))
	}
	count := int(body[1])
	if len(body) != 2+count*5+4 {
		t.Fatalf("dungeon state row boundary: bytes=%d rows=%d", len(body), count)
	}
	if got := binary.LittleEndian.Uint32(body[len(body)-4:]); got != lukeContractInfection {
		t.Fatalf("infection trailer = %d want %d", got, lukeContractInfection)
	}
	rows := make([]raid.DungeonState, 0, count)
	for i := 0; i < count; i++ {
		offset := 2 + i*5
		rows = append(rows, raid.DungeonState{ID: binary.LittleEndian.Uint32(body[offset : offset+4]), State: body[offset+4]})
	}
	return rows
}

// lukeContractAssertProjection checks the whole row set independently: the six
// native rows keep exactly the frozen states the owner handed over, every other
// row is a catalogue slot, and only the current week's slot may be open.
func lukeContractAssertProjection(t *testing.T, body []byte, b *runtimeRaidBattle) {
	t.Helper()
	rows := lukeContractRows(t, body)
	if len(rows) != len(lukeContractCatalogueSlots)+len(b.Dungeons) {
		t.Fatalf("projected rows %d want %d", len(rows), len(lukeContractCatalogueSlots)+len(b.Dungeons))
	}
	native := map[uint32]byte{}
	for _, row := range b.Dungeons {
		if _, exists := native[row.ID]; exists {
			t.Fatalf("fixture repeated native row %d", row.ID)
		}
		native[row.ID] = row.State
	}
	current, open := uint32(0), false
	if !b.TimedOut && b.SingleIndex < len(b.SingleSequence) {
		current = lukeContractCatalogueRole[b.SingleSequence[b.SingleIndex]]
		if current == 0 {
			t.Fatalf("fixture native %d has no reviewed catalogue role", b.SingleSequence[b.SingleIndex])
		}
		open = true
	}
	seen := map[uint32]bool{}
	opened := make([]uint32, 0, 1)
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("repeated row %d", row.ID)
		}
		seen[row.ID] = true
		if want, ok := native[row.ID]; ok {
			if row.State != want {
				t.Fatalf("native row %d state %d want %d", row.ID, row.State, want)
			}
			continue
		}
		if !lukeContractCatalogueSet[row.ID] {
			t.Fatalf("unexpected row %d outside the area40 catalogue union", row.ID)
		}
		want := byte(2)
		if open && row.ID == current {
			want = 0
		}
		if row.State != want {
			t.Fatalf("catalogue row %d state %d want %d", row.ID, row.State, want)
		}
		if row.State == 0 {
			opened = append(opened, row.ID)
		}
	}
	for _, row := range b.Dungeons {
		if !seen[row.ID] {
			t.Fatalf("native row %d missing from the projection", row.ID)
		}
	}
	for _, slot := range lukeContractCatalogueSlots {
		if !seen[slot] {
			t.Fatalf("area40 catalogue slot %d not projected", slot)
		}
	}
	if open {
		if len(opened) != 1 || opened[0] != current {
			t.Fatalf("open catalogue slots %v want only %d", opened, current)
		}
	} else if len(opened) != 0 {
		t.Fatalf("completed or timeout frame reopened catalogue slots %v", opened)
	}
}

func TestSingleLukeSelectorContractArea40SlotPerIndex(t *testing.T) {
	for _, first := range []uint32{lukeContractLightFirst, lukeContractDarkFirst} {
		week := lukeContractWeek(first)
		roles := map[uint32]bool{}
		for _, native := range week {
			role := lukeContractCatalogueRole[native]
			if role == 0 || roles[role] {
				t.Fatalf("fixture week%d native %d reuses catalogue slot %d", first, native, role)
			}
			roles[role] = true
		}
		for index := 0; index <= len(week); index++ {
			t.Run(fmt.Sprintf("week%d_index%d", first, index), func(t *testing.T) {
				b := lukeContractBattle(first, index)
				if b.PhaseCompleted != (index == len(week)) {
					t.Fatal("fixture completion owner")
				}
				body, err := currentRaidDungeonProjectionBody(b, lukeContractInfection)
				if err != nil {
					t.Fatal(err)
				}
				lukeContractAssertProjection(t, body, b)
				alias := lukeContractCatalogueRole[week[index%len(week)]]
				states := map[uint32]byte{}
				for _, row := range lukeContractRows(t, body) {
					states[row.ID] = row.State
				}
				if index < len(week) && states[alias] != 0 {
					t.Fatalf("current week%d index%d slot %d state %d want 0", first, index, alias, states[alias])
				}
				if index == len(week) && states[alias] != 2 {
					t.Fatalf("completed week%d slot %d state %d want 2", first, alias, states[alias])
				}
			})
		}
	}
}

func TestSingleLukeSelectorContractTimeoutHidesEveryRow(t *testing.T) {
	for _, first := range []uint32{lukeContractLightFirst, lukeContractDarkFirst} {
		for _, index := range []int{0, 3, 5} {
			t.Run(fmt.Sprintf("week%d_index%d", first, index), func(t *testing.T) {
				b := lukeContractBattle(first, index)
				b.TimedOut = true
				for i := range b.Dungeons {
					b.Dungeons[i].State = 2
				}
				body, err := currentRaidDungeonProjectionBody(b, lukeContractInfection)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range lukeContractRows(t, body) {
					if row.State != 2 {
						t.Fatalf("timeout row %d state %d want 2", row.ID, row.State)
					}
				}
				lukeContractAssertProjection(t, body, b)
			})
		}
	}
}

// TestSingleLukeSelectorContractCatalogueUnionFixture guards the literal union this
// file pins, so a drifted or duplicated transcription fails before the projection
// assertions can quietly agree with it.
func TestSingleLukeSelectorContractCatalogueUnionFixture(t *testing.T) {
	if len(lukeContractCatalogueSlots) != 42 {
		t.Fatalf("area40 catalogue union size %d want 42", len(lukeContractCatalogueSlots))
	}
	if len(lukeContractCatalogueSet) != len(lukeContractCatalogueSlots) {
		t.Fatalf("area40 catalogue union repeats an id: %d unique of %d", len(lukeContractCatalogueSet), len(lukeContractCatalogueSlots))
	}
	for _, first := range []uint32{lukeContractLightFirst, lukeContractDarkFirst} {
		for _, native := range lukeContractWeek(first) {
			if lukeContractCatalogueSet[native] {
				t.Fatalf("single native %d leaked into the area40 catalogue union", native)
			}
		}
	}
}

func TestSingleLukeSelectorContractRejectsMalformedOwners(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*runtimeRaidBattle)
	}{
		{"duplicate_native", func(b *runtimeRaidBattle) {
			b.SingleSequence[1] = b.SingleSequence[0]
			b.Dungeons[1].ID = b.SingleSequence[0]
		}},
		{"cross_week_native", func(b *runtimeRaidBattle) {
			other := uint32(5334)
			if b.SingleSequence[0] == lukeContractDarkFirst {
				other = 5328
			}
			b.SingleSequence[5] = other
			b.Dungeons[5].ID = other
		}},
		{"unknown_native", func(b *runtimeRaidBattle) {
			b.SingleSequence[2] = 5399
			b.Dungeons[2].ID = 5399
		}},
		{"missing_native_row", func(b *runtimeRaidBattle) {
			b.Dungeons = b.Dungeons[:len(b.Dungeons)-1]
		}},
		{"row_ownership_mismatch", func(b *runtimeRaidBattle) {
			b.Dungeons[3].ID = b.SingleSequence[4]
		}},
		{"future_native_open", func(b *runtimeRaidBattle) {
			b.Dungeons[4].State = 0
		}},
		{"index_negative", func(b *runtimeRaidBattle) {
			b.SingleIndex = -1
		}},
		{"index_past_end", func(b *runtimeRaidBattle) {
			b.SingleIndex = 7
		}},
		{"index_at_end_without_completion", func(b *runtimeRaidBattle) {
			b.SingleIndex = 6
		}},
		{"completion_without_end_index", func(b *runtimeRaidBattle) {
			b.PhaseCompleted = true
		}},
		{"phase_clock", func(b *runtimeRaidBattle) {
			b.Phase = 1
		}},
		{"timeout_with_completed_owner", func(b *runtimeRaidBattle) {
			b.TimedOut = true
			b.SingleIndex = 6
			b.PhaseCompleted = true
			for i := range b.Dungeons {
				b.Dungeons[i].State = 2
			}
		}},
	}
	for _, tc := range cases {
		for _, first := range []uint32{lukeContractLightFirst, lukeContractDarkFirst} {
			t.Run(fmt.Sprintf("%s_week%d", tc.name, first), func(t *testing.T) {
				b := lukeContractBattle(first, 2)
				tc.mutate(b)
				body, err := currentRaidDungeonProjectionBody(b, lukeContractInfection)
				if err == nil {
					t.Fatal("malformed single Luke owner admitted")
				}
				if body != nil {
					t.Fatal("malformed single Luke owner produced a body")
				}
			})
		}
	}
}
