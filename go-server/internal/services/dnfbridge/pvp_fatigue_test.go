package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

var errTestPVFTextMissing = errors.New("test PVF text missing")

type pvpFatigueTestSource map[string]string

func (s pvpFatigueTestSource) ReadText(path string) (string, error) {
	text, ok := s[path]
	if !ok {
		return "", errTestPVFTextMissing
	}
	return text, nil
}

func TestCurrentPVPFatigueRulesRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := loadCurrentPVPFatigueRulesFromSource(archive)
	if err != nil {
		t.Fatal(err)
	}
	if rules.ArcadeUse != 30 || rules.ArcadeDifficulty[0] != 0 || rules.ArcadeDifficulty[1] != 0 || rules.ArcadeDifficulty[2] != 0 || rules.ColosseumUse != 10 || len(rules.Standards) != 41 {
		t.Fatalf("rules=%+v", rules)
	}
	s := pvpRoomTestService(t)
	s.initialEquipmentArchive = archive
	s.pvpFatigueRules = rules
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{50, 89, 90} {
		entry, err := tables.ThresholdToNext(level - 1)
		if err != nil {
			t.Fatal(err)
		}
		if level == 89 {
			entry, err = tables.ThresholdToNext(89)
			entry--
		}
		c := dnfrepo.CharacterRecord{CharacterID: "29", AccountID: "pvp-29", Job: "0", Level: level, Stats: map[string]int64{"exp": int64(entry), "gold": 10}}
		skill := dnfrepo.SkillRecord{CharacterID: "29", Points: dnfrepo.SkillPointState{SyncedLevel: level, TotalSP: 100, RemainingSP: 50}}
		r, err := s.planCurrentPVPArcadeReward(context.Background(), rules, c, skill)
		if err != nil {
			t.Fatal(err)
		}
		want := rules.arcadeReward(level)
		if r.Reward.Experience != want.Experience || r.Reward.Gold != want.Gold || r.Character.Stats["gold"] != 10+int64(want.Gold) {
			t.Fatalf("level %d reward %+v", level, r)
		}
		if level == 50 && r.Character.Stats["exp"] <= int64(entry) {
			t.Fatal("ordinary EXP did not increase")
		}
		if level == 89 && (r.Character.Level != 90 || r.Skill.Points.SyncedLevel != 90 || r.Skill.Points.TotalSP <= 100) {
			t.Fatal("level and SP did not advance together")
		}
		if level == 90 && r.Character.Stats[currentHonorExpertProgressExperienceStatKey] == 0 {
			t.Fatal("level-cap EXP lost")
		}
	}
}

func TestCurrentPVPFatigueRulesReadPVFAndBuildNativeBodies(t *testing.T) {
	source := pvpFatigueTestSource{"etc/pvpfatigueinfo.etc": "" +
		"[arcadeMode]\n" +
		"[fatigue accelerate use]\n30\n[difficulty]\n0 40 1 45 2 50\n[/difficulty]\n[/arcadeMode]\n" +
		"[colosseum]\n[fatigue accelerate use]\n10\n[/colosseum]\n" +
		"[exp reward percent]\n90\n[gold reward percent]\n90\n" +
		"[fatigue and reward standard value]\n50 47972 382 90 230577 1705\n" +
		"[exp calc const]\n2\n[gold calc const]\n2\n"}
	rules, err := loadCurrentPVPFatigueRulesFromSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if rules.ArcadeUse != 30 || rules.ColosseumUse != 10 || rules.ArcadeDifficulty[0] != 40 || rules.ArcadeDifficulty[2] != 50 {
		t.Fatalf("rules=%+v", rules)
	}
	if reward := rules.arcadeReward(90); reward.Experience != 115288 || reward.Gold != 852 {
		t.Fatalf("reward=%+v", reward)
	}
	if got := buildCurrentPVPFatigueUseBody(true); len(got) != 1 || got[0] != 1 {
		t.Fatalf("usage body=%x", got)
	}
	if got := buildCurrentPVPFatigueRewardBody(true, 115288, 852); len(got) != 9 || got[0] != 1 {
		t.Fatalf("reward body=%x", got)
	}
}

