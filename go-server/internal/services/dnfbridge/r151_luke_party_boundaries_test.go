package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func r151ObjectBody(t *testing.T, p *gameSession) []byte {
	t.Helper()
	r := p.dungeon.runtime
	sc, _ := r.Session.Scene()
	keys := keysForR151Destroy(r, sc)
	if len(keys) != 1 {
		t.Fatalf("expected one actual destroy condition, got %v", keys)
	}
	b := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(b, keys[0])
	binary.LittleEndian.PutUint16(b[4:], ^uint16(0))
	return b
}
func r151Result(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	b := make([]byte, currentDungeonPlayResultBaseSize)
	b[currentDungeonPlayResultClientRankPointOffset] = 73
	r151Send(t, s, p, 46, b)
	r151Send(t, s, p, 123, make([]byte, currentDungeonCharacterStatisticBodySize))
	if !s.currentRaidCompletedMapTownReturnReady(p, p.dungeon.runtime) {
		e := s.produceCurrentDungeonSettlementPlanLocked(context.Background(), p, p.dungeon.runtime)
		t.Fatalf("real personal plan/receipt not ready: %v", e)
	}
}
func r151NoReward(t *testing.T, p *gameSession) {
	t.Helper()
	r := p.dungeon.runtime
	if r == nil || r.settlementResultPlan != nil || r.settlementClearRewardSent || r.settlementPlayResultReceived || r.settlementStatisticReceived {
		t.Fatal("completion forged personal result or reward")
	}
}
func TestR151LukeSelfReportControl(t *testing.T) {
	t.Setenv("R151_SELF_REPORT_CONTROL", "1")
	TestR151LukeEnergyPartyReturn(t)
}

func TestR151PublicOrdinaryDispatcherKeepsBaselineFailureBehavior(t *testing.T) {
	s, leader, p, lr, pr := partyDungeonDeathFixture(t)
	fail := &r151PublicFailOp31Conn{remaining: 2}
	p.conn = fail
	// This existing public fixture uses the legacy variable-free 62B profile.
	key := lr.Room.Snapshot().Monsters[0].ObjectKey
	b := make([]byte, 62)
	binary.LittleEndian.PutUint32(b, key)
	binary.LittleEndian.PutUint16(b[4:], leader.selectedCharacterID)
	if e := s.handleDungeonMonsterDeath(leader, b); e != nil {
		t.Fatal(e)
	}
	if fail.failures != 2 || !pr.bossDieCheckAccepted || pr.settlementEntrySent {
		t.Fatal("ordinary upstream retry did not preserve the failed entry state")
	}
	s.dispatchPartyDungeonPeerFinalRoomCompletion(p, pr.partyInstance.id, key)
	s.dispatchPartyDungeonPeerFinalRoomCompletion(p, pr.partyInstance.id, key)
	if fail.failures != 2 || !pr.settlementEntrySent ||
		r151PublicCountMsg(splitUpperFrames(t, fail.write.Bytes()), 31) != 1 {
		t.Fatal("ordinary upstream retry did not recover exactly one personal entry")
	}
	r151NoReward(t, p)
}

