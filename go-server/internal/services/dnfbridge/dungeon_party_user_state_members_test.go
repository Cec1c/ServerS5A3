package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/platform/config"
)

// 本文件钉住 DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE（副本内 op3 USER_STATE 的成员口径）。
// 厂商规格、取源链与回落原因见 dungeon_party_user_state_members.go 的文件头。
//
//   - 默认（未设置 / self / 非法值）= **逐字节保持今天**：条数恒 1、4 字节体、既有日志一条不变；
//   - all = 条数 = 本场队伍副本实例里实际在场的成员，receiver-first + 稳定 slot 升序；
//   - 任何"成员不可证"的情形都回落成只发自己并在事件里带 reason=。

func TestPartyDungeonAllMemberUserStateSwitchDefaultsToSelf(t *testing.T) {
	for _, test := range []struct {
		value   string
		want    bool
		wantBad bool
	}{
		{value: "", want: false},
		{value: "self", want: false},
		{value: " SELF ", want: false},
		{value: "default", want: false},
		{value: "current", want: false},
		{value: "self_only", want: false},
		{value: "all", want: true},
		{value: "ALL", want: true},
		{value: " all ", want: true},
		{value: "all_members", want: true},
		{value: "roster", want: true},
		// 只认显式值：数字/布尔/拼接一律非法并回退 self。
		{value: "1", want: false, wantBad: true},
		{value: "true", want: false, wantBad: true},
		{value: "yes", want: false, wantBad: true},
		{value: "self,all", want: false, wantBad: true},
		{value: "ALL_MEMBER", want: false, wantBad: true},
	} {
		t.Run(partyDungeonAllMemberUserStateEnv+"="+test.value, func(t *testing.T) {
			t.Setenv(partyDungeonAllMemberUserStateEnv, test.value)
			opts := optionsFromConfig(config.ServiceConfig{})
			if opts.partyDungeonUserStateAll != test.want {
				t.Fatalf("party dungeon all-member user state=%t want=%t", opts.partyDungeonUserStateAll, test.want)
			}
			if opts.partyDungeonUserStateEnvBad != test.wantBad {
				t.Fatalf("invalid flag=%t want=%t (raw=%q)",
					opts.partyDungeonUserStateEnvBad, test.wantBad, opts.partyDungeonUserStateEnvRaw)
			}
			if test.wantBad && opts.partyDungeonUserStateEnvRaw != strings.TrimSpace(test.value) {
				t.Fatalf("raw env value=%q want=%q", opts.partyDungeonUserStateEnvRaw, strings.TrimSpace(test.value))
			}
		})
	}
	// 零值 options = 加开关之前的构造方式（测试与探针里的 Service 字面量）：必须保持现行为。
	if got := (&Service{}).options.partyDungeonUserStateAll; got {
		t.Fatal("zero-value options enabled all-member user state")
	}
	if got := (&Service{}).partyDungeonUserStateMode(); got != partyDungeonUserStateValueSelf {
		t.Fatalf("zero-value options label=%q want=%q", got, partyDungeonUserStateValueSelf)
	}
	if got := (&Service{options: options{partyDungeonUserStateAll: true}}).partyDungeonUserStateMode(); got != partyDungeonUserStateValueAll {
		t.Fatalf("all-member options label=%q want=%q", got, partyDungeonUserStateValueAll)
	}
}