func TestCurrentPVPArcadeRewardReceiptIsPerMatch(t *testing.T) {
	first := time.Unix(100, 1)
	second := time.Unix(100, 2)
	room := &currentPVPRoom{id: 7, arcade: &currentPVPArcadeRun{startedAt: first}}
	if got, want := currentPVPArcadeRewardReceipt(room), "pvp_arcade_reward_7_100000000001"; got != want {
		t.Fatalf("receipt=%q, want %q", got, want)
	}
	room.arcade.startedAt = second
	if got := currentPVPArcadeRewardReceipt(room); got == "pvp_arcade_reward_7_100000000001" {
		t.Fatal("different match reused reward receipt")
	}
}

func TestCurrentPVPFatigueAccelerationAndRoundInfo(t *testing.T) {
	_, _, ok := parseCurrentPVPArcadeRoundInfo(make([]byte, 18))
	if ok {
		t.Fatal("short round packet accepted")
	}
	body := make([]byte, 19)
	body[5], body[6] = 2, 1
	if end, won, ok := parseCurrentPVPArcadeRoundInfo(body); !ok || end || won {
		t.Fatalf("round start parsed as end=%v won=%v ok=%v", end, won, ok)
	}
	body[0], body[12] = 1, 1
	if end, won, ok := parseCurrentPVPArcadeRoundInfo(body); !ok || !end || !won {
		t.Fatalf("round end parsed as end=%v won=%v ok=%v", end, won, ok)
	}
}

func TestPVPFatigueNativeRequestsAndPaymentRetry(t *testing.T) {
	for _, upper := range []bool{false, true} {
		s := pvpRoomTestService(t)
		conn := &pvpFailNthConn{}
		session := &gameSession{conn: conn, accountID: "pvp-29", selectedCharacterID: 29}
		room := &currentPVPRoom{id: 7, battleMode: 10, request: currentPVPMakeRoomRequest{dojoDifficulty: 1}}
		room.members[0] = &currentPVPMember{session: session, characterID: 29, level: 90}
		session.pvpRoom = room
		s.pvpFatigueRules = &currentPVPFatigueRules{ArcadeDifficulty: map[byte]int64{0: 40}}
		repos, _ := s.repositoryGroup()
		ctx := context.Background()
		character, _, _ := repos.Character.Load(ctx, "29")
		character.Stats["fatigue"], character.Stats["fatigue_limit"] = 188, 188
		account, _, _ := repos.Account.Load(ctx, character.AccountID)
		premium.Upsert(&account, 17, 86400, 1, s.gameplayNow())
		if err := repos.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		quests := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{}}
		for id := int64(2670); id <= 2674; id++ {
			quests.States[id] = dnfrepo.QuestState{Status: "completed"}
		}
		if err := repos.Quest.Save(ctx, quests); err != nil {
			t.Fatal(err)
		}
		send := func(op uint16, body []byte) error {
			if !upper {
				return s.handleGameCommand(session, 1, op, body)
			}
			packet, err := dnfproto.BuildChannelPacket(op, body, 0, 1)
			if err != nil {
				return err
			}
			return s.handleGameUpper(session, packet)
		}
		// 注：参考实现此处先跑一遍 op562（疲劳值加速勾选框）的 ACK + fatigue_acceleration
		// 持久化断言。op562 需要 fatigue_burn / returnee-lucky 整套非决斗场链路，本次
		// "仅决斗场"移植刻意没有注册该 opcode，所以这一段一并去掉，只验证街机付费链路。
		conn.write.Reset()
		conn.failAt = conn.writes + 1
		if err := send(1271, []byte{1, 1}); err == nil {
			t.Fatal("missing injected payment publication error")
		}
		stored, _, _ := repos.Character.Load(ctx, "29")
		if stored.Stats["fatigue"] != 148 || !room.arcade.paid {
			t.Fatal("payment did not commit before network failure")
		}
		if err := send(1271, []byte{1, 1}); err != nil {
			t.Fatal(err)
		}
		stored, _, _ = repos.Character.Load(ctx, "29")
		if stored.Stats["fatigue"] != 148 {
			t.Fatal("retry charged fatigue twice")
		}
		state, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
		if state.Header.MsgID != 36 || state.Header.Classification != 0 || binary.LittleEndian.Uint16(state.Body) != 40 || ack.Header.MsgID != 1271 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
			t.Fatal("fatigue HUD must precede native usage ACK")
		}
		start := []byte{0, 0x5b, 0x66, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		if err := send(1285, start); err != nil {
			t.Fatal(err)
		}
		end := append([]byte(nil), start...)
		end[0], end[12] = 1, 1
		if err := send(1285, end); err != nil {
			t.Fatal(err)
		}
		conn.write.Reset()
		if err := send(1272, []byte{1, 1, 1}); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 || room.arcade.reward != nil {
			t.Fatal("first round received full-run reward")
		}
		start[6], end[6] = 2, 2
		if err := send(1285, start); err != nil {
			t.Fatal(err)
		}
		// Preseed a committed receipt snapshot to isolate wire retries from PVF I/O.
		room.arcade.reward = &dnfpvp.ArcadeRewardSnapshot{Character: stored, Reward: dnfpvp.ArcadeReward{Experience: 100, Gold: 20}}
		if err := send(1285, end); err != nil {
			t.Fatal(err)
		}
		if !room.arcade.roundEnded || room.arcade.round != 2 {
			t.Fatal("last round was not tracked")
		}
		conn.write.Reset()
		if err := send(1272, []byte{1, 0, 1}); err != nil {
			t.Fatal(err)
		}
		ack, _ = splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0}) {
			t.Fatal("contradictory result accepted")
		}
	}
}

