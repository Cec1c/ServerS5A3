package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"testing"

	dnfcharstat "longheng.io/server/internal/modules/dnf/charstat"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func partyMonsterExperienceFixture(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	s, leader, member, left, right := partyDungeonDeathFixture(t, 2, func(s *Service, leader, _ *gameSession) {
		if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
			t.Fatal("party bootstrap")
		}
	})
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	installCurrentDungeonMonsterExperienceTestPVF(source)
	s.characterStats, err = dnfcharstat.Load(context.Background(), source, dnfcharstat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	repositories, _ := s.repositoryGroup()
	for i, p := range []*gameSession{leader, member} {
		id := strconv.Itoa(int(p.selectedCharacterID))
		c, _, _ := repositories.Character.Load(context.Background(), id)
		c.Job = "0"
		c.Stats["exp"] = 0
		if err := repositories.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Skill.Save(context.Background(), repo.SkillRecord{CharacterID: id, Points: repo.SkillPointState{TotalSP: 20, RemainingSP: 20, SyncedLevel: c.Level}}); err != nil {
			t.Fatal(err)
		}
		[]*runtimeDungeonState{left, right}[i].Character = repo.CloneCharacter(c)
		p.conn.(*bufferConn).write.Reset()
	}
	return s, leader, member
}

func TestPartyMonsterExperienceCreditsBothMembersRegardlessOfKiller(t *testing.T) {
	for _, killerID := range []uint16{99, 100} {
		t.Run(strconv.Itoa(int(killerID)), func(t *testing.T) {
			s, leader, member := partyMonsterExperienceFixture(t)
			killer := leader
			if killerID == 100 {
				killer = member
			}
			runtime := killer.dungeon.runtime
			monster := runtime.Room.Snapshot().Monsters[0]
			resources, err := s.currentDungeonExperienceCommitResources()
			if err != nil {
				t.Fatal(err)
			}
			award, err := currentDungeonMonsterExperienceAwardFor(runtime, monster, resources.Sources)
			if err != nil || award.Gain == 0 {
				t.Fatalf("fixture gain=%d err=%v", award.Gain, err)
			}
			request := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(request, monster.ObjectKey)
			binary.LittleEndian.PutUint16(request[4:], killerID)
			if err := s.handleDungeonMonsterDeath(killer, request); err != nil {
				t.Fatal(err)
			}
			repositories, _ := s.repositoryGroup()
			for _, p := range []*gameSession{leader, member} {
				c, _, _ := repositories.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
				if c.Stats["exp"] != int64(award.Gain) || p.dungeon.runtime.Character.Stats["exp"] != int64(award.Gain) {
					t.Fatalf("killer=%d recipient=%d persisted/runtime exp=%d/%d, want %d", killerID, p.selectedCharacterID, c.Stats["exp"], p.dungeon.runtime.Character.Stats["exp"], award.Gain)
				}
				found := false
				for _, frame := range splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes()) {
					found = found || frame.msgID == currentDungeonCharacterStateMsgID
				}
				if !found {
					t.Fatalf("recipient %d received no experience state", p.selectedCharacterID)
				}
			}
			for _, p := range []*gameSession{leader, member, killer} {
				binary.LittleEndian.PutUint16(request[4:], p.selectedCharacterID)
				if err := s.handleDungeonMonsterDeath(p, request); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"99", "100"} {
				c, _, _ := repositories.Character.Load(context.Background(), id)
				if c.Stats["exp"] != int64(award.Gain) {
					t.Fatalf("duplicate death re-awarded recipient %s", id)
				}
			}
		})
	}
}

func TestPartyMonsterExperienceSkipsIneligiblePeers(t *testing.T) {
	for _, reason := range []string{"left_party", "other_room", "dead", "returning"} {
		t.Run(reason, func(t *testing.T) {
			s, leader, member := partyMonsterExperienceFixture(t)
			switch reason {
			case "left_party":
				s.runtimePartyManagerForService().Leave(member.selectedCharacterID, member.characterGeneration)
			case "other_room":
				member.dungeon.runtime.Room = &runtimeDungeonRoom{}
			case "dead":
				markPartyDungeonLife(member.dungeon.runtime, member.selectedCharacterID, true)
			case "returning":
				member.dungeon.runtime.townReturnPending = true
			}
			monster := leader.dungeon.runtime.Room.Snapshot().Monsters[0]
			request := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(request, monster.ObjectKey)
			binary.LittleEndian.PutUint16(request[4:], leader.selectedCharacterID)
			if err := s.handleDungeonMonsterDeath(leader, request); err != nil {
				t.Fatal(err)
			}
			repositories, _ := s.repositoryGroup()
			c, _, _ := repositories.Character.Load(context.Background(), "100")
			if c.Stats["exp"] != 0 || member.dungeon.runtime.Character.Stats["exp"] != 0 {
				t.Fatal("ineligible peer was awarded kill experience")
			}
		})
	}
}

func TestPartyMonsterExperienceReceiptSurvivesFailedNotification(t *testing.T) {
	s, leader, member := partyMonsterExperienceFixture(t)
	member.conn = &failNthDungeonWriteConn{failAt: 1, err: fmt.Errorf("injected peer write failure")}
	runtime := leader.dungeon.runtime
	monster := runtime.Room.Snapshot().Monsters[0]
	request := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(request, monster.ObjectKey)
	binary.LittleEndian.PutUint16(request[4:], leader.selectedCharacterID)
	if err := s.handleDungeonMonsterDeath(leader, request); err != nil {
		t.Fatal(err)
	}
	repositories, _ := s.repositoryGroup()
	before, _, _ := repositories.Character.Load(context.Background(), "100")
	if before.Stats["exp"] == 0 {
		t.Fatal("failed notification rolled back committed experience")
	}
	member.conn = &bufferConn{}
	s.dispatchCurrentDungeonMonsterExperienceToPeersLocked(leader, runtime, monster)
	after, _, _ := repositories.Character.Load(context.Background(), "100")
	if before.Stats["exp"] != after.Stats["exp"] {
		t.Fatal("notification retry awarded experience twice")
	}
}