// 逐字节断言：默认档（单条）与 all 档（2 人 = 7 字节 / 单人 = 4 字节）。
// 厂商形状 = u8 条数 + N×(u16 key + u8 state)。
func TestBuildDungeonUserStateBodyAllMembersByteExact(t *testing.T) {
	// 默认档入口：2 人队也只发自己，与今天逐字节相同。
	single, err := buildCurrentDungeonUserStateBody(19)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(single, []byte{1, 0x13, 0x00, currentDungeonPlayerUserState}) || len(single) != 4 {
		t.Fatalf("default single-member body=%x want 01130001", single)
	}
	// all 档 2 人：02 | 13 00 01 | 64 00 01 = 7 字节。
	two, err := buildCurrentDungeonUserStateBodyForMemberKeys([]uint16{19, 100})
	if err != nil {
		t.Fatal(err)
	}
	wantTwo := []byte{0x02, 0x13, 0x00, currentDungeonPlayerUserState, 0x64, 0x00, currentDungeonPlayerUserState}
	if !bytes.Equal(two, wantTwo) || len(two) != 7 {
		t.Fatalf("all-member two-member body=%x want %x (len %d)", two, wantTwo, len(two))
	}
	// all 档单人（实例里只有自己）：仍与默认档逐字节相同。
	solo, err := buildCurrentDungeonUserStateBodyForMemberKeys([]uint16{77})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(solo, []byte{1, 0x4d, 0x00, currentDungeonPlayerUserState}) || len(solo) != 4 {
		t.Fatalf("all-member solo body=%x want 014d0001", solo)
	}
	// 空列表 / 零 key 一律报错，绝不发半条脏数据。
	if _, err := buildCurrentDungeonUserStateBodyForMemberKeys(nil); err == nil {
		t.Fatal("empty member list accepted")
	}
	if _, err := buildCurrentDungeonUserStateBodyForMemberKeys([]uint16{0}); err == nil {
		t.Fatal("zero member key accepted")
	}
	if _, err := buildCurrentDungeonUserStateBodyForMemberKeys([]uint16{19, 0}); err == nil {
		t.Fatal("zero trailing member key accepted")
	}
	if _, err := buildCurrentDungeonUserStateBody(0); err == nil {
		t.Fatal("zero object key accepted")
	}
}

// 顺序：receiver-first，其余按稳定 slot 升序。两边的输出必须互为镜像
// （leader 视角 99,100；follower 视角 100,99），且多次调用完全一致。
func TestDungeonPartyUserStateAllMembersOrderReceiverFirstThenStableSlot(t *testing.T) {
	service, leader, follower, leaderRuntime, _ := partyDungeonDeathFixture(t)
	service.options.partyDungeonUserStateAll = true

	leaderKeys, reason := service.dungeonAllMemberUserStateKeys(leader, leaderRuntime, currentSceneActorObjectKey(99))
	if reason != "" {
		t.Fatalf("leader member keys fell back: %s", reason)
	}
	if want := []uint16{99, 100}; !equalUint16Keys(leaderKeys, want) {
		t.Fatalf("leader keys=%v want=%v", leaderKeys, want)
	}
	followerKeys, reason := service.dungeonAllMemberUserStateKeys(follower, leaderRuntime, currentSceneActorObjectKey(100))
	if reason != "" {
		t.Fatalf("follower member keys fell back: %s", reason)
	}
	if want := []uint16{100, 99}; !equalUint16Keys(followerKeys, want) {
		t.Fatalf("follower keys=%v want=%v", followerKeys, want)
	}
	// 实例成员表是 map：50 次调用必须完全同序（禁止 map 迭代顺序泄漏）。
	for iteration := 0; iteration < 50; iteration++ {
		again, reason := service.dungeonAllMemberUserStateKeys(leader, leaderRuntime, 99)
		if reason != "" || !equalUint16Keys(again, leaderKeys) {
			t.Fatalf("iteration %d keys=%v reason=%q want=%v", iteration, again, reason, leaderKeys)
		}
	}
}

