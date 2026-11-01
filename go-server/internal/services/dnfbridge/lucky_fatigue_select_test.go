package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestLuckyFatigueSelectionResetsBothEntriesAndKeepsNewChoice(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "upper"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, q, repos := newChannelReconnectTest(t)
			// 基础选角夹具替换过Character仓储；这里用一致的真实内存事务组，
			// 避免562事务与选角分别读取两个不同的角色仓储。
			coherent := memory.NewMemoryGroup()
			ch, _, _ := repos.Character.Load(ctx, "29")
			account, _, _ := repos.Account.Load(ctx, ch.AccountID)
			if err := coherent.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			if record, found, err := repos.Skill.Load(ctx, "29"); err != nil {
				t.Fatal(err)
			} else if found {
				if err := coherent.Skill.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			if record, found, err := repos.Equipment.Load(ctx, "29"); err != nil {
				t.Fatal(err)
			} else if found {
				if err := coherent.Equipment.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			repos = coherent
			s.repositoryProvider = func() (repo.Group, bool) { return repos, true }
			enablePortedActivityTest(t, s, q)
			s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, FatigueBurn: 100}
			q.accountID = ch.AccountID // 基础选角夹具通过默认账号读取，活动协议需要显式认证账号。
			ch.Slot = 0
			ch.Stats["grow_type"] = 0
			ch.Stats["fatigue_acceleration"] = 1
			ch.Stats["exp"] = 12345
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			selectAgain := func() {
				t.Helper()
				var err error
				if legacy {
					err = s.sendSelectCharacterState(q, make([]byte, 16))
				} else {
					err = s.sendUpperCSharpSelectInit(q, make([]byte, 16))
				}
				if err != nil {
					t.Fatal(err)
				}
				if q.selectedCharacterID != 29 {
					t.Fatal("未完成真实选角")
				}
			}
			selectAgain()
			ch, _, _ = repos.Character.Load(ctx, "29")
			before := ch.Stats["fatigue"]
			if ch.Stats["fatigue_acceleration"] != 0 || ch.Stats["exp"] != 12345 {
				t.Fatal("选角未关闭旧燃烧选择，或改变了角色经验")
			}
			conn := q.conn.(*bufferConn)
			conn.write.Reset()
			// Selection now publishes this state immediately. Force one isolated
			// snapshot here to verify its body independent of de-duplication.
			if err := s.sendCurrentLuckyFatigueState(q, true); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 490 || !bytes.Equal(packet.Body, []byte{100, 0, 0}) || len(rest) != 0 {
				t.Fatal("选角后未同步原生未勾选状态")
			}
			debit := repo.CloneCharacter(ch)
			r := s.debitCurrentLuckyRoomFatigue(q, &debit, worldmap.Dungeon{ID: 310})
			if r.Cost != 1 || r.BonusPercent != 0 {
				t.Fatal("未重新勾选仍按燃烧扣费")
			}
			conn.write.Reset()
			if err := s.handleCurrentLuckyFatigueToggle(q, []byte{1}); err != nil {
				t.Fatal(err)
			}
			ack, reply := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{1}) || len(reply) == 0 {
				t.Fatalf("重新勾选应成功：应答%x", ack.Body)
			}
			q.sceneBootstrapActorGeneration++
			if err := s.sendCurrentLuckyFatigueState(q, false); err != nil {
				t.Fatal(err)
			}
			ch, err := s.updateCurrentCharacterFatigue(ctx, q, false)
			if err != nil || ch.Stats["fatigue_acceleration"] != 1 || ch.Stats["fatigue"] != before {
				t.Fatalf("场景或疲劳刷新改变选择/余额：错误%v，开关%d，余额%d，原余额%d", err, ch.Stats["fatigue_acceleration"], ch.Stats["fatigue"], before)
			}
			debit = repo.CloneCharacter(ch)
			r = s.debitCurrentLuckyRoomFatigue(q, &debit, worldmap.Dungeon{ID: 310})
			if r.Cost != 2 || r.BonusPercent != 100 {
				t.Fatal("重新勾选未生效")
			}
			selectAgain()
			ch, _, _ = repos.Character.Load(ctx, "29")
			if ch.Stats["fatigue_acceleration"] != 0 || ch.Stats["fatigue"] != before {
				t.Fatal("再次选角未重置开关，或选角产生额外扣费")
			}
		})
	}
}

func TestLuckyFatigueSelectionResetRejectsInvalidOwner(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		s, q, repos := luckyEffectsTestService(t, 10)
		ctx := context.Background()
		ch, _, _ := repos.Character.Load(ctx, "99")
		ch.Stats["fatigue_acceleration"] = 1
		if err := repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if foreign {
			q.accountID = "other-account"
		} else {
			q.loginRevoked.Store(true)
		}
		if _, err := s.resetCurrentLuckyFatigueOnSelect(ctx, q); err == nil {
			t.Fatal("无效选角身份可以修改角色燃烧状态")
		}
		ch, _, _ = repos.Character.Load(ctx, "99")
		if ch.Stats["fatigue_acceleration"] != 1 || ch.Stats["fatigue"] != 10 {
			t.Fatal("无效选角写入了角色")
		}
	}
}
