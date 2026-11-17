package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCompletedRaidRetainedSeatDoesNotClaimOrdinaryDungeon(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		single, completed, timedOut bool
		phase                       byte
		wantOrdinary                bool
	}{
		{"active", false, false, false, 0, false},
		{"phase_break", false, true, false, 0, false},
		{"team_final", false, true, false, 1, true},
		{"single_final", true, true, false, 0, true},
		{"fiendwar_final", false, true, false, 0, true},
		{"failed", false, false, true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 1)
			f.service.options.currentClientVersion = "official-24.9"
			p := f.session()
			p.channel.ID, p.channel.Type = 11, 0
			f.state.Started = true
			b := &runtimeRaidBattle{InstanceID: "retained-finished-raid", Mode: 1,
				ChannelID: 25, ChannelType: dnfenum.RaidChannelType,
				Single: tc.single, PhaseCompleted: tc.completed, TimedOut: tc.timedOut, Phase: tc.phase,
				EnterAfter: time.Now().Add(-time.Hour), Deadline: time.Now().Add(time.Hour),
				Dungeons: []raid.DungeonState{{ID: 210, State: 2}}}
			f.state.Battle = b
			if tc.name == "fiendwar_final" {
				b.FiendWar = &fiendwar.FiendWarBattle{}
			}
			for _, id := range []int64{310, 5097} {
				r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: id}}
				err := f.service.freezeCurrentRaidDungeonGroupEntry(p, r, nil)
				if (err == nil) != tc.wantOrdinary {
					t.Fatalf("ordinary dungeon %d: error=%v allowed=%v", id, err, tc.wantOrdinary)
				}
				if tc.wantOrdinary {
					if err := f.service.validateCurrentRaidDungeonEntry(p, r); err != nil {
						t.Fatal(err)
					}
					if r.raidEntry != nil {
						t.Fatal("ordinary runtime acquired a raid binding")
					}
				}
			}
			if f.state.Battle != b || !f.state.Started || len(f.state.Members) != 1 {
				t.Fatal("admission changed retained raid/reward ownership")
			}
			for _, r := range []*runtimeDungeonState{
				{Dungeon: worldmap.Dungeon{ID: 210}},
				{Dungeon: worldmap.Dungeon{ID: 211, SourceSections: []worldmap.RawSection{{Name: "raid dungeon"}}}},
				{Dungeon: worldmap.Dungeon{ID: 5054, Extensions: []worldmap.ExtensionSection{{Name: "raid dungeon"}}}},
				{Dungeon: worldmap.Dungeon{}},
				{Dungeon: worldmap.Dungeon{ID: 310}, raidEntry: &currentRaidDungeonEntry{InstanceID: "old-run"}},
				nil,
			} {
				if !f.service.currentRaidDungeonEntryBindingRequired(p, r) {
					t.Fatal("raid or unknown runtime bypassed admission")
				}
			}
			p.channel.ID, p.channel.Type = 25, dnfenum.RaidChannelType
			if !f.service.currentRaidDungeonEntryBindingRequired(p, &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}}) {
				t.Fatal("raid channel bypassed admission")
			}
		})
	}
}

func TestRealPVFCompletedRaidOrdinaryEntryKeepsForeignRaidMapsGuarded(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{SkipMaps: true, SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	p := f.session()
	p.channel.ID, p.channel.Type = 11, 0
	f.state.Started = true
	f.state.Battle = &runtimeRaidBattle{InstanceID: "finished-phase-two", Mode: 1, Phase: 1,
		PhaseCompleted: true, ChannelID: 25, ChannelType: dnfenum.RaidChannelType,
		Dungeons: []raid.DungeonState{{ID: 219, State: 2}}}
	for _, tc := range []struct {
		id       int64
		ordinary bool
	}{{310, true}, {5097, true}, {210, false}, {5054, false}} {
		d, ok := table.FindDungeon(tc.id)
		if !ok {
			t.Fatalf("real dungeon missing: %d", tc.id)
		}
		r := &runtimeDungeonState{Dungeon: d}
		// The normal-channel selector is deliberately permissive. The entry
		// binding check must independently guard the loaded PVF raid map.
		if !f.service.currentPreparedDungeonSelectionAllowed(p, uint32(tc.id), nil) {
			t.Fatal("selector fixture changed")
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(p, r, nil); (err == nil) != tc.ordinary {
			t.Fatalf("dungeon %d: ordinary=%v freeze=%v", tc.id, tc.ordinary, err)
		}
		if err := f.service.validateCurrentRaidDungeonEntry(p, r); (err == nil) != tc.ordinary {
			t.Fatalf("dungeon %d: ordinary=%v commit=%v", tc.id, tc.ordinary, err)
		}
		if r.raidEntry != nil {
			t.Fatal("ordinary or rejected foreign map acquired binding")
		}
	}
}
