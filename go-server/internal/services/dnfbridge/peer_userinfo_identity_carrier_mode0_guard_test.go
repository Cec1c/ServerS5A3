package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场两族 subtype0 身份载体的形状夹具：
//
//	char 4（t3，崩）  : name "t3"     + 9 条外观行，行内 linkItemID=0 / state=0
//	char 6（test78，不崩）: name "test78" + 13 条外观行，第 3 行 state=3
//
// 两条都按 userinfo_selected_subtype0.go:69-86 的写出顺序生成，尾部是固定 104 字节。
func peerUserInfoIdentityCarrierFixtureForTest(charID uint16, name string, rows int, thirdRowState byte) []byte {
	body := make([]byte, 0, 32+rows*23+104)
	body = append(body, 0x00)                          // subtype0 marker：客户端 mode = body[0] = 0
	body = append(body, 0x01, 0x00)                    // u16 version
	body = append(body, byte(charID), byte(charID>>8)) // u16 charID（低字节落在 mode0 的 route 位）
	body = append(body, byte(len(name)), 0, 0, 0)      // u32 nameLen
	body = append(body, []byte(name)...)
	body = append(body, 0x0f, 0x00, 0x5a) // job / grow / level
	body = append(body, 0x00, 0x00, 0x00) // pvp_grade / pvp_rating_grade / user_state
	body = append(body, byte(rows))       // appearanceCount
	for index := 0; index < rows; index++ {
		state := byte(0)
		if index == 2 {
			state = thirdRowState
		}
		body = append(body, byte(index))            // slot
		body = append(body, 0x32, 0x0b, 0xd4, 0x18) // displayItemID（LE）
		body = append(body, 0x04, 0x00, 0x00, 0x00) // writeUint32(4)
		body = append(body, 0x00, 0x00, 0x00, 0x00) // writeZero(4)
		body = append(body, state)                  // state
		body = append(body, 0x00, 0x00, 0x00, 0x00) // linkItemID
		body = append(body, 0x00, 0x00, 0x00, 0x00) // writeUint32(0)
		body = append(body, 0x00)                   // flag20
	}
	body = append(body, make([]byte, 104)...) // subtype0 固定尾
	return body
}

// TestPeerUserInfoIdentityCarrierMode0GuardDefaultsOff 锁死默认档：
// 未设置/写错/显式 off 都**不预演、不扣包** ⇒ 下发逐字节等于改动前。
func TestPeerUserInfoIdentityCarrierMode0GuardDefaultsOff(t *testing.T) {
	body := peerUserInfoIdentityCarrierFixtureForTest(4, "t3", 9, 0)
	for _, value := range []string{"", "off", "0", "false", "no", "legacy", "whatever"} {
		t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, value)
		if peerUserInfoIdentityCarrierMode0GuardEnabled() {
			t.Fatalf("env=%q must keep the guard disabled", value)
		}
		if walk := peerUserInfoIdentityCarrierMode0GuardSkip(body); walk.Skip {
			t.Fatalf("env=%q must not skip the carrier (skip=true, reason=%s)", value, walk.Reason)
		}
	}
	if got := peerUserInfoIdentityCarrierMode0GuardMode(); got != peerUserInfoIdentityCarrierMode0GuardOff {
		t.Fatalf("default mode = %q, want %q", got, peerUserInfoIdentityCarrierMode0GuardOff)
	}
}

// TestPeerUserInfoIdentityCarrierMode0GuardSkipsTheCrashingShape 是本轮的实机判据：
// char 4 那一族（第 3 条外观行的 linkItemID/state 让客户端 mode0 walk 读出体）必须被扣下。
func TestPeerUserInfoIdentityCarrierMode0GuardSkipsTheCrashingShape(t *testing.T) {
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardOn)
	body := peerUserInfoIdentityCarrierFixtureForTest(4, "t3", 9, 0)
	// 前置：这条体的 mode0 视角字段就是现场那组值（objectKey=0、名字长度=0）。
	if got := peerUserInfoIdentityCarrierMode0WalkFor(body).NameLen; got != 0 {
		t.Fatalf("fixture name length = %d, want 0 (the client reads the third appearance row's linkItemID)", got)
	}
	walk := peerUserInfoIdentityCarrierMode0GuardSkip(body)
	if !walk.Skip {
		t.Fatalf("char-4 shape must be withheld, got skip=false (safe=%t reason=%s)", walk.Safe, walk.Reason)
	}
	if walk.Safe || walk.Reason == "" || walk.Offset <= 0 {
		t.Fatalf("walk must record the first out-of-body step, got %+v", walk)
	}
	if walk.RowCount <= 0 {
		t.Fatalf("walk must have read the bogus row count, got %+v", walk)
	}
	fields := peerUserInfoIdentityCarrierMode0GuardFields(walk, len(body))
	if len(fields) == 0 || fields[0] != "env" {
		t.Fatalf("guard event fields must start with the env name, got %#v", fields)
	}
}

