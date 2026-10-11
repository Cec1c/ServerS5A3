package dnfbridge

import (
	"context"
	"encoding/hex"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// vendorDimensionGateEntranceBodyHex 是厂商 `DimensionGateEntranceInfoBodyBuilder.Build()`
// 对 `[daily default enter count]=5 / [daily default extra enter count]=0` 会写出的
// 固定 8 字节包体（u32 LE remaining + u32 LE extra）。
const vendorDimensionGateEntranceBodyHex = "0500000000000000"

// TestDimensionGateEntranceBodyMatchesVendorLayout 钉住包体逐字节：
// remaining=5 extra=0 → `05 00 00 00 00 00 00 00`，长度 8。
func TestDimensionGateEntranceBodyMatchesVendorLayout(t *testing.T) {
	body := buildCurrentDimensionGateEntranceInfoBody(
		currentDimensionGateVendorFallbackEnterCount,
		currentDimensionGateVendorFallbackExtraCount,
	)
	if len(body) != currentDimensionGateEntranceBodyLen {
		t.Fatalf("body len=%d, want %d", len(body), currentDimensionGateEntranceBodyLen)
	}
	if got := hex.EncodeToString(body); got != vendorDimensionGateEntranceBodyHex {
		t.Fatalf("body=%s, want %s", got, vendorDimensionGateEntranceBodyHex)
	}
}

// TestDimensionGateEntranceBodyIsLittleEndianU32Pair 用一组区分字节序的值证明
// 两个字段都是 u32 小端（不是大端、不是 u16、不是 4+4 交错）。
func TestDimensionGateEntranceBodyIsLittleEndianU32Pair(t *testing.T) {
	body := buildCurrentDimensionGateEntranceInfoBody(0x01020304, 0x05060708)
	if got := hex.EncodeToString(body); got != "0403020108070605" {
		t.Fatalf("body=%s, want 0403020108070605", got)
	}
}

// TestDimensionGateEntranceBodyClampsNegativeCounts 覆盖厂商 `Math.Max(0, ·)`：
// 负数档必须逐字节写成 0，不能出现二进制补码。
func TestDimensionGateEntranceBodyClampsNegativeCounts(t *testing.T) {
	cases := []struct {
		name      string
		remaining int
		extra     int
		want      string
	}{
		{name: "both_negative", remaining: -1, extra: -7, want: "0000000000000000"},
		{name: "remaining_negative_only", remaining: -2147483648, extra: 3, want: "0000000003000000"},
		{name: "extra_negative_only", remaining: 5, extra: -1, want: "0500000000000000"},
		{name: "zero_pair", remaining: 0, extra: 0, want: "0000000000000000"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := buildCurrentDimensionGateEntranceInfoBody(test.remaining, test.extra)
			if len(body) != currentDimensionGateEntranceBodyLen {
				t.Fatalf("body len=%d, want %d", len(body), currentDimensionGateEntranceBodyLen)
			}
			if got := hex.EncodeToString(body); got != test.want {
				t.Fatalf("body=%s, want %s", got, test.want)
			}
		})
	}
}

// TestDimensionGateEntranceSwitchOffMatchesLegacyNoSend 钉住一键回退：开关取
// off/0/false/no/legacy/disable/disabled 时必须**逐字节回到"不发"**（改动前行为），
// 一个字节都不能写。
func TestDimensionGateEntranceSwitchOffMatchesLegacyNoSend(t *testing.T) {
	for _, value := range []string{"off", "0", "false", "no", "legacy", "disable", "disabled", "OFF", " off "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(currentDimensionGateEntranceEnv, value)
			if currentDimensionGateEntranceEnabled() {
				t.Fatalf("switch %q must disable the sender", value)
			}
			connection := &bufferConn{}
			session := &gameSession{
				conn:                connection,
				connID:              "dimension-gate-switch-off",
				selectedCharacterID: 88,
			}
			service := &Service{options: options{
				gameUpperBodyCodec: gameUpperBodyCodecPlain,
			}}
			if err := service.sendCurrentDimensionGateEntranceInfo(session, "test_switch_off"); err != nil {
				t.Fatalf("switch %q send: %v", value, err)
			}
			if connection.write.Len() != 0 {
				t.Fatalf("switch %q wrote %x, want zero bytes", value, connection.write.Bytes())
			}
		})
	}
	// 默认档（未设置开关）必须是"发"：修之前是完全不发，不发就等于没修。
	t.Setenv(currentDimensionGateEntranceEnv, "")
	if !currentDimensionGateEntranceEnabled() {
		t.Fatal("unset switch must keep the sender enabled")
	}
}

