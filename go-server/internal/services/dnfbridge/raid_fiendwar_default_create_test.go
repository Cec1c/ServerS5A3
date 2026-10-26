package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
)

func TestCurrentFiendWarDefaultCreateAndModify(t *testing.T) {
	for _, flag := range []byte{0, 1} {
		t.Run(string('0'+rune(flag)), func(t *testing.T) {
			s, a, ac := prepareCurrentRaidTest(t)
			a.channel = channelcatalog.Channel{ID: 184, Type: 42}
			if err := s.handleGameCommand(a, 1, 664, currentRaidRequestFixture(2, "普通超时空", flag, 0, 0)); err != nil {
				t.Fatal(err)
			}
			state, found := s.runtimeRaidForMember(a.selectedCharacterID)
			if !found || state.CurrentUIFlags != [3]byte{} || state.RouteOrRaidType != 2 || len(state.Members) != 1 {
				t.Fatal("超时空默认创建必须登记为普通攻坚队", found, state)
			}
			metadata, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			ack, rest := splitGameServerUpperPacket(t, rest)
			wantMetadata, err := buildCurrentRuntimeRaidMetadata(state, 0)
			if err != nil || metadata.Header.MsgID != 591 || metadata.Header.Classification != 0 || !bytes.Equal(metadata.Body, wantMetadata) || ack.Header.MsgID != 664 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, raid.BuildCreateRaidResultBody(state.RaidKey)) {
				t.Fatal("创建成功前必须先投影普通团本元数据")
			}
			rest = consumeCurrentRaidProfiles(t, rest, state)
			roster, rest := splitGameServerUpperPacket(t, rest)
			if roster.Header.MsgID != 591 || len(rest) != 0 {
				t.Fatal("创建后成员投影顺序异常")
			}
			entries := s.currentRaidSearchEntries(a, raid.CurrentSearchRequest{}, "")
			if len(entries) != 1 || entries[0].Flags != [4]byte{} || entries[0].RaidKey != state.RaidKey {
				t.Fatal("超时空目录不得登记成单人团本", entries)
			}
			ac.write.Reset()
			if err := s.handleGameCommand(a, 1, 670, currentRaidRequestFixture(2, "超时空改名", flag, 0)); err != nil {
				t.Fatal(err)
			}
			after, _ := s.runtimeRaidForMember(a.selectedCharacterID)
			ack, _ = splitGameServerUpperPacket(t, ac.write.Bytes())
			if after.CurrentUIFlags != [3]byte{} || !bytes.Equal(after.NameBytes, rosterNameBytes("超时空改名")) || ack.Header.MsgID != 670 || !bytes.Equal(ack.Body, []byte{1}) {
				t.Fatal("默认标志不得阻止普通超时空改名", after, ack)
			}
		})
	}
}

func TestCurrentFiendWarDefaultCreationAcceptsTeamMembers(t *testing.T) {
	for _, method := range []string{"邀请", "申请"} {
		t.Run(method, func(t *testing.T) {
			s, a, b, ac, bc := prepareAccountFriendTest(t)
			a.channel = channelcatalog.Channel{ID: 184, Type: 42}
			b.channel = a.channel
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: a.selectedCharacterID, TownID: 98, AreaID: 0, Session: a})
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 98, AreaID: 0, Session: b})
			if err := s.handleGameCommand(a, 1, 664, currentRaidRequestFixture(2, "超时空组队", 1, 0, 0)); err != nil {
				t.Fatal(err)
			}
			ac.write.Reset()
			bc.write.Reset()
			if method == "邀请" {
				if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(b.selectedCharacterID)); err != nil {
					t.Fatal(err)
				}
				ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
				if !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("默认创建后邀请被误作单人拒绝", ack)
				}
				if err := s.handleGameCommand(b, 1, 11, currentRaidApprovalFixture(a.selectedCharacterID)); err != nil {
					t.Fatal(err)
				}
			} else {
				body := currentRaidApplyFixture(a.selectedCharacterID)
				body[0] = 184
				if err := s.handleGameCommand(b, 1, 820, body); err != nil {
					t.Fatal(err)
				}
				ack, _ := splitGameServerUpperPacket(t, bc.write.Bytes())
				if !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("默认创建后申请被误作单人拒绝", ack)
				}
				if err := s.handleGameCommand(a, 1, 11, currentRaidApprovalFixture(b.selectedCharacterID)); err != nil {
					t.Fatal(err)
				}
			}
			state, found := s.runtimeRaidForMember(b.selectedCharacterID)
			if !found || state.LeaderID != a.selectedCharacterID || state.CurrentUIFlags != [3]byte{} || len(state.Members) != 2 || state.Members[0].GroupIndex == state.Members[1].GroupIndex || currentRaidMemberMaximum(state) != 8 {
				t.Fatal("普通超时空必须允许加入并保留8人编制", found, state)
			}
		})
	}
}

func TestCurrentFiendWarDefaultCreationKeepsAuthenticationAndModeGuards(t *testing.T) {
	for _, kind := range []string{"未认证", "账号不符", "旧连接", "路由不符", "非法模式", "另阶段", "未知尾标志"} {
		t.Run(kind, func(t *testing.T) {
			s, a, ac := prepareCurrentRaidTest(t)
			a.channel = channelcatalog.Channel{ID: 184, Type: 42}
			body := currentRaidRequestFixture(2, "拒绝无效创建", 1, 0, 0)
			switch kind {
			case "未认证":
				a.accountFromToken = false
			case "账号不符":
				a.accountID = "other-account"
			case "旧连接":
				s.unbindGameSession(a)
			case "路由不符":
				body[0] = 0
			case "非法模式":
				body[len(body)-3] = 2
			case "另阶段":
				body[len(body)-2] = 1
			case "未知尾标志":
				body[len(body)-1] = 1
			}
			if err := s.handleGameCommand(a, 1, 664, body); err != nil {
				t.Fatal(err)
			}
			assertCurrentRaidFailureOnly(t, ac, 664)
			if len(s.raids) != 0 {
				t.Fatal("无效请求创建了攻坚队")
			}
		})
	}
}

func TestRealPVFCurrentFiendWarDefaultCreationPreparesOrdinaryBattle(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	s, a := f.service, peers[0]
	s.raids = make(map[uint32]*runtimeRaidState)
	if err := s.handleGameCommand(a, 1, 664, currentRaidRequestFixture(2, "超时空普通战场", 1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	state, found := s.runtimeRaidForMember(a.selectedCharacterID)
	if !found {
		t.Fatal("默认窗口未能创建普通超时空")
	}
	battle, err := s.prepareCurrentFiendWarBattle(a, state)
	if err != nil || battle == nil || battle.Single || battle.Mode != 3 || battle.FiendWar == nil || len(battle.Dungeons) == 0 {
		t.Fatal("默认创建未接上真实PVF普通超时空战场", battle, err)
	}
}
