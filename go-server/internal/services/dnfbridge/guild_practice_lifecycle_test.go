package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const guildPracticeBridgeETC = `[friend agit]
[index]
8510
[/friend agit]
[friend barricade dungeon]
8511 8512 8513
[/friend barricade dungeon]
[guardian dummy monster]
9
[guardian list]
[guardian]
1
[index]
71021 71022 71023 71024 71025
[/guardian]
[guardian]
2
[index]
63830 63830 63831 63831 63832
[/guardian]
[/guardian list]`

func guildPracticeEntryFixture(t *testing.T) (*Service, map[uint16]*gameSession, map[uint16]*runtimeDungeonState, r.Group) {
	t.Helper()
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	text := guildPracticeBridgeETC
	if path := os.Getenv("GUILD_PRACTICE_EXTRACTED_ETC"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text = string(body)
	}
	items, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "", "etc/agitwar.etc": text,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	runtimes := make(map[uint16]*runtimeDungeonState)
	for id, peer := range peers {
		peer.townPositionSnapshot.CharacterID = id
		c, _, err := repos.Character.Load(context.Background(), fmt.Sprint(id))
		if err != nil {
			t.Fatal(err)
		}
		dgn := worldmap.Dungeon{ID: 8510}
		dgn.Metadata.IntegerValues = map[string]int64{"agit war dungeon": 1}
		dgn.Metadata.Flags = map[string]bool{"friendly agit dungeon": true}
		runtimes[id] = &runtimeDungeonState{AccountID: c.AccountID, Character: c, Dungeon: dgn,
			Session: &worldmap.DungeonSession{}, Request: dungeoncmd.SelectDungeonRequest{DungeonID: 8510, Difficulty: 3, RuntimeState: 1, RuntimeToken: 0xffff}}
	}
	return s, peers, runtimes, repos
}

func TestGuildPracticeAdmissionWholePartyAndSelectorOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member uint16
		change func(*gameSession, *runtimeDungeonState)
		want   bool
	}{
		{"solo", 0, nil, true}, {"same-guild", 30, nil, true}, {"other-guild", 31, nil, false},
		{"nonmember", 32, nil, false}, {"other-channel", 33, nil, false}, {"duplicate-leader", 29, nil, false},
		{"other-area", 30, func(p *gameSession, rt *runtimeDungeonState) {
			p.townPositionSnapshot.AreaID = 1
			rt.Character.Stats["area_id"] = 1
		}, false},
		{"wrong-account", 30, func(_ *gameSession, rt *runtimeDungeonState) { rt.AccountID = "another" }, false},
		{"missing-generation", 30, func(p *gameSession, _ *runtimeDungeonState) { p.characterGeneration = 0 }, false},
		{"request-mismatch", 30, func(_ *gameSession, rt *runtimeDungeonState) { rt.Request.RuntimeState = 0 }, false},
		{"selector-origin", 30, func(p *gameSession, _ *runtimeDungeonState) {
			p.townSelectorOriginBound = true
			p.townSelectorOriginSnapshot = p.townPositionSnapshot
			p.townPositionSnapshot = currentTownPositionSnapshot{}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, peers, runs, _ := guildPracticeEntryFixture(t)
			var followers []*runtimePartyPreparedDungeonEntry
			if tc.member != 0 {
				if tc.change != nil {
					tc.change(peers[tc.member], runs[tc.member])
				}
				followers = append(followers, &runtimePartyPreparedDungeonEntry{session: peers[tc.member], runtime: runs[tc.member]})
			}
			err := s.freezeCurrentGuildPracticeGroup(context.Background(), peers[29], runs[29], followers)
			if (err == nil) != tc.want {
				t.Fatalf("admission error=%v want success=%t", err, tc.want)
			}
			if !tc.want {
				for _, run := range runs {
					if run.guildPractice != nil {
						t.Fatal("failed admission partially bound an owner")
					}
				}
				return
			}
			if err = s.validateCurrentGuildPracticeEntry(peers[29], runs[29]); err != nil {
				t.Fatal(err)
			}
			if tc.member != 0 {
				if err = s.validateCurrentGuildPracticeEntry(peers[tc.member], runs[tc.member]); err != nil {
					t.Fatal(err)
				}
				if runs[29].guildPractice == runs[tc.member].guildPractice {
					t.Fatal("members share mutable delivery flags")
				}
			}
		})
	}
}

