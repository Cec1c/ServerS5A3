package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 只有明确测试开团的夹具调用，不能让全局会话绑定自动伪造已入等候区。
func enterRaidStartWaitingFixture(t *testing.T, f *raidStartGuardFixture, battleID string) {
	t.Helper()
	if f.service.onlinePlayers == nil {
		f.service.onlinePlayers = newOnlinePlayerManager()
	}
	for _, member := range f.state.Members {
		peer, found := f.service.onlineGameSession(member.CharID)
		if !found {
			t.Fatal("开团夹具缺少已认证成员", member.CharID)
		}
		rule, err := f.service.currentRaidAdmissionRule(peer)
		if err != nil {
			t.Fatal(err)
		}
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: member.CharID, AccountID: peer.accountID, Session: peer, TownID: rule.WaitingTown, AreaID: rule.WaitingArea, ChannelID: peer.channel.ID, Instance: f.state.RaidKey, RaidBattleInstance: battleID})
	}
}

func raidStartWaitingFixture(t *testing.T, route byte) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	if route == 2 {
		f, peers := actualCurrentFiendWar(t, 8)
		f.state.Battle = nil // 准备夹具不能代替真实开始入口。
		enterRaidStartWaitingFixture(t, f, "")
		return f, peers
	}
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要当前真实PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 2)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.CurrentLayout, f.state.CurrentUIFlags, f.state.TailFlag, f.state.RouteOrRaidType = true, [3]byte{}, 0, route
	var peers []*gameSession
	for i := range f.state.Members {
		m := &f.state.Members[i]
		m.GroupIndex, m.SlotOrder = 1, byte(i)
		peer := f.session()
		peer.selectedCharacterID = m.CharID
		if i > 0 {
			peer.accountID = "account-" + strconv.Itoa(int(m.CharID))
		}
		if route == 1 {
			peer.channel.ID, peer.channel.Type = 32, 32
			if err := f.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: strconv.Itoa(int(m.CharID)), States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}, 12830: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
		}
		peer.loginPacketSeen, peer.accountFromToken = true, true
		f.service.bindGameSessionCharacter(peer, m.CharID)
		rule, err := f.service.currentRaidAdmissionRule(peer)
		if err != nil {
			t.Fatal(err)
		}
		bag := dnfrepo.InventoryRecord{CharacterID: strconv.Itoa(int(m.CharID)), Slots: map[string]dnfrepo.ItemStack{"0:50": {ItemID: int64(rule.ItemID), Count: rule.Count + 2}}}
		if route == 1 {
			bag.Slots["0:51"] = dnfrepo.ItemStack{ItemID: 10159142, Count: 40}
		}
		if err := f.repos.Inventory.Save(context.Background(), bag); err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peer)
	}
	enterRaidStartWaitingFixture(t, f, "")
	return f, peers
}

func raidWaitingInventories(t *testing.T, f *raidStartGuardFixture) map[uint16]dnfrepo.InventoryRecord {
	t.Helper()
	bags := make(map[uint16]dnfrepo.InventoryRecord)
	for _, m := range f.state.Members {
		bag, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(m.CharID)))
		if !found || err != nil {
			t.Fatal("读取隔离背包失败", found, err)
		}
		bags[m.CharID] = bag
	}
	return bags
}