// 4 人队 + 乱序插入实例：输出只由 roster（稳定 slot 序）决定。
func TestDungeonPartyUserStateAllMembersOrderIgnoresInstanceInsertionOrder(t *testing.T) {
	service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
	service.options.partyDungeonUserStateAll = true
	instance := leaderRuntime.partyInstance
	if instance == nil {
		t.Fatal("fixture did not publish a shared party dungeon instance")
	}
	roster := []alignedcmd.PartyMemberState{
		{UserID: 99, UserState: 1},
		{UserID: 100, UserState: 1},
		{UserID: 101, UserState: 1},
		{UserID: 102, UserState: 1},
	}
	storeRuntimePartyState(leader, alignedcmd.PartyState{PartyID: 12, UserID: 99, Members: roster})
	for _, order := range [][]uint16{{100, 101, 102}, {102, 100, 101}, {101, 102, 100}} {
		instance.mu.Lock()
		instance.members = map[uint16]time.Time{}
		instance.sessions = map[uint16]*gameSession{}
		instance.members[99] = time.Now()
		instance.sessions[99] = leader
		for _, id := range order {
			instance.members[id] = time.Now()
			instance.sessions[id] = &gameSession{selectedCharacterID: id}
		}
		instance.mu.Unlock()
		keys, reason := service.dungeonAllMemberUserStateKeys(leader, leaderRuntime, 99)
		if reason != "" {
			t.Fatalf("insertion order %v fell back: %s", order, reason)
		}
		if want := []uint16{99, 100, 101, 102}; !equalUint16Keys(keys, want) {
			t.Fatalf("insertion order %v keys=%v want=%v", order, keys, want)
		}
	}
	// 自己不是 slot 0 时仍然 receiver-first，其余仍按 slot 升序。
	storeRuntimePartyState(leader, alignedcmd.PartyState{
		PartyID: 12,
		UserID:  100,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 100, UserState: 1},
			{UserID: 99, UserState: 1},
			{UserID: 101, UserState: 1},
			{UserID: 102, UserState: 1},
		},
	})
	keys, reason := service.dungeonAllMemberUserStateKeys(leader, leaderRuntime, 99)
	if reason != "" {
		t.Fatalf("non-slot-zero self fell back: %s", reason)
	}
	if want := []uint16{99, 100, 101, 102}; !equalUint16Keys(keys, want) {
		t.Fatalf("non-slot-zero self keys=%v want=%v", keys, want)
	}
}

// 任何"成员不可证"的情形：4 字节只发自己 + reason=，绝不发半条脏数据。
func TestDungeonPartyUserStateAllMembersFallsBackToSelfWithReason(t *testing.T) {
	selfBody := []byte{0x01, 0x63, 0x00, currentDungeonPlayerUserState}
	assertFallback := func(t *testing.T, service *Service, session *gameSession, runtimeLookup *runtimeDungeonState, selfKey uint16, wantReason string) {
		t.Helper()
		members := service.dungeonUserStateMembersForSession(session, runtimeLookup, selfKey)
		if members.Mode != partyDungeonUserStateValueAll {
			t.Fatalf("mode=%q want all", members.Mode)
		}
		if members.Reason != wantReason {
			t.Fatalf("fallback reason=%q want=%q", members.Reason, wantReason)
		}
		body, err := buildCurrentDungeonUserStateBodyForMemberKeys(members.Keys)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, selfBody) || len(body) != 4 {
			t.Fatalf("%s body=%x want %x (len 4)", wantReason, body, selfBody)
		}
		if len(members.Keys) != 1 || members.Keys[0] != selfKey {
			t.Fatalf("%s keys=%v want [%d]", wantReason, members.Keys, selfKey)
		}
	}

	t.Run("no_active_dungeon_runtime", func(t *testing.T) {
		service, leader, _, _, _ := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		assertFallback(t, service, leader, nil, 99, "no_active_dungeon_runtime")
	})
	t.Run("no_party_dungeon_instance_for_this_run", func(t *testing.T) {
		service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		leaderRuntime.partyInstance = nil
		assertFallback(t, service, leader, leaderRuntime, 99, "no_party_dungeon_instance_for_this_run")
	})
	t.Run("sole_member_in_party_dungeon_instance", func(t *testing.T) {
		// 队友还没落位（或已离开）：实例里只剩自己。
		service, leader, _, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		service.releasePartyDungeonInstance(followerRuntime, 100, "test_user_state_fallback")
		assertFallback(t, service, leader, leaderRuntime, 99, "sole_member_in_party_dungeon_instance")
	})
	t.Run("party_dungeon_member_session_unavailable", func(t *testing.T) {
		// 队友 key 不可得：实例里挂着成员但会话指针已失效（身份不可证）。
		service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		instance := leaderRuntime.partyInstance
		instance.mu.Lock()
		delete(instance.sessions, 100)
		instance.mu.Unlock()
		assertFallback(t, service, leader, leaderRuntime, 99, "party_dungeon_member_session_unavailable")
	})
	t.Run("self_not_registered_in_party_dungeon_instance", func(t *testing.T) {
		service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		instance := leaderRuntime.partyInstance
		instance.mu.Lock()
		delete(instance.members, 99)
		delete(instance.sessions, 99)
		instance.mu.Unlock()
		assertFallback(t, service, leader, leaderRuntime, 99, "self_not_registered_in_party_dungeon_instance")
	})
	t.Run("party_dungeon_member_missing_from_local_roster", func(t *testing.T) {
		// 实例里有成员但本会话名册里没有它的稳定 slot ⇒ 顺序只能猜，回落。
		service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
		service.options.partyDungeonUserStateAll = true
		storeRuntimePartyState(leader, alignedcmd.PartyState{
			PartyID: 12,
			UserID:  99,
			Members: []alignedcmd.PartyMemberState{{UserID: 99, UserState: 1}},
		})
		assertFallback(t, service, leader, leaderRuntime, 99, "party_dungeon_member_missing_from_local_roster")
	})
}