func TestPVPFatigueTutorialAndInvalidUsageDoNotCharge(t *testing.T) {
	for _, test := range []string{"tutorial", "disabled", "short_balance", "wrong_mode", "wrong_difficulty"} {
		t.Run(test, func(t *testing.T) {
			s := pvpRoomTestService(t)
			session := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29}
			room := &currentPVPRoom{id: 1, battleMode: 10, request: currentPVPMakeRoomRequest{dojoDifficulty: 1}}
			room.members[0] = &currentPVPMember{session: session, characterID: 29}
			session.pvpRoom = room
			s.pvpFatigueRules = &currentPVPFatigueRules{ArcadeDifficulty: map[byte]int64{0: 40}}
			repos, _ := s.repositoryGroup()
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			c.Stats["fatigue"] = 188
			if test == "short_balance" {
				c.Stats["fatigue"] = 39
			}
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			q := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{}}
			for id := int64(2670); id <= 2674; id++ {
				q.States[id] = dnfrepo.QuestState{Status: "completed"}
			}
			if test == "tutorial" {
				q.States[2674] = dnfrepo.QuestState{Status: "active", ProgressValue: 262657}
			}
			if err := repos.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
			body := []byte{1, 1}
			if test == "wrong_mode" {
				body = []byte{2, 0}
			}
			if test == "wrong_difficulty" {
				body = []byte{1, 2}
			}
			if test == "disabled" {
				start := make([]byte, 19)
				start[5], start[6] = 2, 1
				if err := s.handleCurrentPVPArcadeRoundInfo(session, start); err != nil {
					t.Fatal(err)
				}
			} else if err := s.handleCurrentPVPFatigueUsage(session, body); err != nil {
				t.Fatal(err)
			}
			got, _, _ := repos.Character.Load(ctx, "29")
			if got.Stats["fatigue"] != c.Stats["fatigue"] || (room.arcade != nil && room.arcade.paid) {
				t.Fatal("ineligible run charged fatigue")
			}
		})
	}
}
