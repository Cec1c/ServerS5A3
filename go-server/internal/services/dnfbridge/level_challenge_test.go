package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestLevelChallengeNativeCreationAndState(t *testing.T) {
	for _, length := range []int{8, 9} {
		for _, ordinary := range []byte{0, 0xff} {
			options := make([]byte, length)
			options[6], options[7] = ordinary, 1
			if ok, err := currentLevelChallengeCreation(options); err != nil || !ok {
				t.Fatalf("普通模式=%x 参与标记=%v %v", ordinary, ok, err)
			}
			if _, private := currentPrivateCreationGrow(options); private {
				t.Fatal("活动创建被识别成决斗专用创建")
			}
			options[6] = 2
			if _, err := currentLevelChallengeCreation(options); err == nil {
				t.Fatal("与决斗创建冲突未拒绝")
			}
		}
	}
	if ok, err := currentLevelChallengeCreation(make([]byte, 8)); ok || err != nil {
		t.Fatal("普通创建被改变")
	}
	state := onlineevent.LevelChallengeState{"7": {}, "9": {}}
	ch := []repo.CharacterRecord{{CharacterID: "7", Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}}, {CharacterID: "9", Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1, "delete_flag": 1}}}
	if b := buildCurrentLevelChallengeRoster(state, ch, true); !bytes.Equal(b, []byte{2, 1, 0, 0, 0, 0}) {
		t.Fatalf("815=%x", b)
	}
	if b := buildCurrentLevelChallengeRoster(state, ch, false); b[0] != 255 {
		t.Fatal("活动结束仍能报名")
	}
	c := &onlineevent.LevelChallengeCatalog{Main: []onlineevent.LegacyLevelReward{{Item: onlineevent.AttendancePVFItemReward{Count: 300}}}}
	b, err := buildCurrentLevelChallengeState(true, c)
	if err != nil || !bytes.Equal(b, []byte{1, 0x2c, 1, 0, 0}) {
		t.Fatalf("816=%x err=%v", b, err)
	}
}

func TestLevelChallengeNativeCapturedCreationTail(t *testing.T) {
	// 现场失败请求的九字节创建尾不变；职业和名称替换为普通测试值。
	body, err := hex.DecodeString("080400000074657374000000000000ff0105")
	if err != nil {
		t.Fatal(err)
	}
	req, err := parseCreateCharacter(body, 30)
	if err != nil || req.job != 8 || !bytes.Equal(req.options, []byte{0, 0, 0, 0, 0, 0, 0xff, 1, 5}) {
		t.Fatalf("现用op5解析：尾=%x err=%v", req.options, err)
	}
	if active, err := currentLevelChallengeCreation(req.options); err != nil || !active {
		t.Fatalf("普通模式0xFF的活动请求被拒绝：%v %v", active, err)
	}
	for i := range req.options {
		if i == 6 || i == 7 || i == 8 {
			continue
		}
		bad := append([]byte(nil), req.options...)
		bad[i] = 1
		if _, err := currentLevelChallengeCreation(bad); err == nil {
			t.Fatalf("未知创建模式字段%d未拒绝", i)
		}
	}
	for value := 0; value <= 255; value++ {
		options := append([]byte(nil), req.options...)
		options[8] = byte(value)
		if active, err := currentLevelChallengeCreation(options); err != nil || !active {
			t.Fatalf("普通模式下保留选中值%x被当作创建模式：%v", value, err)
		}
	}
	for _, bad := range []byte{1, 2, 3, 4, 5, 0xfe} {
		options := append([]byte(nil), req.options...)
		options[6] = bad
		if _, err := currentLevelChallengeCreation(options); err == nil {
			t.Fatalf("决斗或未知模式%x未拒绝", bad)
		}
	}
	ordinary := append([]byte(nil), req.options...)
	ordinary[7] = 0
	if active, err := currentLevelChallengeCreation(ordinary); err != nil || active {
		t.Fatal("普通创建被误登记为活动角色")
	}
}