// 事件字段：成功一条带 mode/user_state_count/member_keys；回落一条额外带 reason=；
// 默认档（self）**一条都不加**（既有日志与包序逐字节不变）。
func TestDungeonPartyUserStateAllMembersEventFields(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service, leader, _, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
	service.packetLog = logger
	service.options.partyDungeonUserStateAll = true

	if body, err := service.currentDungeonUserStateBody(leader, leaderRuntime, 99, "test_all_members"); err != nil {
		t.Fatal(err)
	} else if want := []byte{2, 99, 0, 1, 100, 0, 1}; !bytes.Equal(body, want) {
		t.Fatalf("all-member body=%x want %x", body, want)
	}
	service.releasePartyDungeonInstance(followerRuntime, 100, "test_all_members_fallback")
	if body, err := service.currentDungeonUserStateBody(leader, leaderRuntime, 99, "test_all_members_fallback"); err != nil {
		t.Fatal(err)
	} else if want := []byte{1, 99, 0, 1}; !bytes.Equal(body, want) {
		t.Fatalf("fallback body=%x want %x", body, want)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	full := ""
	fallback := ""
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "kind=game-dungeon-user-state-members") {
			continue
		}
		switch {
		case strings.Contains(line, "source=test_all_members "):
			full = line
		case strings.Contains(line, "source=test_all_members_fallback "):
			fallback = line
		}
	}
	if full == "" {
		t.Fatalf("all-member event missing\n%s", text)
	}
	for _, field := range []string{
		"mode=all",
		"user_state_count=2",
		"member_keys=99,100",
		"self_key=99",
		"user_state=1",
		"body_len=7",
		"env=" + partyDungeonAllMemberUserStateEnv,
		"order=receiver_first_then_stable_party_slot_ascending",
	} {
		if !strings.Contains(full, field) {
			t.Fatalf("all-member event is missing %q\nline=%s", field, full)
		}
	}
	if strings.Contains(full, "reason=") {
		t.Fatalf("all-member event must not carry a fallback reason\nline=%s", full)
	}
	if fallback == "" {
		t.Fatalf("fallback event missing\n%s", text)
	}
	for _, field := range []string{
		"mode=all",
		"user_state_count=1",
		"member_keys=99",
		"body_len=4",
		"reason=sole_member_in_party_dungeon_instance",
	} {
		if !strings.Contains(fallback, field) {
			t.Fatalf("fallback event is missing %q\nline=%s", field, fallback)
		}
	}

	// 默认档：同一个 fixture、同一条发送路径，事件一条都不能有。
	defaultLogPath := filepath.Join(t.TempDir(), "packet-default.log")
	defaultLogger, err := openPacketLogger(defaultLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defaultService, defaultLeader, _, defaultRuntime, _ := partyDungeonDeathFixture(t)
	defaultService.packetLog = defaultLogger
	if body, err := defaultService.currentDungeonUserStateBody(defaultLeader, defaultRuntime, 99, "test_default_self"); err != nil {
		t.Fatal(err)
	} else if want := []byte{1, 99, 0, 1}; !bytes.Equal(body, want) {
		t.Fatalf("default body=%x want %x", body, want)
	}
	if err := defaultLogger.Close(); err != nil {
		t.Fatal(err)
	}
	defaultLogged, err := os.ReadFile(defaultLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(defaultLogged), "game-dungeon-user-state-members") {
		t.Fatalf("default self mode must not add any member event\n%s", string(defaultLogged))
	}
}