func TestRealPVFRaidStartRequiresEveryMemberInOwnedWaitingArea(t *testing.T) {
	for route, name := range []string{"安图恩", "卢克", "超时空"} {
		t.Run(name, func(t *testing.T) {
			for _, fault := range []string{"全员到位", "队长在外", "队员在外", "只在同频道", "其他团实例", "其他频道", "旧战斗实例", "账号不符", "旧连接", "缺少在线场景", "索引记录不符"} {
				t.Run(fault, func(t *testing.T) {
					f, peers := raidStartWaitingFixture(t, byte(route))
					id := peers[1].selectedCharacterID
					if fault == "队长在外" {
						id = peers[0].selectedCharacterID
					}
					row, _ := f.service.onlinePlayers.PlayerForCharacter(id)
					switch fault {
					case "队长在外", "队员在外":
						row.AreaID = 0
					case "只在同频道":
						row.TownID, row.AreaID, row.Instance = 1, 0, 0
					case "其他团实例":
						row.Instance++
					case "其他频道":
						row.ChannelID++
						row.Session.channel.ID++ // 在线管理器按会话频道登记，不能只改输入字段。
					case "旧战斗实例":
						row.RaidBattleInstance = "previous-battle"
					case "账号不符":
						row.AccountID = "foreign-account"
					case "旧连接":
						row.Session = &gameSession{selectedCharacterID: id, accountID: row.AccountID}
					}
					f.service.onlinePlayers.EnterArea(&row)
					if fault == "缺少在线场景" {
						f.service.onlinePlayers.LeaveArea(id)
					}
					if fault == "索引记录不符" {
						row.AreaID++
					}
					before := cloneRuntimeRaidState(*f.state)
					bags := raidWaitingInventories(t, f)
					if fault != "全员到位" {
						_, ok, err := f.service.startRuntimeRaidWithEntryCost(peers[0])
						if ok || err == nil || !reflect.DeepEqual(before, *f.state) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
							t.Fatal("未到位仍开团或改动了战斗/门票", ok, err)
						}
						peers[1].channel = peers[0].channel
						enterRaidStartWaitingFixture(t, f, "")
					}
					state, ok, err := f.service.startRuntimeRaidWithEntryCost(peers[0])
					if !ok || err != nil || !state.Started || state.Battle == nil {
						t.Fatal("全员进入后不能正常开团", ok, err)
					}
					for _, p := range peers {
						rule, _ := f.service.currentRaidAdmissionRule(p)
						after := raidWaitingInventories(t, f)[p.selectedCharacterID]
						presence, _ := f.service.onlinePlayers.PlayerForCharacter(p.selectedCharacterID)
						if bags[p.selectedCharacterID].Slots["0:50"].Count-after.Slots["0:50"].Count != rule.Count || presence.RaidBattleInstance != state.Battle.InstanceID {
							t.Fatal("未按全员名单扣一次门票并绑定本场", p.selectedCharacterID)
						}
					}
				})
			}
		})
	}
}

func TestRealPVFRaidStartOutsideWaitingAreaOnlySendsFailure(t *testing.T) {
	f, peers := raidStartWaitingFixture(t, 0)
	f.service.onlinePlayers.LeaveArea(peers[1].selectedCharacterID)
	if err := f.service.handleOnlineStartRaid(peers[0], 666, nil); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if packet.Header.MsgID != 666 || packet.Header.Classification != 1 || len(packet.Body) == 0 || packet.Body[0] != 0 || len(rest) != 0 || f.state.Started || f.state.Battle != nil {
		t.Fatal("站位拒绝后发送了开始/战况/扣费刷新包")
	}
}

func TestRealPVFRaidNextPhaseWaitsForEveryMemberManualAndClock(t *testing.T) {
	for _, clock := range []bool{false, true} {
		t.Run(strconv.FormatBool(clock), func(t *testing.T) {
			f, leader, _ := firstAntonFlowFixture(t)
			flow := f.state.Battle.RewardFlow[0]
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			b := f.state.Battle
			enterRaidStartWaitingFixture(t, f, b.InstanceID)
			peer, _ := f.service.onlinePlayers.PlayerForCharacter(20)
			peer.AreaID = 1
			f.service.onlinePlayers.EnterArea(&peer)
			before := cloneRuntimeRaidState(*f.state)
			bags := raidWaitingInventories(t, f)
			if clock {
				states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil)
				if len(states) != 0 || len(errs) != 0 {
					t.Fatal("自动阶段在成员未到位时启动或反复报告异常", states, errs)
				}
			} else if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); ok || err == nil {
				t.Fatal("第二阶段允许成员不在等待区", ok, err)
			}
			if !reflect.DeepEqual(before, cloneRuntimeRaidState(*f.state)) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
				t.Fatal("等待成员时改动阶段或资产")
			}
			enterRaidStartWaitingFixture(t, f, b.InstanceID)
			if clock {
				states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil.Add(time.Second))
				if len(states) != 1 || len(errs) != 0 {
					t.Fatal("全员到位后自动阶段不能继续", states, errs)
				}
			} else if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); !ok || err != nil {
				t.Fatal("全员到位后第二阶段不能开始", ok, err)
			}
			if b.Phase != 1 || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
				t.Fatal("第二阶段重复扣票或未推进")
			}
		})
	}
}