func TestGuildPracticeAdmissionMatchesNativeRequestAndExpansion(t *testing.T) {
	for _, id := range []uint32{8510, 8511, 8512, 8513} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, p, runs, _ := guildPracticeEntryFixture(t)
			run := runs[29]
			run.Request.DungeonID = id
			run.Dungeon.ID = int64(id)
			err := s.freezeCurrentGuildPracticeGroup(context.Background(), p[29], run, nil)
			if (err == nil) != (id <= 8511) {
				t.Fatalf("expansion0 id=%d: %v", id, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*runtimeDungeonState)
	}{
		{"ordinary-state", func(rt *runtimeDungeonState) { rt.Request.RuntimeState = 0 }},
		{"difficulty", func(rt *runtimeDungeonState) { rt.Request.Difficulty = 2 }},
		{"token", func(rt *runtimeDungeonState) { rt.Request.RuntimeToken = 0 }},
		{"tail", func(rt *runtimeDungeonState) { rt.Request.OpaqueTail = []byte{1} }},
		{"unowned-map", func(rt *runtimeDungeonState) { rt.Request.DungeonID = 700; rt.Dungeon.ID = 700 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, runs, _ := guildPracticeEntryFixture(t)
			tc.change(runs[29])
			if err := s.freezeCurrentGuildPracticeGroup(context.Background(), p[29], runs[29], nil); err == nil {
				t.Fatal("accepted invalid native shape")
			}
		})
	}
	s, p, runs, _ := guildPracticeEntryFixture(t)
	if err := validateCurrentDungeonEntryRequest(runs[29].Request); err == nil {
		t.Fatal("ordinary validator accepted practice state")
	}
	if err := s.freezeCurrentGuildPracticeGroup(context.Background(), p[29], runs[29], nil); err != nil {
		t.Fatal(err)
	}
	p[29].characterGeneration++
	if err := s.validateCurrentGuildPracticeEntry(p[29], runs[29]); err == nil {
		t.Fatal("stale generation admitted")
	}
}

func TestGuildPracticeFrozenProjectionRefreshAndReturn(t *testing.T) {
	s, p, runs, repos := guildPracticeEntryFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	g.Guardians = &guild.GuardianState{EverAcquired: true, ActiveID: 1, Owned: []guild.Guardian{{ID: 1, Level: 1, Skills: [5]uint8{1, 1, 1, 1, 1}}}}
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	g.Placements = &guild.PlacementState{NextID: 2, Rows: []guild.Placement{{ID: 1, ItemID: 10156091, ObjectID: 71021, CreatorID: 29, Area: 0, X: 900, Y: 276, Maintenance: 15, Cells: []guild.PlacementCell{{X: 1, Y: 1}}, CreatedAt: now, ReadyAt: now}}}
	if err = guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	followers := []*runtimePartyPreparedDungeonEntry{{session: p[30], runtime: runs[30]}}
	if err = s.freezeCurrentGuildPracticeGroup(ctx, p[29], runs[29], followers); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), runs[29].guildPractice.projection...)
	runs[30].guildPractice.Placements[0].Cells[0].X = 99
	runs[30].guildPractice.projection[10] = 88
	if runs[29].guildPractice.Placements[0].Cells[0].X != 1 || !bytes.Equal(before, runs[29].guildPractice.projection) {
		t.Fatal("participant snapshot aliases another member")
	}
	g.Guardians.Owned[0].Level = 11
	g.Placements.Rows[0].X = 901
	if err = guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if runs[29].guildPractice.Guardian.Guardian.Level != 1 || runs[29].guildPractice.Placements[0].X != 900 {
		t.Fatal("live mutation altered frozen run")
	}
	conn := p[29].conn.(*bufferConn)
	if err = s.sendCurrentGuildPracticeSnapshot(p[29], runs[29]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(guildReply(t, p[29], 191), before) {
		t.Fatal("191 differs from frozen owner")
	}
	conn.write.Reset()
	if err = s.sendCurrentGuildPracticeSnapshot(p[29], runs[29]); err != nil {
		t.Fatal(err)
	}
	// This caller already holds the dungeon lock, as settlement can do.
	p[29].dungeon.mu.Lock()
	err = s.writeCurrentGuildEssenceSnapshot(p[29])
	p[29].dungeon.mu.Unlock()
	if err != nil || conn.write.Len() != 0 {
		t.Fatal("duplicate entry or live guild refresh replaced frozen scene", err)
	}
	releaseCurrentGuildPracticeProjection(p[29])
	if err = s.writeCurrentGuildEssenceSnapshot(p[29]); err != nil {
		t.Fatal(err)
	}
	after, err := buildCurrentGuildPlacementSnapshot(g)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(guildReply(t, p[29], 191), after) {
		t.Fatal("return did not restore current guild snapshot")
	}
}
