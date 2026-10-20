package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// P67 names 黑暗冥王阵; the real job9 list names skill 214 黑暗明王阵.
// These are ephemeral PVF-derived boundary fixtures, NOT test74 database data.
func TestRealPVFR134DarkKnightTPLearning(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the P67 real-PVF regression")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := parseInitialCharacterSkillsWithCatalog(archive, 9, catalog)
	if err != nil {
		t.Fatal(err)
	}
	sp, tp, err := parseInitialSkillPointTablesFromSource(archive)
	if err != nil {
		t.Fatal(err)
	}
	targets := []struct {
		id, pre    uint16
		name, file string
		fitness    int
	}{
		{154, 9, "强化 - 破魂斩", "drawswordexp.skl", 1},
		{158, 79, "强化 - 灭魂封魔斩", "darkraveexp.skl", 3},
		{226, 60, "强化 - 禁魂斩", "momentaryslashexp.skl", 2},
		{166, 24, "强化 - 暗劲爆发", "darkexplosionexp.skl", 3},
		{217, 73, "强化 - 魔影剑舞", "illusionslashexp.skl", 1},
		{214, 74, "强化 - 黑暗明王阵", "wavespinexp.skl", 4},
	}
	for _, target := range targets {
		d, ok := catalog.Find(9, target.id)
		if !ok || d.Name != target.name || d.Path != "skill/demonicswordman/"+target.file ||
			!d.IsTPSkill() || d.FixedLevelSkill || d.RequiredLevel != 65 || d.MaximumLevel != 7 ||
			!reflect.DeepEqual(d.Prerequisites, []dnfskill.Prerequisite{{SkillID: target.pre, Level: 10}}) ||
			!reflect.DeepEqual(d.GrowTypes, []int{target.fitness}) || d.LevelCost(0) != 2 ||
			d.MaxLevelForGrowType(0) != 5 || d.MaxLearnableLevel(0, 69) != 5 {
			t.Fatalf("real skill contract changed: %+v", d)
		}
		t.Logf("job=9 skill=%d name=%q path=%s required=%d cap=%v fitness=%v prerequisites=%v TP=%v", d.ID, d.Name, d.Path, d.RequiredLevel, d.GrowtypeMaximumLevels, d.GrowTypes, d.Prerequisites, d.SpecialPurchaseCost)
		for _, route := range []string{"owner-handler", "upper", "legacy-normalized"} {
			for _, grow := range []int{0, 16, 32} {
				t.Run(fmt.Sprintf("%d/%s/grow%d", target.id, route, grow), func(t *testing.T) {
					f := newR134TPFixture(t, catalog, initial, sp, tp, d.RequiredLevel, grow, route)
					f.learn(t, target.pre, 10)
					before := f.record(t)
					f.buy(t, []skillcmd.BuySkillEntry{{SkillID: target.id, LevelDelta: 1}}, true)
					after := f.record(t)
					if after.Skills[int64(target.id)].Level != 1 || after.Points.RemainingTP != before.Points.RemainingTP-2 || after.Points.RemainingSP != before.Points.RemainingSP {
						t.Fatalf("learn ledger: before=%+v after=%+v", before.Points, after.Points)
					}
					// At the exact PVF minimum level, rank 2 is still level-gated.
					f.buy(t, []skillcmd.BuySkillEntry{{SkillID: target.id, LevelDelta: 1}}, false)
					f.assertUnchanged(t, after)
				})
			}
		}
		for _, denied := range []string{"missing-prerequisite", "prerequisite-9", "below-level", "unavailable-grow", "insufficient-tp", "foreign-job"} {
			t.Run(fmt.Sprintf("%d/reject/%s", target.id, denied), func(t *testing.T) {
				level := d.RequiredLevel
				if denied == "below-level" {
					level--
				}
				f := newR134TPFixture(t, catalog, initial, sp, tp, level, 0, "owner-handler")
				if denied != "missing-prerequisite" && denied != "foreign-job" {
					preLevel := 10
					if denied == "prerequisite-9" {
						preLevel--
					}
					f.learn(t, target.pre, preLevel)
				}
				if denied == "foreign-job" {
					found := false
					for j := byte(0); j < 16; j++ {
						if _, exists := catalog.Find(j, target.id); exists {
							continue
						}
						foreignInitial, e := parseInitialCharacterSkillsWithCatalog(archive, j, catalog)
						if e != nil {
							t.Fatal(e)
						}
						f = newR134TPFixture(t, catalog, foreignInitial, sp, tp, level, 0, "owner-handler", j)
						f.failureCode = 3
						found = true
						break
					}
					if !found {
						t.Fatal("no foreign-job missing-ID control")
					}
				}
				if denied == "unavailable-grow" {
					ch, _, e := f.repos.Character.Load(ctx, "65001")
					if e != nil {
						t.Fatal(e)
					}
					for g, cap := range d.GrowtypeMaximumLevels {
						if cap == 0 {
							ch.Stats["grow_type"] = int64(g)
							break
						}
					}
					if e := f.repos.Character.Save(ctx, ch); e != nil {
						t.Fatal(e)
					}
				}
				if denied == "insufficient-tp" {
					f.failureCode = 2
					// In-memory ledger boundary only, never a player DB repair.
					r := f.record(t)
					r.Points.RemainingTP = d.LevelCost(0) - 1
					if e := f.repos.Skill.Save(ctx, r); e != nil {
						t.Fatal(e)
					}
				}
				before := f.record(t)
				f.buy(t, []skillcmd.BuySkillEntry{{SkillID: target.id, LevelDelta: 1}}, false)
				f.assertUnchanged(t, before)
			})
		}
	}
	t.Run("six-in-one-batch", func(t *testing.T) {
		f := newR134TPFixture(t, catalog, initial, sp, tp, 65, 0, "upper")
		entries := []skillcmd.BuySkillEntry{}
		for _, target := range targets {
			f.learn(t, target.pre, 10)
			entries = append(entries, skillcmd.BuySkillEntry{SkillID: target.id, LevelDelta: 1})
		}
		before := f.record(t)
		f.buy(t, entries, true)
		after := f.record(t)
		if before.Points.RemainingTP-after.Points.RemainingTP != 12 || before.Points.RemainingSP != after.Points.RemainingSP {
			t.Fatalf("batch ledger: before=%+v after=%+v", before.Points, after.Points)
		}
		for _, target := range targets {
			if after.Skills[int64(target.id)].Level != 1 {
				t.Fatalf("missing skill %d", target.id)
			}
		}
	})
}

