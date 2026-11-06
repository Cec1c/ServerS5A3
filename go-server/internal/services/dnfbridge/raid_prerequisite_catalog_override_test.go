package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func antonPrerequisiteOverrideCatalog(t *testing.T, objective string) *dnfquest.Catalog {
	t.Helper()
	source := bridgePVFSource{dnfquest.DefaultList: "12830 `override.qst`\n", "n_quest/override.qst": "[name]\n`raid prerequisite`\n[grade]\n`[mission]`\n" + objective}
	idx, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), idx)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestRealPVFAntonReplacedPrerequisiteDoesNotBlockEitherPhaseResult(t *testing.T) {
	for _, phase := range []byte{0, 1} {
		for _, status := range []string{"absent", "active", "completed"} {
			t.Run(fmt.Sprintf("phase%d_%s", phase, status), func(t *testing.T) {
				var f *raidStartGuardFixture
				var sessions []*gameSession
				var now time.Time
				if phase == 0 {
					f, sessions, now = phase0RewardFlowWithGroups(t, []byte{1, 1})
				} else {
					f, _, now = actualFinalAntonWeeklyFixture(t)
					config, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
					if err != nil {
						t.Fatal(err)
					}
					f.state.Battle.AntonFlowConfig = config
					if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
						t.Fatal(err)
					}
					if _, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(errs) != 0 {
						t.Fatal(errs)
					}
					for i, id := range []uint16{19, 20} {
						p := f.session()
						p.selectedCharacterID = id
						p.accountID = []string{"account-1", "account-20"}[i]
						p.loginPacketSeen, p.accountFromToken = true, true
						f.service.bindGameSessionCharacter(p, id)
						sessions = append(sessions, p)
					}
				}
				f.service.questCatalog = antonPrerequisiteOverrideCatalog(t, "[type]\n`[seeking]`\n[sub type]\n-1\n[int data]\n3037 1\n[/int data]\n")
				before := map[string]repo.QuestRecord{}
				for _, p := range sessions {
					id := strconv.Itoa(int(p.selectedCharacterID))
					q := repo.QuestRecord{CharacterID: id, States: map[int64]repo.QuestState{}}
					if status != "absent" {
						q.States[12830] = repo.QuestState{Status: status, ProgressValue: 1, UpdatedAt: now.Add(-time.Hour), Extra: map[string]string{"quest_type": "[seeking]"}}
					}
					if err := f.repos.Quest.Save(context.Background(), q); err != nil {
						t.Fatal(err)
					}
					stored, found, err := f.repos.Quest.Load(context.Background(), id)
					if err != nil || !found {
						t.Fatal("seeded quest record unavailable", err)
					}
					before[id] = stored
				}
				state := cloneRuntimeRaidState(*f.state)
				for _, p := range sessions {
					f.conn.write.Reset()
					if err := f.service.sendCurrentAntonTerminalResult(p, state); err != nil {
						t.Fatalf("replacement objective blocked result for %d: %v", p.selectedCharacterID, err)
					}
					assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
					f.conn.write.Reset()
					if err := f.service.sendCurrentAntonTerminalResult(p, state); err != nil || f.conn.write.Len() != 0 {
						t.Fatal("result replay", err)
					}
					id := strconv.Itoa(int(p.selectedCharacterID))
					after, _, err := f.repos.Quest.Load(context.Background(), id)
					if err != nil || !reflect.DeepEqual(before[id], after) {
						t.Fatal("replacement objective mutated", id, err)
					}
				}
			})
		}
	}
}

func TestRealPVFAntonOriginalPrerequisiteStillUpdatesAfterResultGate(t *testing.T) {
	f, p, state, now := onlineAntonTerminalFixture(t)
	f.service.questCatalog = antonPrerequisiteOverrideCatalog(t, "[type]\n`[raid phase clear]`\n[sub type]\n0\n[int data]\n0 0 5 -1 1 0 5 -1\n[/int data]\n")
	seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
	if err := f.service.sendCurrentAntonTerminalResult(p, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("uncommitted result", err)
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(p, state); err != nil {
		t.Fatal(err)
	}
	q, _, err := f.repos.Quest.Load(context.Background(), "19")
	if err != nil || q.States[12830].ProgressValue != 5|4<<9 {
		t.Fatal("original counter", q, err)
	}
}