func TestR151LukeCompletionBoundaries(t *testing.T) {
	for _, mode := range []string{"unreported_result", "op31_twice_then_replay", "left_party", "stale_generation", "wrong_run", "wrong_owner", "missing_exp_diagnosis", "save_failure", "commit_failure"} {
		t.Run(mode, func(t *testing.T) {
			f, ps := r151Fixture(t, 5063)
			s := f.service
			p := ps[1]
			r := p.dungeon.runtime
			var failing *r151PublicFailOp31Conn
			r151ClearToResult(t, f, ps, func(phase string) bool {
				if phase == "before_object" {
					switch mode {
					case "op31_twice_then_replay":
						failing = &r151PublicFailOp31Conn{remaining: 2}
						p.conn = failing
					case "left_party":
						r151Send(t, s, p, 13, nil)
					case "stale_generation":
						p.characterGeneration++ // queued old membership must not apply to new generation
					case "wrong_run":
						e := *r.raidEntry
						e.RunID += "-foreign"
						r.raidEntry = &e
					case "wrong_owner":
						r.Character.CharacterID = "999"
					}
					return false
				}
				return phase == "after_object"
			})
			switch mode {
			case "left_party", "stale_generation", "wrong_run", "wrong_owner":
				if r.settlementEntrySent || r.bossDieCheckAccepted || r.settlementResultPlan != nil {
					t.Fatal("foreign/departed owner received completion")
				}
				return
			case "op31_twice_then_replay":
				// Public master has no R133 immediate retry. Exercise a repeated
				// accepted destroy through the full handler before checking recovery.
				if failing.failures == 1 {
					r151Send(t, s, ps[0], 39, r151ObjectBody(t, ps[0]))
				}
				if failing.failures != 2 || r.settlementEntrySent || !r.bossDieCheckAccepted {
					t.Fatalf("failed-op31 intermediate failures=%d sent=%t accepted=%t", failing.failures, r.settlementEntrySent, r.bossDieCheckAccepted)
				}
				r151Send(t, s, ps[0], 39, r151ObjectBody(t, ps[0]))
				r151Send(t, s, ps[0], 39, r151ObjectBody(t, ps[0]))
				if !r.settlementEntrySent || r151PublicCountMsg(splitUpperFrames(t, failing.write.Bytes()), 31) != 1 {
					t.Fatal("replayed destroy did not resume exactly one personal op31")
				}
			}
			if !r.settlementEntrySent {
				t.Fatal("no personal entry")
			}
			r151NoReward(t, p)
			if mode == "missing_exp_diagnosis" {
				c, _, e := f.repos.Character.Load(context.Background(), "20")
				if e != nil {
					t.Fatal(e)
				}
				delete(c.Stats, "exp")
				if e = f.repos.Character.Save(context.Background(), c); e != nil {
					t.Fatal(e)
				}
				b := make([]byte, currentDungeonPlayResultBaseSize)
				r151Send(t, s, p, 46, b)
				e = s.produceCurrentDungeonSettlementPlanLocked(context.Background(), p, r)
				t.Logf("direct plan error for missing exp: %v", e)
				if !errors.Is(e, dnfdungeon.ErrSettlementStateInvalid) || r.settlementResultPlan != nil || r.settlementClearRewardSent {
					t.Fatal("missing exp did not reject personal asset owner")
				}
				return
			}
			r151Result(t, s, ps[0])
			if mode == "unreported_result" {
				r151Send(t, s, ps[0], 72, []byte{1, 2})
				if ps[0].dungeon.runtime != nil || p.dungeon.runtime != r || r.townReturnOp24Sent {
					t.Fatal("leader return crossed member result barrier")
				}
				r151NoReward(t, p)
				r151Result(t, s, p)
				r151Send(t, s, p, 72, []byte{1, 2})
				if p.dungeon.runtime != nil {
					t.Fatal("late personal result cannot return independently")
				}
				return
			}
			if mode == "save_failure" || mode == "commit_failure" {
				before, _, _ := f.repos.Character.Load(context.Background(), "20")
				beforeSkill, _, _ := f.repos.Skill.Load(context.Background(), "20")
				beforeInv, _, _ := f.repos.Inventory.Load(context.Background(), "20")
				fault := &r151SettlementFailure{inner: f.repos.CharacterSettlement, mode: mode, enabled: true}
				repos := f.repos
				repos.CharacterSettlement = fault
				s.repositoryProvider = func() (repo.Group, bool) { return repos, true }
				b := make([]byte, currentDungeonPlayResultBaseSize)
				b[currentDungeonPlayResultClientRankPointOffset] = 73
				r151Send(t, s, p, 46, b)
				r151Send(t, s, p, 123, make([]byte, currentDungeonCharacterStatisticBodySize))
				if r.settlementResultPlan != nil || r.settlementClearRewardSent || fault.hits == 0 {
					t.Fatal("failed transaction crossed reward barrier")
				}
				after, _, _ := f.repos.Character.Load(context.Background(), "20")
				afterSkill, _, _ := f.repos.Skill.Load(context.Background(), "20")
				afterInv, _, _ := f.repos.Inventory.Load(context.Background(), "20")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeSkill, afterSkill) || !reflect.DeepEqual(beforeInv, afterInv) {
					t.Fatal("settlement failure did not roll back all assets")
				}
				r151Send(t, s, ps[0], 72, []byte{1, 2})
				if p.dungeon.runtime != r || r.townReturnOp24Sent {
					t.Fatal("failed assets were dragged to town")
				}
				fault.enabled = false
				r151Result(t, s, p)
				r151Send(t, s, p, 72, []byte{1, 2})
				if p.dungeon.runtime != nil {
					t.Fatal("recovered settlement cannot return")
				}
				return
			}
			r151Result(t, s, p)
			before, _, _ := f.repos.Character.Load(context.Background(), "20")
			r151Send(t, s, ps[0], 39, r151ObjectBody(t, ps[0]))
			r151Result(t, s, p)
			after, _, _ := f.repos.Character.Load(context.Background(), "20")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("destroy/result replay mutated committed assets")
			}
			r151Send(t, s, ps[0], 72, []byte{1, 2})
			if p.dungeon.runtime != nil {
				t.Fatal("ready replay peer stranded")
			}
		})
	}
}

type r151SettlementFailure struct {
	inner   repo.CharacterSettlementUnitOfWork
	mode    string
	enabled bool
	hits    int
}

func (f *r151SettlementFailure) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	return f.inner.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		if !f.enabled || id != "20" {
			return apply(tx)
		}
		if f.mode == "save_failure" {
			tx.Character = r151FailCharacter{CharacterRepository: tx.Character, f: f}
		}
		if e := apply(tx); e != nil {
			return e
		}
		f.hits++
		return fmt.Errorf("r151 final transaction rollback")
	})
}

type r151FailCharacter struct {
	repo.CharacterRepository
	f *r151SettlementFailure
}

func (r r151FailCharacter) Save(ctx context.Context, c repo.CharacterRecord) error {
	r.f.hits++
	return errors.New("r151 character save rollback")
}

type r151PublicFailOp31Conn struct {
	bufferConn
	remaining int
	failures  int
}

func (connection *r151PublicFailOp31Conn) Write(data []byte) (int, error) {
	if connection.remaining > 0 {
		rest := data
		for len(rest) >= dnfproto.GameServerUpperHeaderSize {
			total := int(binary.LittleEndian.Uint32(rest[3:7]))
			if total < dnfproto.GameServerUpperHeaderSize || total > len(rest) {
				break
			}
			packet, err := dnfproto.ParseChannelPacket(rest[:total])
			if err == nil && packet.Header.MsgID == currentDungeonSettlementEntryMsgID {
				connection.remaining--
				connection.failures++
				return 0, errors.New("injected member op31 write failure")
			}
			rest = rest[total:]
		}
	}
	return connection.bufferConn.Write(data)
}

func r151PublicCountMsg(frames []upperFrame, msgID uint16) int {
	count := 0
	for _, frame := range frames {
		if frame.msgID == msgID {
			count++
		}
	}
	return count
}
