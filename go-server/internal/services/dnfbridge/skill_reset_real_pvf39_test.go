package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	profession "longheng.io/server/internal/modules/dnf/profession"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFResetFreeAwakeningEvidence39(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("fixed QA PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(ctx, archive, catalog)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for job := byte(0); job < 16; job++ {
		for branch := byte(0); branch <= 5; branch++ {
			base, _ := profiles.FreeGrants(job, branch, 0)
			awakened, _ := profiles.FreeGrants(job, branch, 1)
			initial := map[uint16]int{}
			for _, g := range base {
				initial[g.SkillID] = g.Level
			}
			for _, g := range awakened {
				if g.Level <= initial[g.SkillID] {
					continue
				}
				def, ok := catalog.Find(job, g.SkillID)
				if !ok || !def.Active {
					continue
				}
				found++
				t.Logf("job=%d branch=%d stage=1 grant=%+v definition=%+v FixedLevelForCharacter(49)=%d (50)=%d (85)=%d (90)=%d", job, branch, g, def, def.FixedLevelForCharacter(49), def.FixedLevelForCharacter(50), def.FixedLevelForCharacter(85), def.FixedLevelForCharacter(90))
				if found == 1 {
					raw, err := archive.ReadText(def.Path)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("actual skill raw:\n%s", raw)
					list, err := archive.ReadText("character/character.lst")
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("actual character list:\n%s", list)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no real free first-awakening active fixed-level skill found")
	}
}

// Job/branch/skill and expected levels come from the fixed QA character/skill documents.
func TestRealPVFResetOwnerRebuildsFreeAwakening39(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("fixed QA PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(ctx, archive, catalog)
	if err != nil {
		t.Fatal(err)
	}
	chr, err := archive.ReadText("character/swordman/swordman.chr")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual chr:\n%s", chr)
	grants, ok := profiles.FreeGrants(0, 1, 1)
	if !ok {
		t.Fatal("missing real profile")
	}
	levels := map[uint16]int{}
	for _, g := range grants {
		levels[g.SkillID] = g.Level
	}
	if levels[86] != 1 {
		t.Fatalf("actual first-awakening grant=%v", grants)
	}
	for _, tc := range []struct{ level, want int }{{50, 1}, {54, 1}, {55, 2}, {85, 8}, {90, 9}} {
		t.Run(strconv.Itoa(tc.level), func(t *testing.T) {
			r := memory.NewMemoryGroup()
			character := repo.CharacterRecord{CharacterID: "39", AccountID: "skill39", Job: "0", Level: tc.level, Stats: map[string]int64{"grow_type": 0x11}}
			if err := r.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			cooldown := time.Unix(1234, 0).UTC()
			before := repo.SkillRecord{CharacterID: "39", Skills: map[int64]repo.SkillState{86: {Level: 30, Enabled: true}}, Cooldowns: map[int64]time.Time{86: cooldown}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 3, SyncedLevel: tc.level}}
			if err := r.Skill.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			points := repo.SkillPointState{TotalSP: 100, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: tc.level}
			owner, err := skillcmd.NewOwner(r, skillcmd.OwnerOptions{Catalog: catalog, InitialLevels: levels, PointBaseline: &points})
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.ApplyReset(ctx, skillcmd.Command{Operation: "skill_init", AccountID: "skill39", SelectedCharacterID: 39})
			if err != nil {
				t.Fatal(err)
			}
			after, _, err := r.Skill.Load(ctx, "39")
			if err != nil {
				t.Fatal(err)
			}
			if after.Skills[86].Level != tc.want {
				t.Errorf("public Reset fixed free skill86 level=%d want=%d", after.Skills[86].Level, tc.want)
			}
			if after.Points != points || result.Points != points || !reflect.DeepEqual(after.Cooldowns, before.Cooldowns) {
				t.Fatalf("reset points/cooldown=%+v", after)
			}
			// An unrelated account must never alter the character's skill record.
			_, err = owner.ApplyReset(ctx, skillcmd.Command{Operation: "skill_init", AccountID: "other", SelectedCharacterID: 39})
			if err == nil {
				t.Fatal("foreign account reset accepted")
			}
			unchanged, _, _ := r.Skill.Load(ctx, "39")
			if !reflect.DeepEqual(after, unchanged) {
				t.Fatal("foreign reset mutated skill state")
			}
			for _, refund := range []byte{0, 1} {
				_, err = owner.ApplyBuy(ctx, skillcmd.NewBuyCommand(alignedcmd.Request{AccountID: "skill39", SelectedCharacterID: 39}, skillcmd.BuySkillRequest{Count: 1, Entries: []skillcmd.BuySkillEntry{{SkillID: 86, LevelDelta: 1, RefundFlag: refund}}}))
				if !errors.Is(err, skillcmd.ErrSkillLevel) {
					t.Fatalf("automatic skill manual mutation refund=%d err=%v", refund, err)
				}
				unchanged, _, _ = r.Skill.Load(ctx, "39")
				if !reflect.DeepEqual(after, unchanged) {
					t.Fatal("rejected automatic skill mutation changed ledger/state")
				}
			}
			failure := errors.New("skill39 forced transaction rollback after save")
			r.CharacterSkills = resetRollback39{delegate: r.CharacterSkills, failure: failure}
			failingOwner, err := skillcmd.NewOwner(r, skillcmd.OwnerOptions{Catalog: catalog, InitialLevels: levels, PointBaseline: &points})
			if err != nil {
				t.Fatal(err)
			}
			_, err = failingOwner.ApplyReset(ctx, skillcmd.Command{Operation: "skill_init", AccountID: "skill39", SelectedCharacterID: 39})
			if !errors.Is(err, failure) {
				t.Fatalf("reset transaction err=%v", err)
			}
			unchanged, _, _ = r.Skill.Load(ctx, "39")
			if !reflect.DeepEqual(after, unchanged) {
				t.Fatal("failed reset transaction committed state")
			}
		})
	}
}

type resetRollback39 struct {
	delegate repo.CharacterSkillUnitOfWork
	failure  error
}

func (u resetRollback39) WithinCharacterSkill(ctx context.Context, id string, apply func(repo.SkillRepository) error) error {
	return u.delegate.WithinCharacterSkill(ctx, id, func(scoped repo.SkillRepository) error {
		if err := apply(scoped); err != nil {
			return err
		}
		return u.failure
	})
}

func TestRealPVFResetBridgeCommand39(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("fixed QA PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "39", AccountID: "skill39", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 0x11}}); err != nil {
		t.Fatal(err)
	}
	before := repo.SkillRecord{CharacterID: "39", Skills: map[int64]repo.SkillState{}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 2, SyncedLevel: 90}}
	if err := r.Skill.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive, initialSPTable: map[int]int{1: 100}, initialTPTable: map[int]int{50: 10}, repositoryProvider: func() (repo.Group, bool) { return r, true }, options: options{accountID: "skill39", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "skill39-reset", selectedCharacterID: 39}
	handled, err := service.handleAlignedGameCommand(session, 1, uint16(dnfenum.CmdPacketSkillInit), []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("reset route handled=%v err=%v", handled, err)
	}
	after, _, err := r.Skill.Load(ctx, "39")
	if err != nil {
		t.Fatal(err)
	}
	if after.Skills[86].Level != 9 {
		t.Fatalf("real op491 route skill86=%+v want level9", after.Skills[86])
	}
	if after.Skills[87].Level != 0 {
		t.Fatal("reset granted another profession's first awakening")
	}
	if after.Points.RemainingSP != 100 || after.Points.RemainingTP != 10 {
		t.Fatal(after.Points)
	}
	// Missing authoritative growth must withhold reset rules, never fall back to base-job grants.
	character, _, _ := r.Character.Load(ctx, "39")
	character.Stats = nil
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	rules, levels, points := service.skillResetOwnerRules(ctx, r, 39)
	if rules != nil || levels != nil || points != nil {
		t.Fatal("missing grow_type received reset rules")
	}
	connection.write.Reset()
	handled, err = service.handleAlignedGameCommand(session, 1, uint16(dnfenum.CmdPacketSkillInit), []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("missing growth reset route handled=%v err=%v", handled, err)
	}
	unchanged, _, _ := r.Skill.Load(ctx, "39")
	if !reflect.DeepEqual(after, unchanged) {
		t.Fatal("missing grow_type reset mutated persisted state")
	}
	if connection.write.Len() != 0 {
		t.Fatal("missing grow_type reset emitted success packet")
	}
}
