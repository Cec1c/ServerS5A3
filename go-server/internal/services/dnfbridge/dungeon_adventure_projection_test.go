package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Counts actual serialized output, not calls to the projection helper. A large
// roster before a later op38 would still fail on a 1KiB TCP receive window even
// when all the application-level Write calls return immediately.
func assertAdventureAfterDeaths(t *testing.T, wire []byte, wantDeaths, wantPushes int, wantGrowth uint64) {
	t.Helper()
	deaths, pushes := 0, 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, wire) {
		if frame.Header.Classification != 0 {
			continue
		}
		switch frame.Header.MsgID {
		case 38:
			deaths++
		case currentAdventureInfoPushMsgID:
			pushes++
			if deaths < wantDeaths {
				t.Fatalf("完整冒险团仍挡住死亡通知：%d/%d", deaths, wantDeaths)
			}
			if got := binary.LittleEndian.Uint64(frame.Body[18+currentAdventureInfoGrowthCapsuleOffset:]); got != wantGrowth {
				t.Fatalf("冒险团投影经验=%d want=%d", got, wantGrowth)
			}
		}
	}
	if deaths != wantDeaths || pushes != wantPushes {
		t.Fatalf("死亡/冒险团通知=%d/%d want=%d/%d", deaths, pushes, wantDeaths, wantPushes)
	}
}

func TestMonsterAdventureProjectionCoalescesAcrossDeathBursts(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	c := p.conn.(*bufferConn)
	for i := 0; i < 3; i++ {
		// Three separately arrived groups must not each enqueue a 7KiB roster.
		if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[i:i+1], false); err != nil {
			t.Fatal(err)
		}
	}
	assertAdventureAfterDeaths(t, c.write.Bytes(), 3, 0, 0)
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || state.GrowthExperience != 30 || !p.dungeon.runtime.monsterAdventureInfoPending {
		t.Fatalf("延迟投影丢失已提交胶囊经验：%+v %v", state, err)
	}
	// Opening the panel mid-fight reads the committed account, not a stale
	// snapshot and not a predicted/uncommitted EXP value.
	c.write.Reset()
	if err := s.handleCurrentRequestAdventureInfo(p, []byte{99, 0}); err != nil {
		t.Fatal(err)
	}
	frames := splitAllCurrentGameServerUpperPackets(t, c.write.Bytes())
	if len(frames) != 1 || frames[0].Header.MsgID != 1401 || frames[0].Body[0] != 1 ||
		binary.LittleEndian.Uint64(frames[0].Body[1+18+currentAdventureInfoGrowthCapsuleOffset:]) != 30 {
		t.Fatal("战斗中主动查询未得到已提交的最新经验")
	}
	c.write.Reset()
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[3:], false); err != nil {
		t.Fatal(err)
	}
	assertAdventureAfterDeaths(t, c.write.Bytes(), 2, 1, 50)
	if p.dungeon.runtime.monsterAdventureInfoPending {
		t.Fatal("清房后仍有待发送投影")
	}
	c.write.Reset()
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	// Replays can resend native op38, but cannot add EXP or another roster.
	for _, f := range splitAllCurrentGameServerUpperPackets(t, c.write.Bytes()) {
		if f.Header.MsgID == currentAdventureInfoPushMsgID {
			t.Fatal("重复死亡重新发送完整冒险团")
		}
	}
	account, _, _ = repos.Account.Load(context.Background(), "account-1")
	state, err = adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || state.GrowthExperience != 50 {
		t.Fatalf("重复报告改变经验：%+v %v", state, err)
	}
}

func TestMonsterAdventureProjectionWaitsForBossForcedVisualDeaths(t *testing.T) {
	for _, burst := range []bool{false, true} {
		t.Run(strconv.FormatBool(burst), func(t *testing.T) {
			s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
			prepareDeathBurstCapsule(t, s, repos)
			p.dungeon.runtime.Room.monsters[0].Spawn.Rank = "[boss]"
			var err error
			if burst {
				err = s.handleCurrentDungeonDeathBurstPackets(p, packets[:1], false)
			} else {
				packet, parseErr := dnfproto.ParseLegacyGamePacket(packets[0].Data)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				err = s.handleDungeonMonsterDeath(p, packet.Body)
			}
			if err != nil {
				t.Fatal(err)
			}
			// One actual boss reward; four forced visual deaths earn no EXP.
			assertAdventureAfterDeaths(t, p.conn.(*bufferConn).write.Bytes(), 5, 1, 10)
		})
	}
}