// TestPeerUserInfoIdentityCarrierMode0GuardKeepsTheSafeShape 锁死 char 6 那一族：
// 名字长度本身越界 ⇒ 客户端自己的 dstr 读端停手（现场 425 字节载体 0 次崩溃）
// ⇒ on 档**照旧发出**；只有 strict 档才保守扣下。
func TestPeerUserInfoIdentityCarrierMode0GuardKeepsTheSafeShape(t *testing.T) {
	body := peerUserInfoIdentityCarrierFixtureForTest(6, "test78", 13, 0x03)
	walk := peerUserInfoIdentityCarrierMode0WalkFor(body)
	if walk.Safe || walk.Reason != "mode0_name_length_out_of_body" {
		t.Fatalf("char-6 shape must read as an out-of-body name length, got %+v", walk)
	}
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardOn)
	if guarded := peerUserInfoIdentityCarrierMode0GuardSkip(body); guarded.Skip {
		t.Fatalf("on mode must keep the char-6 shape (reason=%s)", guarded.Reason)
	}
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardStrict)
	if strict := peerUserInfoIdentityCarrierMode0GuardSkip(body); !strict.Skip {
		t.Fatal("strict mode must withhold every carrier whose mode0 walk leaves the body")
	}
}

// TestPeerUserInfoIdentityCarrierMode0GuardShortBodyIsSafe 锁死客户端的门槛：
// 体短于 0x4E 时客户端连 objectKey 都不读（钩子 bodyLength >= 0x4E），不构成 mode0 解析。
func TestPeerUserInfoIdentityCarrierMode0GuardShortBodyIsSafe(t *testing.T) {
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardStrict)
	for _, size := range []int{0, 5, peerUserInfoMode0NameLengthOffset, peerUserInfoMode0NameLengthOffset + 3} {
		body := make([]byte, size)
		if walk := peerUserInfoIdentityCarrierMode0GuardSkip(body); walk.Skip {
			t.Fatalf("body of %d bytes must stay safe, got %+v", size, walk)
		}
	}
}

// TestPeerUserInfoIdentityCarrierMode0GuardWalkTerminates 锁死"表恰好读得下"这一族：
// 名字长度与装备摘要表都落在体内时**不扣包**（今天的正常角色属于这一族）。
func TestPeerUserInfoIdentityCarrierMode0GuardWalkTerminates(t *testing.T) {
	body := make([]byte, peerUserInfoMode0NameLengthOffset+4+6+1+2*(5+4+10))
	// name length = 0
	body[peerUserInfoMode0NameLengthOffset] = 0
	pos := peerUserInfoMode0NameLengthOffset + 4 + peerUserInfoMode0PostNameFixedBytes
	body[pos] = 2 // 两行
	pos++
	for row := 0; row < 2; row++ {
		pos += 5
		body[pos] = 0 // raw length = 0
		pos += 4 + 10
	}
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardStrict)
	if walk := peerUserInfoIdentityCarrierMode0GuardSkip(body); walk.Skip {
		t.Fatalf("an in-body equip summary must not be withheld, got %+v", walk)
	}
}