// 真实分发路径（不是只测 builder）：走一次 sendCurrentPostStartMapPlayerPlacement，
// 在 op2 mode0/mode1/op120 的**同一批**下行里找出 class0/op3 USER_STATE 的 body。
func TestPostStartMapDungeonUserStateRealDispatchPath(t *testing.T) {
	for _, test := range []struct {
		name string
		all  bool
		want []byte
	}{
		{name: "default self keeps four byte body", all: false, want: []byte{1, 99, 0, 1}},
		{name: "all members declares both slots", all: true, want: []byte{2, 99, 0, 1, 100, 0, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, leader, _, leaderRuntime, _ := partyDungeonDeathFixture(t)
			service.characterStats = testCharacterStatTable(t)
			service.options.partyDungeonUserStateAll = test.all
			scene := leaderRuntime.Session.Snapshot().Scene
			connection := leader.conn.(*bufferConn)
			connection.write.Reset()
			if err := service.sendCurrentPostStartMapPlayerPlacement(leader, leaderRuntime, scene, "test_user_state_dispatch"); err != nil {
				t.Fatal(err)
			}
			stream := connection.write.Bytes()
			found := 0
			for len(stream) > 0 {
				packet, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyUserState) {
					continue
				}
				found++
				if !bytes.Equal(packet.Body, test.want) {
					t.Fatalf("post-start-map user state body=%x want %x", packet.Body, test.want)
				}
			}
			if found != 1 {
				t.Fatalf("post-start-map user state packets=%d want 1", found)
			}
		})
	}
}