type failedAdventureProjectionConn struct {
	*bufferConn
	fail bool
}

func (c *failedAdventureProjectionConn) Write(b []byte) (int, error) {
	if c.fail && len(b) >= 3 && b[0] == 0 && binary.LittleEndian.Uint16(b[1:3]) == currentAdventureInfoPushMsgID {
		return 0, io.ErrClosedPipe
	}
	return c.bufferConn.Write(b)
}

func TestMonsterAdventureProjectionWriteFailureRetainsCommittedState(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	r := p.dungeon.runtime
	c := &failedAdventureProjectionConn{bufferConn: p.conn.(*bufferConn), fail: true}
	p.conn = c
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("未报告投影写失败：%v", err)
	}
	if !r.monsterAdventureInfoPending {
		t.Fatal("写失败丢弃了待刷新状态")
	}
	c.fail = false
	c.write.Reset()
	if err := s.flushCurrentMonsterAdventureInfo(p, r); err != nil {
		t.Fatal(err)
	}
	assertAdventureAfterDeaths(t, c.write.Bytes(), 0, 1, 50)
}

func TestMonsterAdventureProjectionRejectsStaleRuntimeAndRevokedLogin(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	r := p.dungeon.runtime
	c := p.conn.(*bufferConn)
	for _, mode := range []string{"runtime", "character", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			c.write.Reset()
			r.monsterAdventureInfoPending = true
			p.dungeon.runtime, p.selectedCharacterID = r, 99
			p.loginRevoked.Store(false)
			switch mode {
			case "runtime":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "character":
				p.selectedCharacterID = 100
			case "revoked":
				p.loginRevoked.Store(true)
			}
			if err := s.flushCurrentMonsterAdventureInfo(p, r); err != nil || c.write.Len() != 0 || !r.monsterAdventureInfoPending {
				t.Fatalf("旧场景投影泄漏或被消费：%v", err)
			}
		})
	}
}

func TestMonsterAdventureProjectionPartyMembersKeepExperienceAndDeathOrder(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(strconv.FormatBool(queued), func(t *testing.T) {
			testMonsterAdventureProjectionPartyMembers(t, queued)
		})
	}
}