func TestLevelChallengeRosterRefreshAfterCreation(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	account := repo.AccountRecord{AccountID: "challenge"}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	c := &onlineevent.LevelChallengeCatalog{Limit: 1, Job: "[all]"}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return repos, true }, levelChallengeCatalog: c, spendTimeCatalogState: &currentSpendTimeRuntimeCatalog{}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, accountID: account.AccountID}
	enablePortedActivityTest(t, s, session)
	if _, err := s.activityControls.Save(ctx, s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 222, Enabled: false}, {ID: 288, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	s.portedActivityNativeReady[288] = true
	// 目录已下发；此专项核对选角列表刷新必须先发815，且读取最新账号名额。
	session.spendTime.eventInfoSent = true
	check := func(send func(*gameSession, []repo.CharacterRecord) error, characters []repo.CharacterRecord, want []byte) {
		t.Helper()
		conn.write.Reset()
		if err := send(session, characters); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 815 || !bytes.Equal(packet.Body, want) {
			t.Fatalf("选角前活动名单=%d %x，期望815 %x", packet.Header.MsgID, packet.Body, want)
		}
		if len(rest) < 7 || binary.LittleEndian.Uint16(rest[1:3]) != 2 {
			t.Fatalf("815之后缺少角色表：%x", rest)
		}
	}
	check(s.sendCurrentCharacterSelectRoster, nil, []byte{0, 0})
	// 登录与返回选角实际使用GET_USERINFO，不能只覆盖独立角色表发送器。
	getUserInfo := func(session *gameSession, _ []repo.CharacterRecord) error {
		return s.sendUpperGetUserInfoBootstrap(session)
	}
	check(getUserInfo, nil, []byte{0, 0})
	ch := repo.CharacterRecord{CharacterID: "7", AccountID: account.AccountID, Job: "0", Level: 1, CreatedAt: time.Now()}
	if err := c.Register(&account, &ch); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	check(s.sendUpperRoster, []repo.CharacterRecord{ch}, []byte{1, 1, 0, 0, 0, 0})
	check(s.sendCurrentCharacterSelectRoster, []repo.CharacterRecord{ch}, []byte{1, 1, 0, 0, 0, 0})
	check(getUserInfo, []repo.CharacterRecord{ch}, []byte{1, 1, 0, 0, 0, 0})
	for _, envelope := range []string{"legacy_hidden_probe_fixed16_zero_prefix", "fixed15_character_list"} {
		check(func(session *gameSession, _ []repo.CharacterRecord) error {
			return s.sendUpperGetUserInfoRosterBootstrapEnvelope(session, envelope)
		}, []repo.CharacterRecord{ch}, []byte{1, 1, 0, 0, 0, 0})
	}
	ch.Stats["delete_flag"] = 1
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	check(s.sendUpperRoster, nil, []byte{1, 0})
	check(getUserInfo, nil, []byte{1, 0})
}

func TestLevelChallengeRosterUsesDisplayIndexesAfterReorderAndDelete(t *testing.T) {
	state := onlineevent.LevelChallengeState{"15": {}, "700": {}, "500": {}}
	roles := []repo.CharacterRecord{
		{CharacterID: "9000", Slot: 2},
		{CharacterID: "15", Slot: 14, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
		{CharacterID: "700", Slot: 20, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
	}
	check := func(rows []repo.CharacterRecord, want []byte) {
		t.Helper()
		if actual := buildCurrentLevelChallengeRoster(state, rows, true); !bytes.Equal(actual, want) {
			t.Fatalf("显示序号名单=%x，期望%x", actual, want)
		}
	}
	check(roles, []byte{3, 2, 1, 0, 0, 0, 2, 0, 0, 0})
	check([]repo.CharacterRecord{roles[2], roles[0], roles[1]}, []byte{3, 2, 0, 0, 0, 0, 2, 0, 0, 0})
	check(roles[:2], []byte{3, 1, 1, 0, 0, 0})
}

func TestLevelChallengeRosterProjectsOnlyEligibleRegisteredDisplayIndexes(t *testing.T) {
	state := onlineevent.LevelChallengeState{
		"89":      {},
		"90":      {},
		"91":      {},
		"deleted": {},
	}
	characters := []repo.CharacterRecord{
		{CharacterID: "90", Level: currentAdventureCharacterLevelCap, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
		{CharacterID: "ordinary", Level: currentAdventureCharacterLevelCap - 1, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
		{CharacterID: "89", Level: currentAdventureCharacterLevelCap - 1, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
		{CharacterID: "91", Level: currentAdventureCharacterLevelCap + 1, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1}},
		{CharacterID: "deleted", Level: currentAdventureCharacterLevelCap - 1, Stats: map[string]int64{onlineevent.LevelChallengeRoleStat: 1, "delete_flag": 1}},
	}
	if actual := buildCurrentLevelChallengeRoster(state, characters, true); !bytes.Equal(actual, []byte{4, 1, 2, 0, 0, 0}) {
		t.Fatalf("未满级有效注册角色名单=%x，期望历史count=4且仅投影displayIndex=2", actual)
	}
}