// 另外两个发送点的**真实分发路径**：finish-loading 延迟档（教程）与 op191 剧情恢复延迟档。
func TestDeferredDungeonUserStateSendPointsHonorAllMembersMode(t *testing.T) {
	assertOneUserState := func(t *testing.T, stream []byte, want []byte) {
		t.Helper()
		found := 0
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyUserState) {
				continue
			}
			found++
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("user state body=%x want %x", packet.Body, want)
			}
		}
		if found != 1 {
			t.Fatalf("user state packets=%d want 1", found)
		}
	}

	t.Run("finish_loading_deferred", func(t *testing.T) {
		for _, test := range []struct {
			name string
			all  bool
			want []byte
		}{
			{name: "default self", all: false, want: []byte{1, 99, 0, 1}},
			{name: "all members", all: true, want: []byte{2, 99, 0, 1, 100, 0, 1}},
		} {
			t.Run(test.name, func(t *testing.T) {
				service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{})
				service.options.partyDungeonUserStateAll = test.all
				// finish-loading 那条延迟 op3 前面的技能扇出需要一份本地技能目录
				// （合成 PVF 没有 Script.pvf，靠 initialSkillsByJob 短路）。
				skillCatalog, err := buildSkillCatalogFromSource(context.Background(), initialEquipmentMemSource{
					"skill/skilllist.lst":     "11 `job11.lst`\n",
					"skill/job11.lst":         "1 `job11/one.skl`\n",
					"skill/job11/one.skl":     "[skill type]\n`active`\n[skill class]\n1\n[required level]\n1\n",
					"character/character.lst": "11 `job11.chr`\n",
					"character/job11.chr":     "[job]\n`[swordman]`\n",
				})
				if err != nil {
					t.Fatal(err)
				}
				service.skillCatalog = skillCatalog
				service.initialSkillsByJob = map[byte][]initialSkillEntry{11: {{SkillID: 1, Level: 1}}}
				service.initialSPTable = map[int]int{1: 11, 20: 11}
				service.initialTPTable = map[int]int{1: 14, 20: 14}

				connection := &bufferConn{}
				session := &gameSession{
					conn:                              connection,
					selectedCharacterID:               99,
					dungeon:                           dungeonSessionState{runtime: runtime},
					postStartMapPlayerStateSent:       true,
					deferredDungeonUserStateObjectKey: currentSceneActorObjectKey(99),
				}
				attachTwoMemberDungeonRun(session, runtime)
				if err := service.sendCurrentPostFinishLoadingPlayerState(session, "test_finish_loading_user_state"); err != nil {
					t.Fatal(err)
				}
				assertOneUserState(t, connection.write.Bytes(), test.want)
				if session.deferredDungeonUserStateObjectKey != 0 {
					t.Fatalf("deferred key=%d want cleared", session.deferredDungeonUserStateObjectKey)
				}
			})
		}
	})

	t.Run("story_pause_resume_deferred", func(t *testing.T) {
		for _, test := range []struct {
			name string
			all  bool
			want []byte
		}{
			{name: "default self", all: false, want: []byte{1, 99, 0, 1}},
			{name: "all members", all: true, want: []byte{2, 99, 0, 1, 100, 0, 1}},
		} {
			t.Run(test.name, func(t *testing.T) {
				service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{})
				service.options.partyDungeonUserStateAll = test.all
				// 教程 op191 state-1/type-1 的延迟发送档（DNFBRIDGE_TUTORIAL_DEFER_USER_STATE
				// 默认档）才是发 op3 的那一支；置 false 是"完全抑制"的应急回退。
				service.options.tutorialDeferUserState = true
				connection := &bufferConn{}
				session := &gameSession{
					conn:                          connection,
					selectedCharacterID:           99,
					dungeon:                       dungeonSessionState{runtime: runtime},
					postStartMapPlayerStateSent:   true,
					currentFinishLoadingStateSent: true,
				}
				attachTwoMemberDungeonRun(session, runtime)
				if err := service.handleCurrentDungeonStoryPause(session, []byte{1, 1}); err != nil {
					t.Fatal(err)
				}
				stream := connection.write.Bytes()
				pause, rest := splitGameServerUpperPacket(t, stream)
				if pause.Header.MsgID != currentDungeonStoryPauseMsgID {
					t.Fatalf("op170 header=%+v", pause.Header)
				}
				assertOneUserState(t, rest, test.want)
			})
		}
	})
}

// attachTwoMemberDungeonRun 给会话挂上"同一场副本里两个成员"的运行时状态：
// 队伍名册（稳定 slot 序：自己 99 在 slot 0，队友 100 在 slot 1）+ 队伍副本实例
// （两者都实际在场）。只用于钉住 op3 USER_STATE 的成员口径。
func attachTwoMemberDungeonRun(session *gameSession, runtime *runtimeDungeonState) {
	storeRuntimePartyState(session, alignedcmd.PartyState{
		PartyID: 7,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1},
			{UserID: 100, UserState: 1},
		},
	})
	runtime.partyInstance = &dungeonPartyInstance{
		id:       1,
		members:  map[uint16]time.Time{99: time.Now(), 100: time.Now()},
		sessions: map[uint16]*gameSession{99: session, 100: {selectedCharacterID: 100}},
	}
}

func equalUint16Keys(got, want []uint16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