func testMonsterAdventureProjectionPartyMembers(t *testing.T, queued bool) {
	s, leader, member := partyMonsterExperienceFixture(t)
	s.options.currentClientVersion = "official-24.9"
	repos, _ := s.repositoryGroup()
	prepareDeathBurstCapsule(t, s, repos)
	var err error
	s.honorTable, err = dnfhonor.LoadTables(honorServiceTestSource{dnfhonor.TablePath: honorServiceTestDocument()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*gameSession{leader, member} {
		id := strconv.Itoa(int(p.selectedCharacterID))
		c, _, _ := repos.Character.Load(context.Background(), id)
		c.Level = 90
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		p.dungeon.runtime.Character = c
		skill, _, _ := repos.Skill.Load(context.Background(), id)
		skill.Points.SyncedLevel = 90
		if err := repos.Skill.Save(context.Background(), skill); err != nil {
			t.Fatal(err)
		}
	}
	resources, err := s.currentDungeonExperienceCommitResources()
	if err != nil {
		t.Fatal(err)
	}
	award, err := currentDungeonMonsterExperienceAwardFor(leader.dungeon.runtime, leader.dungeon.runtime.Room.Snapshot().Monsters[0], resources.Sources)
	if err != nil || award.Gain == 0 {
		t.Fatalf("组队夹具没有有效经验：%+v %v", award, err)
	}
	var release func()
	if queued {
		// Hold the actual recipient owner while deaths and EXP callbacks are
		// queued. A later mailbox wakeup must deliver every death before the
		// final roster, even when the EXP callbacks ran between death frames.
		release = holdPartyDeathRecipient(t, s, member, nil)
	}
	for i, m := range leader.dungeon.runtime.Room.Snapshot().Monsters {
		body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
		binary.LittleEndian.PutUint32(body, m.ObjectKey)
		binary.LittleEndian.PutUint16(body[4:], leader.selectedCharacterID)
		body[30] = 1
		if err := s.handleDungeonMonsterDeath(leader, body); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for _, p := range []*gameSession{leader, member} {
				if queued && p == member {
					continue
				}
				assertAdventureAfterDeaths(t, p.conn.(*bufferConn).write.Bytes(), 1, 0, 0)
			}
		}
	}
	if queued {
		release()
		drainPartyDeathRecipient(t, s, member)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for {
			pending := true
			if err := s.callGameSession(ctx, member, "test-adventure-projection-drained", func() error {
				pending = member.dungeon.runtime.monsterAdventureInfoPending
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !pending {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("队友清房后未刷新冒险团")
			case <-time.After(5 * time.Millisecond):
			}
		}
		// Stop the owner before inspecting its buffer/runtime below.
		stopTestGameSessionEvents(t, s, member)
	}
	// Both characters share this fixture account; each accepted kill keeps the
	// original domain award for both recipients, with no extra replay award.
	for _, p := range []*gameSession{leader, member} {
		wire := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
		deaths, pushes := 0, 0
		for _, frame := range wire {
			if frame.Header.MsgID == 38 {
				deaths++
			}
			if frame.Header.MsgID == currentAdventureInfoPushMsgID {
				pushes++
				if deaths != 2 {
					t.Fatal("队友完整冒险团先于死亡通知发送")
				}
			}
		}
		if deaths != 2 || pushes == 0 || p.dungeon.runtime.monsterAdventureInfoPending {
			t.Fatalf("队员%d清房同步不完整：deaths=%d pushes=%d", p.selectedCharacterID, deaths, pushes)
		}
		character, _, err := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil || character.Stats[currentHonorExpertProgressExperienceStatKey] != int64(award.Gain)*2 {
			t.Fatalf("队员%d经验改变：%+v %v", p.selectedCharacterID, character, err)
		}
	}
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || state.GrowthExperience != uint64(award.Gain)*4 {
		t.Fatalf("共享账号经验被丢弃或重复累计：%+v %v", state, err)
	}
}

type rejectedAdventureSettlement struct{}

func (rejectedAdventureSettlement) WithinCharacterSettlement(context.Context, string, func(dnfrepo.Group) error) error {
	return errors.New("拒绝未提交账号经验")
}

func TestMonsterAdventureProjectionDoesNotPublishFailedAccountTransaction(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	repos.CharacterSettlement = rejectedAdventureSettlement{}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	assertAdventureAfterDeaths(t, p.conn.(*bufferConn).write.Bytes(), 5, 0, 0)
	if p.dungeon.runtime.monsterAdventureInfoPending {
		t.Fatal("失败事务标记了待刷新冒险团")
	}
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || state.GrowthExperience != 0 {
		t.Fatalf("失败事务改变账号经验：%+v %v", state, err)
	}
}

func TestMonsterAdventureProjectionKeepsOtherProfilesAndModesImmediate(t *testing.T) {
	for _, mode := range []string{"legacy", "raid", "practice"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, _ := prepareDungeonDeathBurstTest(t, true)
			prepareDeathBurstCapsule(t, s, repos)
			r := p.dungeon.runtime
			switch mode {
			case "legacy":
				s.options.currentClientVersion = ""
			case "raid":
				r.raidEntry = &currentRaidDungeonEntry{}
			case "practice":
				r.guildPractice = &currentGuildPracticeRun{}
			}
			if err := s.sendCurrentMonsterAdventureInfo(p, r, "compatibility_test"); err != nil {
				t.Fatal(err)
			}
			assertAdventureAfterDeaths(t, p.conn.(*bufferConn).write.Bytes(), 0, 1, 0)
			if r.monsterAdventureInfoPending {
				t.Fatal("其他档位或模式进入普通房间延迟投影")
			}
		})
	}
}