// TestDimensionGateEntranceFallsBackToVendorDefaultWhenPVFUnavailable 覆盖"读不到 PVF
// 就用厂商 fallback 5/0"这条分支的端到端字节：包体仍必须是 0500000000000000，
// classification=0，msg_id=厂商 0x0326（806）。
func TestDimensionGateEntranceFallsBackToVendorDefaultWhenPVFUnavailable(t *testing.T) {
	t.Setenv(currentDimensionGateEntranceEnv, "")
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "dimension-gate-fallback",
		selectedCharacterID: 88,
	}
	service := &Service{options: options{
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
		// 指向不存在的 PVF，强制走 fallback 分支。
		pvfPath: "testdata/does-not-exist-dimension-gate.pvf",
	}}
	limit := service.currentDimensionGateEntryLimitForSession()
	if limit.enterCount != currentDimensionGateVendorFallbackEnterCount ||
		limit.extraCount != currentDimensionGateVendorFallbackExtraCount {
		t.Fatalf("fallback limit=%d/%d, want %d/%d",
			limit.enterCount, limit.extraCount,
			currentDimensionGateVendorFallbackEnterCount, currentDimensionGateVendorFallbackExtraCount)
	}
	if limit.source != currentDimensionGateEntryLimitSourceFallback {
		t.Fatalf("fallback body_source=%q, want %q", limit.source, currentDimensionGateEntryLimitSourceFallback)
	}

	if err := service.sendCurrentDimensionGateEntranceInfo(session, "test_fallback"); err != nil {
		t.Fatalf("send: %v", err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("trailing bytes after packet: %x", trailing)
	}
	if packet.Header.MsgID != currentDimensionGateEntranceMsgID || packet.Header.Classification != 0 {
		t.Fatalf("packet header=%+v, want msg_id=%d class=0", packet.Header, currentDimensionGateEntranceMsgID)
	}
	if got := hex.EncodeToString(packet.Body); got != vendorDimensionGateEntranceBodyHex {
		t.Fatalf("packet body=%s, want %s", got, vendorDimensionGateEntranceBodyHex)
	}
}

// TestDimensionGateEntranceMsgIDIsVendorHexNotOurEnum 钉住 opcode 依据：厂商
// `PacketTypesA21.cs:813 DIMENSION_GATE_ENTRANCE_INFO = 0x0326` = 十进制 806。
// 我方 `dnfenum` 在 0x032x 区段整体偏移 +1~+2，所以这里断言的是**裸数字**，
// 不是任何我方枚举名。
func TestDimensionGateEntranceMsgIDIsVendorHexNotOurEnum(t *testing.T) {
	if currentDimensionGateEntranceMsgID != 0x0326 {
		t.Fatalf("opcode=0x%04X, want vendor hex 0x0326", currentDimensionGateEntranceMsgID)
	}
	if currentDimensionGateEntranceMsgID != 806 {
		t.Fatalf("opcode decimal=%d, want vendor decimal 806", currentDimensionGateEntranceMsgID)
	}
}

