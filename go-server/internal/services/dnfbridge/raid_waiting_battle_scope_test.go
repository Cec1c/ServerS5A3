package dnfbridge

import (
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func waitingBattleScopeForTest(key uint32, battle string, channel int, town, area byte, peers ...*gameSession) currentRaidWaitingBattleScope {
	scope := currentRaidWaitingBattleScope{raidKey: key, battleID: battle, channelID: channel, townID: town, areaID: area, members: make(map[uint16]currentRaidWaitingBattleMember)}
	for _, peer := range peers {
		scope.members[peer.selectedCharacterID] = currentRaidWaitingBattleMember{accountID: peer.accountID, session: peer}
	}
	return scope
}

func TestRaidWaitingBattleRebindOnlyFrozenOwnedScene(t *testing.T) {
	for _, raid := range []struct {
		name            string
		town, area, typ byte
		channel         int
	}{{"安图恩", 19, 2, 23, 23}, {"卢克", 32, 4, 32, 231}, {"超时空", 98, 2, 42, 184}} {
		t.Run(raid.name, func(t *testing.T) {
			for _, kind := range []string{"普通区域残留团号", "其他城镇", "其他频道", "其他团", "非成员", "账号不符", "旧连接", "另一场战斗", "记录与索引不符"} {
				t.Run(kind, func(t *testing.T) {
					manager := newOnlinePlayerManager()
					key := uint32(raid.channel)<<16 | 29
					var peers []*gameSession
					var rows []*onlinePlayerInfo
					for _, id := range []uint16{29, 30, 31} {
						peer := &gameSession{channel: channelcatalog.Channel{ID: raid.channel, Type: raid.typ}, selectedCharacterID: id, accountID: "owned-account"}
						peers = append(peers, peer)
						rows = append(rows, &onlinePlayerInfo{CharacterID: id, AccountID: peer.accountID, Session: peer, TownID: raid.town, AreaID: raid.area, ChannelID: raid.channel, Instance: key, PositionX: 100 + id, PositionY: 200, Direction: 5})
					}
					scope := waitingBattleScopeForTest(key, "owned-battle", raid.channel, raid.town, raid.area, peers...)
					foreign := rows[2]
					switch kind {
					case "普通区域残留团号":
						foreign.AreaID = 1
						if raid.typ == 32 {
							foreign.AreaID = 2
						}
					case "其他城镇":
						foreign.TownID++
					case "其他频道":
						foreign.ChannelID++
						foreign.Session.channel.ID++
					case "其他团":
						foreign.Instance++
					case "非成员":
						delete(scope.members, 31)
					case "账号不符":
						foreign.AccountID = "other-account"
					case "旧连接":
						foreign.Session = &gameSession{channel: peers[2].channel, selectedCharacterID: 31, accountID: peers[2].accountID}
					case "另一场战斗":
						foreign.RaidBattleInstance = "previous-battle"
					}
					for _, row := range rows {
						manager.EnterArea(row)
					}
					if kind == "记录与索引不符" {
						foreign.AreaID++
					}
					before := *foreign
					validBefore := *rows[0]
					manager.bindRaidWaitingBattleInstance(scope)
					manager.bindRaidWaitingBattleInstance(scope)
					validBefore.RaidBattleInstance = scope.battleID
					if !reflect.DeepEqual(*rows[0], validBefore) || !manager.PeerInSameArea(29, 30) || manager.PeerInSameArea(29, 31) {
						t.Fatal("合法成员未共同切入本场等待区，或混入其他记录")
					}
					if !reflect.DeepEqual(*foreign, before) {
						t.Fatal("开团改动了范围外的场景记录", before, *foreign)
					}
				})
			}
		})
	}
}

func TestRealPVFRaidWaitingBattleScopeUsesCurrentResourceAndOwner(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要本机真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raid := range []struct {
		name       string
		route, typ byte
		channel    int
		town, area byte
	}{{"安图恩", 0, 23, 23, 19, 2}, {"卢克", 1, 32, 231, 32, 4}, {"超时空", 2, 42, 184, 98, 2}} {
		t.Run(raid.name, func(t *testing.T) {
			for _, kind := range []string{"有效", "战斗频道不符", "战斗类型不符", "模式不符", "成员账号不符", "成员旧连接", "认证失效"} {
				t.Run(kind, func(t *testing.T) {
					s, session, _ := prepareCurrentRaidTest(t)
					s.initialEquipmentArchive = archive
					session.channel = channelcatalog.Channel{ID: raid.channel, Type: raid.typ}
					id := session.selectedCharacterID
					state := runtimeRaidState{RaidKey: uint32(raid.channel)<<16 | uint32(id), LeaderID: id, RouteOrRaidType: raid.route, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: id}}}
					battle := &runtimeRaidBattle{InstanceID: "resource-owned-battle", Mode: raid.route + 1, ChannelID: raid.channel, ChannelType: raid.typ, MemberAccounts: map[uint16]string{id: session.accountID}}
					switch kind {
					case "战斗频道不符":
						battle.ChannelID++
					case "战斗类型不符":
						battle.ChannelType++
					case "模式不符":
						state.RouteOrRaidType++
					case "成员账号不符":
						battle.MemberAccounts[id] = "other-account"
					case "成员旧连接":
						s.unbindGameSession(session)
					case "认证失效":
						session.accountFromToken = false
					}
					scope, err := s.prepareCurrentRaidWaitingBattleScope(session, state, battle)
					if kind != "有效" {
						if err == nil {
							t.Fatal("无效归属仍冻结了等待区重绑范围")
						}
						return
					}
					if err != nil || scope.townID != raid.town || scope.areaID != raid.area || scope.channelID != raid.channel || scope.battleID != battle.InstanceID || scope.members[id].session != session || scope.members[id].accountID != session.accountID {
						t.Fatal("未从真实PVF与当前成员冻结等待区范围", scope, err)
					}
				})
			}
		})
	}
}