// peerUserInfoIdentityCarrierMode0GuardFixture 是"看别人"链的端到端夹具：
// 请求方角色 1 看被查看角色 5；被查看方带 4 行外观，第 4 行的 displayItemID 低字节 0x32
// 正好落在客户端 mode0 walk 读的"装备摘要行数"字节上 ⇒ 那次 walk 必然读出体。
func peerUserInfoIdentityCarrierMode0GuardFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, account := range []dnfrepo.AccountRecord{
		{AccountID: "dnf:1", State: "active"},
		{AccountID: "dnf:2", State: "active"},
	} {
		if err := repositories.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	viewer := dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "dnf:1", Name: "viewer", Job: "0", Level: 90}
	// 名字长度必须与现场 t3 同口径（2 字节）：客户端 mode0 walk 读"装备摘要行数"的那个字节
	// 落在第几条外观行上，取决于 subtype0 头长度（5 + 4 + nameLen + 3），而现场崩的那一族
	// 正是 nameLen=2（t3）。
	target := dnfrepo.CharacterRecord{CharacterID: "5", AccountID: "dnf:2", Name: "t5", Job: "14", Level: 90}
	for _, character := range []dnfrepo.CharacterRecord{viewer, target} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	entries := map[string]dnfrepo.EquipmentEntry{}
	for slot := 0; slot <= 3; slot++ {
		entries[strconv.Itoa(slot)] = dnfrepo.EquipmentEntry{
			SlotIndex: int16(slot),
			ItemID:    int64(0x18B40000 + slot*0x101 + 0x32),
		}
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "5", Entries: entries}); err != nil {
		t.Fatal(err)
	}
	sourceConn := &bufferConn{}
	channel := channelcatalog.Channel{ID: 42}
	source := &gameSession{conn: sourceConn, channel: channel, accountID: "dnf:1", selectedCharacterID: 1, townActorOwnerChannel: 42}
	peer := &gameSession{conn: &bufferConn{}, channel: channel, accountID: "dnf:2", selectedCharacterID: 5, townActorOwnerChannel: 42}
	service := &Service{
		options:             options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	service.bindGameSessionCharacter(source, 1)
	service.bindGameSessionCharacter(peer, 5)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 1, AreaID: 2, Session: source})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 1, AreaID: 2, Session: peer})
	return service, source, sourceConn
}

// TestPeerUserInfoIdentityCarrierMode0GuardDropsTheCarrierOnTheWire 是端到端判据：
// 默认档（未设置）逐字节不变（身份载体 + mode3 两条，且不写 guard 判读行）；
// 打开档位后，被判"会读出体"的载体**一个字节都不下发**，只留 mode3 详情体，
// 并在 packet_log 里留下 decision=skip 一位。
func TestPeerUserInfoIdentityCarrierMode0GuardDropsTheCarrierOnTheWire(t *testing.T) {
	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, "")
	defaultService, defaultSource, defaultConn := peerUserInfoIdentityCarrierMode0GuardFixture(t)
	defaultLogged := requestPeerUserInfo(t, defaultService, defaultSource)
	defaultFirst, defaultRest := splitLongHengGameServerUpperPacket(t, defaultConn.write.Bytes())
	if defaultFirst.Body[0] != 0 {
		t.Fatalf("default档 first frame must stay the subtype0 identity carrier, subtype=%#x", defaultFirst.Body[0])
	}
	if _, rest := splitLongHengGameServerUpperPacket(t, defaultRest); len(rest) != 0 {
		t.Fatalf("default档 must send exactly two frames, trailing=%d", len(rest))
	}
	if strings.Contains(defaultLogged, "kind=game-peer-userinfo-identity-carrier-mode0-guard ") {
		t.Fatalf("default档 must not add the guard judgement line\n%s", defaultLogged)
	}

	t.Setenv(peerUserInfoIdentityCarrierMode0GuardEnv, peerUserInfoIdentityCarrierMode0GuardOn)
	guardedService, guardedSource, guardedConn := peerUserInfoIdentityCarrierMode0GuardFixture(t)
	guardedLogged := requestPeerUserInfo(t, guardedService, guardedSource)
	guardedOnly, guardedRest := splitLongHengGameServerUpperPacket(t, guardedConn.write.Bytes())
	if len(guardedRest) != 0 {
		t.Fatalf("guard档 must send exactly one frame, trailing=%d", len(guardedRest))
	}
	if guardedOnly.Body[0] != currentPeerUserInfoMode {
		t.Fatalf("guard档 first frame subtype=%#x, want the mode3 detail body", guardedOnly.Body[0])
	}
	line := ""
	for _, candidate := range strings.Split(guardedLogged, "\n") {
		if strings.Contains(candidate, "kind=game-peer-userinfo-identity-carrier-mode0-guard ") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("guard judgement event missing from packet log\n%s", guardedLogged)
	}
	for _, want := range []string{
		"decision=skip",
		"target_char_id=5",
		"env=" + peerUserInfoIdentityCarrierMode0GuardEnv,
		"switch=" + peerUserInfoIdentityCarrierMode0GuardOn,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("guard judgement line missing %q\n%s", want, line)
		}
	}
	if !strings.Contains(guardedLogged, "identity_carrier_sent=false") {
		t.Fatalf("mode3 response event must report the withheld carrier\n%s", guardedLogged)
	}
}