// TestParseDimensionGateEntryLimitReadsVendorTags 覆盖 PVF 解析：tag 名照抄厂商
// `DimensionGateEntryLimitConfigProvider.cs:48,53`，取每个 tag 的**第一个整数**，
// 读不到就回落 5/0。真档 `etc/dimensiongate.etc` 的内容形状取自线上服务端同源副本。
func TestParseDimensionGateEntryLimitReadsVendorTags(t *testing.T) {
	realShape := "" +
		"[dungeon reset type]\n-1 \n[/dungeon reset type]\n" +
		"[daily enter count limit]\n-1 \n" +
		"[daily default enter count]\n5 \n" +
		"[daily default extra enter count]\n0 \n" +
		"[levelup reset type]\n0 \n"
	limit, err := parseCurrentDimensionGateEntryLimit(realShape)
	if err != nil {
		t.Fatalf("parse real shape: %v", err)
	}
	if limit.enterCount != 5 || limit.extraCount != 0 {
		t.Fatalf("real shape limit=%d/%d, want 5/0", limit.enterCount, limit.extraCount)
	}
	if limit.source != currentDimensionGateEntryLimitSourceVendorPVF || limit.tagsFound != 2 {
		t.Fatalf("real shape source=%q tags=%d, want %q/2",
			limit.source, limit.tagsFound, currentDimensionGateEntryLimitSourceVendorPVF)
	}

	// 真值不是硬编码 5/0：另一个 PVF 档给 7/2 就必须读出 7/2。
	custom, err := parseCurrentDimensionGateEntryLimit(
		"[daily default enter count]\n7\n[daily default extra enter count]\n2\n",
	)
	if err != nil {
		t.Fatalf("parse custom: %v", err)
	}
	if custom.enterCount != 7 || custom.extraCount != 2 {
		t.Fatalf("custom limit=%d/%d, want 7/2", custom.enterCount, custom.extraCount)
	}
	if got := hex.EncodeToString(buildCurrentDimensionGateEntranceInfoBody(custom.enterCount, custom.extraCount)); got != "0700000002000000" {
		t.Fatalf("custom body=%s, want 0700000002000000", got)
	}

	// 缺 tag → 厂商 fallback 5/0；PVF 里的负值同样被 Math.Max(0, ·) 夹到 0。
	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{name: "no_tags", text: "[dungeon reset type]\n-1\n[/dungeon reset type]\n", want: "5/0"},
		{name: "empty", text: "   \n", want: "5/0"},
		{name: "negative_enter", text: "[daily default enter count]\n-3\n[daily default extra enter count]\n-4\n", want: "0/0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, _ := parseCurrentDimensionGateEntryLimit(test.text)
			got := hex.EncodeToString(buildCurrentDimensionGateEntranceInfoBody(parsed.enterCount, parsed.extraCount))
			want := map[string]string{"5/0": "0500000000000000", "0/0": "0000000000000000"}[test.want]
			if got != want {
				t.Fatalf("body=%s, want %s (%s)", got, want, test.want)
			}
		})
	}
}

// newDimensionGateEntranceRouteFixture 复用 initial_town_transition_order_test.go
// 里已被验证过的登录进城 fixture（同一条 armCurrentInitialTownRoute + 延迟场景尾路由），
// 目的只是把真实的 init 包序列打出来做相对位置断言。
func newDimensionGateEntranceRouteFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	service, session, repositories := newTownMoveTest(t)
	ctx := context.Background()
	skillCatalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst":    "0 `job0.lst`\n",
		"skill/job0.lst":         "46 `job0/initial.skl`\n",
		"skill/job0/initial.skl": "[skill type]\n`active`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.initialSkillsByJob = map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}}
	service.initialSPTable = map[int]int{90: 3710}
	service.initialTPTable = map[int]int{90: 41}
	service.skillCatalog = skillCatalog
	service.questCatalog = buildQuestListTestCatalog(t)
	service.options.accountPrefix = defaultAccountPrefix
	service.adventureGroupTable = loadAdventureGroupTestTables(t)
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: defaultAccountPrefix + "1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "0"},
	}); err != nil {
		t.Fatal(err)
	}
	character, found, err := repositories.Character.Load(ctx, "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 90
	character.Stats[currentDungeonTutorialCompletedKey] = currentDungeonTutorialCompleteFlag
	character.Stats[currentOpenAuraSkinSlotFlagStat] = 1
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	session.selectedCharacterID = 29
	session.townSceneReadyCharacterID = 0
	session.sceneBootstrapTailDeferred = true
	session.channel = channelcatalog.Channel{
		ServerID: 1,
		ID:       11,
		Type:     1,
		Name:     "ch.11",
		Port:     10011,
	}
	session.residentChannel = session.channel
	service.armCurrentInitialTownRoute(session, 29)
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	return service, session, connection
}