type r134TPFixture struct {
	service     *Service
	session     *gameSession
	conn        *bufferConn
	repos       repo.Group
	catalog     *dnfskill.Table
	initial     map[uint16]int
	baseline    *repo.SkillPointState
	route       string
	failureCode byte
}

func newR134TPFixture(t *testing.T, c *dnfskill.Table, initial []initialSkillEntry, sp, tp map[int]int, level, grow int, route string, jobs ...byte) *r134TPFixture {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	job := byte(9)
	if len(jobs) > 0 {
		job = jobs[0]
	}
	character := repo.CharacterRecord{CharacterID: "65001", AccountID: "r134-pvf-fixture", Job: fmt.Sprint(job), Level: level, Stats: map[string]int64{"grow_type": int64(grow), "skill_tree_index": 0}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	p := skillPointsFromPVFTables(sp, tp, level)
	points := repo.SkillPointState{TotalSP: p.TotalSP, RemainingSP: p.TotalSP, TotalTP: p.TotalTP, RemainingTP: p.TotalTP, SyncedLevel: level}
	states := map[int64]repo.SkillState{}
	floors := map[uint16]int{}
	for _, entry := range initial {
		states[entry.SkillID] = repo.SkillState{Level: entry.Level, Enabled: true}
		floors[uint16(entry.SkillID)] = entry.Level
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "65001", Skills: states, Points: points}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	s := &Service{options: options{accountID: character.AccountID, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return repos, true }, skillCatalog: c, initialSkillsByJob: map[byte][]initialSkillEntry{job: initial}, initialSPTable: sp, initialTPTable: tp}
	session := &gameSession{conn: conn, connID: "r134-real-pvf-tp", selectedCharacterID: 65001, channel: channelcatalog.Channel{ID: 19}}
	return &r134TPFixture{s, session, conn, repos, c, floors, &points, route, 18}
}

func (f *r134TPFixture) record(t *testing.T) repo.SkillRecord {
	t.Helper()
	r, ok, e := f.repos.Skill.Load(context.Background(), "65001")
	if e != nil || !ok {
		t.Fatalf("load found=%t err=%v", ok, e)
	}
	return r
}
func (f *r134TPFixture) assertUnchanged(t *testing.T, before repo.SkillRecord) {
	t.Helper()
	if after := f.record(t); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected request changed skills, layout, points or timestamps")
	}
}

