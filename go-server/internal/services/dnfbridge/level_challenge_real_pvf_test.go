package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFLevelChallengeNativeCreationAllJobs(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	s := &Service{initialEquipmentArchive: archive, repositoryProvider: func() (repo.Group, bool) { return repos, true },
		options:               options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		spendTimeCatalogState: &currentSpendTimeRuntimeCatalog{}}
	enablePortedActivityTest(t, s, nil)
	if _, err := s.activityControls.Save(ctx, s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 222, Enabled: false}, {ID: 288, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	s.portedActivityNativeReady[288] = true
	// 与Service.Start一致，创建请求的短事务计时之前先预热静态资源。
	for _, preload := range []func(context.Context) error{s.preloadCharacterStatTable, s.preloadSkillCatalog, s.preloadProfessionProfiles} {
		if err := preload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for job := 0; job <= jobmap.MaxID; job++ {
		if !jobmap.Valid(job) {
			continue
		}
		t.Run(fmt.Sprintf("职业%d", job), func(t *testing.T) {
			account := fmt.Sprintf("challenge-account-%d", job)
			if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: account}); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, accountID: account, loginPacketSeen: true, accountFromToken: true}
			session.spendTime.eventInfoSent = true
			createWithOptions := func(name string, options []byte) (bool, string, []byte) {
				t.Helper()
				conn.write.Reset()
				request := buildCreateRequest(byte(job), name)
				request = append(request, options...)
				if err := s.handleUpperCreateCharacter(session, request); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.MsgID != 5 || len(ack.Body) == 0 {
					t.Fatalf("创建应答=%d %x", ack.Header.MsgID, ack.Body)
				}
				if ack.Body[0] != 1 {
					return false, "", rest
				}
				if len(ack.Body) < 3 {
					t.Fatal("创建成功缺少角色编号")
				}
				return true, strconv.Itoa(int(binary.LittleEndian.Uint16(ack.Body[1:3]))), rest
			}
			create := func(name string, participate byte) (bool, string, []byte) {
				t.Helper()
				return createWithOptions(name, []byte{0, 0, 0, 0, 0, 0, 0xff, participate, 5})
			}
			ok, id, rest := create(fmt.Sprintf("challenge%d", job), 1)
			if !ok {
				t.Fatalf("FF 01 05活动创建仍失败：%x", conn.write.Bytes())
			}
			ch, found, err := repos.Character.Load(ctx, id)
			if err != nil || !found || ch.Level != 1 || ch.Stats["grow_type"] != 0 || ch.Stats["pvp_private_character"] != 0 || ch.Stats[onlineevent.LevelChallengeRoleStat] != 1 {
				t.Fatalf("活动角色落库不符：%+v err=%v", ch, err)
			}
			if s.luckyCharacterCatalog != nil && s.luckyCharacterCatalog.Level1To89Only && ch.Stats[currentLuckyNewCharacterStat] != 1 {
				t.Fatal("new role creation did not atomically persist lucky eligibility")
			}
			if _, found, err := repos.Inventory.Load(ctx, id); err != nil || !found {
				t.Fatalf("初始背包未落库：%v", err)
			}
			if equipment, found, err := repos.Equipment.Load(ctx, id); err != nil || !found || len(equipment.Entries) == 0 {
				t.Fatalf("初始装备未落库：%v", err)
			}
			if _, found, err := repos.Skill.Load(ctx, id); err != nil || !found {
				t.Fatalf("初始技能未落库：%v", err)
			}
			packet, _ := splitLongHengGameServerUpperPacket(t, rest)
			var expected packetWriter
			expected.writeByte(1)
			expected.writeByte(1)
			expected.writeUint32(0) // 每个测试账号只有一个角色，原生名单按显示序号匹配。
			if packet.Header.MsgID != 815 || !bytes.Equal(packet.Body, expected.bytes()) {
				t.Fatalf("创建后活动名单=%d %x", packet.Header.MsgID, packet.Body)
			}
			automatic := s.levelChallengeCatalog.AutomaticNewCharacters
			repeatOK, _, _ := create(fmt.Sprintf("repeat%d", job), 1)
			if repeatOK != automatic {
				t.Fatalf("第二名新角色创建=%v，自动参加=%v", repeatOK, automatic)
			}
			expectedCount := 1
			if repeatOK {
				expectedCount++
			}
			ordinaryOK, ordinaryID, _ := create(fmt.Sprintf("ordinary%d", job), 0)
			if !ordinaryOK {
				t.Fatal("名额用完后普通创建被阻止")
			} else if ordinary, found, err := repos.Character.Load(ctx, ordinaryID); err != nil || !found || ordinary.Stats[onlineevent.LevelChallengeRoleStat] != 0 || ordinary.Stats["grow_type"] != 0 {
				t.Fatalf("普通角色错误报名或被末字节转职：%+v err=%v", ordinary, err)
			}
			stored, _, err := repos.Account.Load(ctx, account)
			state, stateErr := onlineevent.ReadLevelChallengeState(stored)
			if err != nil || stateErr != nil || len(state) != expectedCount {
				t.Fatalf("账号名额回执不符：%+v %v %v", state, err, stateErr)
			}
			if _, registered := state[id]; !registered {
				t.Fatal("活动角色没有持久报名回执")
			}
			legacyOK, legacyID, _ := createWithOptions(fmt.Sprintf("legacy%d", job), nil)
			if !legacyOK {
				t.Fatal("无创建选项旧请求被阻止")
			}
			legacy, found, err := repos.Character.Load(ctx, legacyID)
			expectedLegacyFlag := int64(0)
			if automatic {
				expectedLegacyFlag = 1
				expectedCount++
			}
			if err != nil || !found || legacy.Stats[onlineevent.LevelChallengeRoleStat] != expectedLegacyFlag || legacy.Stats["grow_type"] != 0 {
				t.Fatalf("无选项legacy角色=%+v found=%t err=%v", legacy, found, err)
			}
			stored, _, err = repos.Account.Load(ctx, account)
			state, stateErr = onlineevent.ReadLevelChallengeState(stored)
			if err != nil || stateErr != nil || len(state) != expectedCount {
				t.Fatalf("legacy报名回执不符：%+v %v %v", state, err, stateErr)
			}
			if _, registered := state[legacyID]; registered != automatic {
				t.Fatalf("legacy角色报名=%v，自动参加=%v", registered, automatic)
			}
			// 此时没有进入城镇、也未初始化在线时钟，选角后仍须正确通知816。
			for _, selected := range []struct {
				id   string
				flag byte
			}{{id, 1}, {ordinaryID, 0}, {legacyID, byte(expectedLegacyFlag)}} {
				n, err := strconv.Atoi(selected.id)
				if err != nil {
					t.Fatal(err)
				}
				session.selectedCharacterID = uint16(n)
				session.characterGeneration++
				s.gameSessions = map[uint16]*gameSession{uint16(n): session}
				conn.write.Reset()
				if err := s.sendCurrentLevelChallengeSelectedState(session); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				body, err := buildCurrentLevelChallengeState(selected.flag == 1, s.levelChallengeCatalog)
				if err != nil || packet.Header.MsgID != 816 || !bytes.Equal(packet.Body, body) || len(rest) != 0 {
					t.Fatalf("首场景前参与状态=%d %x，期望%x，err=%v", packet.Header.MsgID, packet.Body, body, err)
				}
			}
		})
	}
}