// TestDimensionGateEntranceIsSentInsideInitialTownInitSequence 是接线位置断言：
// 在真实的登录进城 init 包序列里，
//
//	op1303（raid daily availability） < **NOTI 0x0326 DimensionGateEntranceInfo** < 定型 op24
//
// 即它是 init 窗口内的包、而且正好发在 op1303 兄弟包之后、城镇提交之前；
// 整条流里只能出现一次，且包体是厂商 8 字节默认档 0500000000000000。
func TestDimensionGateEntranceIsSentInsideInitialTownInitSequence(t *testing.T) {
	t.Setenv(currentDimensionGateEntranceEnv, "")
	service, session, connection := newDimensionGateEntranceRouteFixture(t)

	if err := service.sendDeferredSelectSceneTail(session, "dimension_gate_order_test"); err != nil {
		t.Fatalf("resume scene tail: %v", err)
	}
	if connection.write.Len() == 0 {
		t.Fatal("scene tail wrote no packets")
	}

	const raidDailyAvailabilityMsgID uint16 = 1303
	dimensionGateCount := 0
	dimensionGateIndex := -1
	raidDailyIndex := -1
	transitionIndex := -1
	index := 0
	for stream := connection.write.Bytes(); len(stream) > 0; index++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		switch {
		case packet.Header.Classification == 0 && packet.Header.MsgID == raidDailyAvailabilityMsgID:
			raidDailyIndex = index
		case packet.Header.Classification == 0 && packet.Header.MsgID == currentDimensionGateEntranceMsgID:
			dimensionGateCount++
			dimensionGateIndex = index
			if got := hex.EncodeToString(packet.Body); got != vendorDimensionGateEntranceBodyHex {
				t.Fatalf("dimension gate body=%s, want %s", got, vendorDimensionGateEntranceBodyHex)
			}
		case packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID:
			if transitionIndex < 0 {
				transitionIndex = index
			}
		}
	}
	if dimensionGateCount != 1 {
		t.Fatalf("dimension gate packets=%d index=%d, want exactly 1", dimensionGateCount, dimensionGateIndex)
	}
	if raidDailyIndex < 0 {
		t.Fatal("op1303 raid daily availability was not sent; fixture no longer matches the route")
	}
	if dimensionGateIndex <= raidDailyIndex {
		t.Fatalf("dimension gate index=%d must follow op1303 index=%d", dimensionGateIndex, raidDailyIndex)
	}
	if transitionIndex < 0 {
		t.Fatal("typed op24 transition was not sent")
	}
	if dimensionGateIndex >= transitionIndex {
		t.Fatalf("dimension gate index=%d must precede typed op24 index=%d", dimensionGateIndex, transitionIndex)
	}
	t.Logf("init sequence order: op1303 index=%d < dimension_gate index=%d < typed_op24 index=%d (total packets=%d)",
		raidDailyIndex, dimensionGateIndex, transitionIndex, index)
}

// TestRealPVFDimensionGateEntranceUsesRealConfig 真档用例（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：
// 用线上服务端档的同源副本 Script.pvf 走完整链路，必须读到 `etc/dimensiongate.etc`
// 而不是回落 fallback，且 `[daily default enter count]=5 / [... extra enter count]=0`。
func TestRealPVFDimensionGateEntranceUsesRealConfig(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to inspect runtime etc/dimensiongate.etc")
	}
	t.Setenv(currentDimensionGateEntranceEnv, "")
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "dimension-gate-real-pvf",
		selectedCharacterID: 88,
	}
	service := &Service{options: options{
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
		pvfPath:            pvfPath,
	}}

	limit := service.currentDimensionGateEntryLimitForSession()
	if limit.pvfStatus != currentDimensionGatePVFStatusReadOK {
		t.Fatalf("real PVF status=%q, want %q", limit.pvfStatus, currentDimensionGatePVFStatusReadOK)
	}
	if limit.source != currentDimensionGateEntryLimitSourceVendorPVF || limit.tagsFound != 2 {
		t.Fatalf("real PVF source=%q tags=%d, want %q/2",
			limit.source, limit.tagsFound, currentDimensionGateEntryLimitSourceVendorPVF)
	}
	if limit.enterCount != 5 || limit.extraCount != 0 {
		t.Fatalf("real PVF limit=%d/%d, want the vendor 5/0", limit.enterCount, limit.extraCount)
	}

	if err := service.sendCurrentDimensionGateEntranceInfo(session, "real_pvf_smoke"); err != nil {
		t.Fatalf("send with real PVF: %v", err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("trailing bytes after packet: %x", trailing)
	}
	if packet.Header.MsgID != currentDimensionGateEntranceMsgID || packet.Header.Classification != 0 {
		t.Fatalf("packet header=%+v, want msg_id=%d class=0", packet.Header, currentDimensionGateEntranceMsgID)
	}
	if got := hex.EncodeToString(packet.Body); got != vendorDimensionGateEntranceBodyHex {
		t.Fatalf("real PVF packet body=%s, want %s", got, vendorDimensionGateEntranceBodyHex)
	}
	t.Logf("real PVF dimension gate: path=%s status=%s body_source=%s remaining=%d extra=%d body=%s",
		limit.pvfPath, limit.pvfStatus, limit.source, limit.enterCount, limit.extraCount, hex.EncodeToString(packet.Body))
}
