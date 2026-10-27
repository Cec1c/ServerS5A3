package dnfbridge

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestSingleLukeTeamProjectionKeepsOriginalRowsRound8(t *testing.T) {
	for _, phase := range []byte{0, 1} {
		b := &runtimeRaidBattle{Mode: 2, Phase: phase, Dungeons: []raid.DungeonState{{ID: 5054, State: 0}, {ID: 5056, State: 1}, {ID: 5099, State: 2}}}
		before := cloneRuntimeRaidBattle(b)
		body, err := currentRaidDungeonProjectionBody(b, 5099)
		want, wantErr := raid.BuildDungeonStateBody(b.Dungeons, 5099)
		if err != nil || wantErr != nil || !bytes.Equal(body, want) || !reflect.DeepEqual(before, cloneRuntimeRaidBattle(b)) {
			t.Fatalf("team Luke phase=%d projection changed: err=%v wantErr=%v", phase, err, wantErr)
		}
	}
}

func TestSingleLukeResolveEveryWeekAndIndexRound8(t *testing.T) {
	for _, first := range []uint32{5323, 5329} {
		for index := 0; index < 6; index++ {
			t.Run(fmt.Sprintf("week%d_index%d", first, index), func(t *testing.T) {
				f, session := singleLukeSelectorGuardFixture(t)
				b := f.state.Battle
				b.SingleSequence = lukeContractWeek(first)
				b.SingleIndex = index
				b.Dungeons = lukeContractBattle(first, index).Dungeons
				before := cloneRuntimeRaidBattle(b)
				native := first + uint32(index)
				alias := lukeContractCatalogueRole[native]
				if got := f.service.resolveCurrentSingleLukeSelector(session, alias); got != native {
					t.Fatalf("slot %d resolved to %d want owned native %d", alias, got, native)
				}
				for other, slot := range lukeContractCatalogueRole {
					if slot != alias && f.service.resolveCurrentSingleLukeSelector(session, slot) != slot {
						t.Fatalf("noncurrent slot %d (native %d) translated", slot, other)
					}
				}
				if !reflect.DeepEqual(before, cloneRuntimeRaidBattle(b)) {
					t.Fatal("resolution mutated frozen battle owner")
				}
			})
		}
	}
}