func (f *r134TPFixture) learn(t *testing.T, id uint16, level int) {
	t.Helper()
	d, ok := f.catalog.Find(9, id)
	if !ok {
		t.Fatalf("prerequisite %d missing", id)
	}
	for _, p := range d.Prerequisites {
		f.learn(t, p.SkillID, p.Level)
	}
	current := f.record(t).Skills[int64(id)].Level
	if current < level {
		f.buy(t, []skillcmd.BuySkillEntry{{SkillID: id, LevelDelta: byte(level - current)}}, true)
	}
}

func (f *r134TPFixture) buy(t *testing.T, entries []skillcmd.BuySkillEntry, success bool) {
	t.Helper()
	body := []byte{0, byte(len(entries))}
	for _, e := range entries {
		body = append(body, byte(e.SkillID), byte(e.SkillID>>8), e.RefundFlag, e.LevelDelta)
	}
	body = append(body, 0)
	op := uint16(dnfenum.CmdPacketBuySkill)
	var reply []byte
	if f.route == "owner-handler" {
		r, e := skillcmd.NewHandler().Handle(context.Background(), alignedcmd.Request{Opcode: op, Body: body, AccountID: "r134-pvf-fixture", SelectedCharacterID: 65001, Repositories: f.repos, SkillCatalog: f.catalog, InitialSkillLevels: f.initial, SkillPointBaseline: f.baseline})
		if e != nil || len(r.UpperResponses) != 1 {
			t.Fatalf("handler=%+v err=%v", r, e)
		}
		reply = r.UpperResponses[0].Body
		if (len(reply) > 0 && reply[0] == 1) != success {
			t.Fatalf("entries=%v expected success=%t reply=%x reason=%s", entries, success, reply, r.Reason)
		}
	} else {
		f.conn.write.Reset()
		var e error
		if f.route == "upper" {
			raw, err := dnfproto.BuildChannelPacket(op, body, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			e = f.service.handleGameUpper(f.session, raw)
		} else {
			body = normalizeLegacyGameBody(op, append(body, 0xb8, 0x1a, 0, 0))
			e = f.service.handleGameCommandRequest(f.session, gameplayRequest{Opcode: op, Classification: byte(dnfenum.GameCmdCommand), Body: body})
		}
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range splitAllCurrentUpperPackets(t, f.conn.write.Bytes()) {
			if p.Header.MsgID == op && p.Header.Classification == 1 {
				if len(p.Body) < 3 || !bytes.Equal(p.Body[:3], []byte{0, 0, 0}) {
					t.Fatalf("unexpected codec prefix=%x", p.Body)
				}
				reply = p.Body[3:]
			}
		}
		if len(reply) == 0 || (reply[0] == 1) != success {
			t.Fatalf("route=%s entries=%v expected success=%t reply=%x", f.route, entries, success, reply)
		}
	}
	if !success && !bytes.Equal(reply, []byte{0, f.failureCode}) {
		t.Fatalf("unexpected failure=%x", reply)
	}
}
