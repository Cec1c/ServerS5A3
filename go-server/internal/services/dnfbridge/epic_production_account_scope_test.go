package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 回归钉子：自制史诗 NPC（史诗制作者）"选择武器"链路的账号口径。
//
// 现场与逐字段证据见 epic_production_account_scope.go 文件头。这里钉两件事：
//
//  1. **会话账号优先**：香港那台服务器 `DNFBRIDGE_ACCOUNT_ID=dnf:1`（共享兜底账号），
//     而操作者的会话账号是 `dnf:10001`（class1/op1 登录包里的启动器令牌解出来的）、
//     角色 3 的 `account_id` 也是 `dnf:10001`。改动前归属校验拿 `s.accountID()`
//     （= dnf:1）去比 ⇒ 恒不匹配 ⇒ 回 `00 03` ⇒ 客户端弹「用户信息错误」。
//  2. **失败形状不变**（参考同形）：非法请求/越权请求仍然回 u8 0 + 失败码 3，
//     即 wire body 恰好 `00 03`（与现场 SEND hex
//     `018905120000007a3d5fad50000000000003` 的尾 2 字节逐字节相同）。

const (
	// epicProductionSharedAccount 是香港线上 `DNFBRIDGE_ACCOUNT_ID` 的取值。
	epicProductionSharedAccount = "dnf:1"
	// epicProductionSessionAccount 是现场那条会话（conn_id=game-000016）绑定的账号。
	epicProductionSessionAccount = "dnf:10001"
	// epicProductionLiveTargetItemID 是操作者选的那把武器：108000520 = 席蒿待罪（十字架），
	// 真 PVF `[at priest]`（job 14 = Priest/ATPriest.chr）武器清单第 1 项。
	epicProductionLiveTargetItemID = uint32(108000520)
	// epicProductionTestStatSeed 是测试用角色 stats 的非空种子（空 map 会被仓储归一成 nil）。
	epicProductionTestStatSeed = "epic_production_account_scope_test_seed"
)

// epicProductionVendor916InfoBodyHex 是厂商 9.16 原版（D:\DNF90-test 的 DNF90Server.exe，
// 清单 sha256 23DF8D1C…DF94）在 `packet_log.txt` 里发出的 op1355 body（去掉 16 字节
// class0 头）。两条样本分别是 character_id=1（:2701）与 character_id=2（:3084），
// 两条的事件都自述 `target_item_id=0 charge_point=0 weekly_remaining_count=7`
// ⇒ 厂商在**还没选武器**时照发，且 body = u32 角色号 + 28 字节状态。
const (
	epicProductionVendor916InfoPacketHexChar1 = "004b0530000000fc33a5e62d000000000100000007000000000000000000000000000000000000000000000000000000"
	epicProductionVendor916InfoPacketHexChar2 = "004b05300000002f6b86842d000000000200000007000000000000000000000000000000000000000000000000000000"
)

func epicProductionVendor916InfoBody(t *testing.T, packetHex string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(packetHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 48 {
		t.Fatalf("vendor op1355 packet length=%d want=48", len(raw))
	}
	if raw[0] != 0 || binary.LittleEndian.Uint16(raw[1:3]) != 1355 ||
		binary.LittleEndian.Uint32(raw[3:7]) != 48 {
		t.Fatalf("vendor op1355 header=%x", raw[:7])
	}
	return raw[16:]
}

// TestEpicProductionAccountScopePrefersSessionAccount 钉"账号口径"本身：
// 会话账号优先；识别不出会话账号才退回共享兜底账号；回退档逐值回到改动前。
func TestEpicProductionAccountScopePrefersSessionAccount(t *testing.T) {
	service := &Service{options: options{accountID: epicProductionSharedAccount}}

	tokenBound := &gameSession{accountID: epicProductionSessionAccount}
	if got := service.epicProductionAccountIDForSession(tokenBound); got != epicProductionSessionAccount {
		t.Fatalf("session scope account=%q want=%q", got, epicProductionSessionAccount)
	}
	// 零值 options 的测试/探针路径同样是 session 档：会话账号优先，
	// 只有会话账号识别不出时才落到 `accountID()` 的兜底账号（dnf:1）。
	if got := (&Service{}).epicProductionAccountIDForSession(tokenBound); got != epicProductionSessionAccount {
		t.Fatalf("zero-value options with token-bound session account=%q want=%q", got, epicProductionSessionAccount)
	}
	unidentified := &gameSession{}
	if got := service.epicProductionAccountIDForSession(unidentified); got != epicProductionSharedAccount {
		t.Fatalf("unidentified session account=%q want=%q", got, epicProductionSharedAccount)
	}
	if got := (&Service{}).epicProductionAccountIDForSession(unidentified); got != epicProductionSharedAccount {
		t.Fatalf("zero-value options fallback account=%q want=%q", got, epicProductionSharedAccount)
	}

	legacy := &Service{options: options{
		accountID:                  epicProductionSharedAccount,
		epicProductionAccountScope: epicProductionAccountScopeShared,
	}}
	if got := legacy.epicProductionAccountIDForSession(tokenBound); got != epicProductionSharedAccount {
		t.Fatalf("shared scope account=%q want=%q", got, epicProductionSharedAccount)
	}
	if legacy.epicProductionAccountScope().String() != epicProductionAccountScopeValueShared ||
		service.epicProductionAccountScope().String() != epicProductionAccountScopeValueSession {
		t.Fatalf("account scope labels session=%q shared=%q",
			service.epicProductionAccountScope().String(), legacy.epicProductionAccountScope().String())
	}
	if mode := readEpicProductionAccountScopeMode(" SHARED "); mode != epicProductionAccountScopeShared {
		t.Fatalf("read shared mode=%v", mode)
	}
	for _, raw := range []string{"", "session", "bogus"} {
		if mode := readEpicProductionAccountScopeMode(raw); mode != epicProductionAccountScopeSession {
			t.Fatalf("read mode(%q)=%v want session", raw, mode)
		}
	}
}

// TestEpicProductionSessionOwnsCharacterRejectsForeignAndUnresolvedAccounts 钉归属判定：
// 越权与"会话账号未定案"都必须**拒绝**（fail-closed），且给出可判读原因。
func TestEpicProductionSessionOwnsCharacterRejectsForeignAndUnresolvedAccounts(t *testing.T) {
	service := &Service{options: options{accountID: epicProductionSharedAccount}}
	character := dnfrepo.CharacterRecord{CharacterID: "3", AccountID: epicProductionSessionAccount}

	if ok, reason := service.epicProductionSessionOwnsCharacter(character, epicProductionSessionAccount); !ok || reason != "" {
		t.Fatalf("owner ok=%t reason=%q", ok, reason)
	}
	if ok, reason := service.epicProductionSessionOwnsCharacter(character, "dnf:10005"); ok || reason != "character_owner_mismatch" {
		t.Fatalf("foreign ok=%t reason=%q", ok, reason)
	}
	if ok, reason := service.epicProductionSessionOwnsCharacter(character, "  "); ok || reason != "session_account_unresolved" {
		t.Fatalf("unresolved ok=%t reason=%q", ok, reason)
	}
	if ok, reason := service.epicProductionSessionOwnsCharacter(dnfrepo.CharacterRecord{CharacterID: "3"}, epicProductionSessionAccount); ok ||
		reason != "character_owner_mismatch" {
		t.Fatalf("ownerless character ok=%t reason=%q", ok, reason)
	}
}

// newEpicProductionStartTestService 造一个只到"账号归属"为止的处理器夹具：
// 仓储是内存实现，PVF 故意指向不存在的文件（`options.pvfPath`） —— 于是
// **过了账号归属校验**之后必然停在"读 PVF 失败"那一支，日志里能直接看出
// 处理器到底走没走过归属校验（改动前会写 `reason=dnf epic production request is invalid`）。
func newEpicProductionStartTestService(t *testing.T, character dnfrepo.CharacterRecord) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	// 空 Stats map 会被仓储的深拷贝归一成 nil（repository.cloneInt64Map），
	// 于是 op1355 那条路径会在 `character.Stats == nil` 上提前返回；这里给一个非空种子。
	if len(character.Stats) == 0 {
		character.Stats = map[string]int64{epicProductionTestStatSeed: 1}
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID: epicProductionSharedAccount,
			pvfPath:   filepath.Join(t.TempDir(), "missing-Script.pvf"),
			// 与线上同一档头部（香港 packet_log 里 class1/op1417 的 SEND len=18）。
			gameUpperHeader: gameUpperHeaderServer16,
		},
		packetLog: logger,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	session := &gameSession{conn: connection, selectedCharacterID: 3, accountID: character.AccountID}
	return service, session, connection, logger, logPath
}

func epicProductionStartRequestBody(t *testing.T, targetItemID uint32) []byte {
	t.Helper()
	body := make([]byte, currentEpicProductionStartRequestSize)
	binary.LittleEndian.PutUint32(body, targetItemID)
	return body
}

// assertEpicProductionReferenceFailurePacket 钉"参考同形失败"：class1 / msg_id=1417，
// body 恰好 2 字节 `00 03`，整包 18 字节 —— 与现场 SEND
// `018905120000007a3d5fad50000000000003`（len=18，尾两字节 `00 03`）同形。
func assertEpicProductionReferenceFailurePacket(t *testing.T, connection *bufferConn) {
	t.Helper()
	raw := connection.write.Bytes()
	packet, rest := splitLongHengGameServerUpperPacket(t, raw)
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes=%d", len(rest))
	}
	if packet.Header.MsgID != uint16(1417) || packet.Header.Classification != 1 {
		t.Fatalf("failure header msg=%d class=%d", packet.Header.MsgID, packet.Header.Classification)
	}
	if !bytes.Equal(packet.Body, []byte{0, currentEpicProductionGenericError}) {
		t.Fatalf("failure body=%x want=00%02x", packet.Body, currentEpicProductionGenericError)
	}
	if len(raw) != 18 || !strings.HasSuffix(hex.EncodeToString(raw), "0003") {
		t.Fatalf("failure packet len=%d hex=%s want len=18 tail=0003", len(raw), hex.EncodeToString(raw))
	}
}

// TestHandleCurrentEpicProductionStartPassesAccountGateForTokenBoundSession 是主回归：
// 共享兜底账号 dnf:1 的服务器上，令牌绑定的会话 dnf:10001 选自己名下的武器，
// **不许**再被判成"越权"。改动前这一支写 `reason=dnf epic production request is invalid`
// 并回 `00 03`（= 现场「用户信息错误」）。
func TestHandleCurrentEpicProductionStartPassesAccountGateForTokenBoundSession(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   epicProductionSessionAccount,
		Job:         "14",
		Stats:       map[string]int64{},
	}
	service, session, connection, logger, logPath := newEpicProductionStartTestService(t, character)

	if err := service.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
		t.Fatal(err)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)

	// 账号归属校验必须已经放行：不再出现"请求非法"这条与 PVF 无关的通用原因。
	if strings.Contains(logged, "reason="+errCurrentEpicProductionInvalid.Error()) {
		t.Fatalf("account gate still rejected the token-bound session:\n%s", logged)
	}
	for _, forbidden := range []string{"reason=character_owner_mismatch", "reason=character_load_failed", "reason=session_account_unresolved"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("unexpected owner rejection %q:\n%s", forbidden, logged)
		}
	}
	// 夹具里没有 PVF，所以放行之后必然停在这一支：日志能证明"确实走过了归属校验"。
	if !strings.Contains(logged, "game-upper-epic-production-start-blocked") ||
		!strings.Contains(logged, "character_id=3") {
		t.Fatalf("expected the post-gate PVF failure event:\n%s", logged)
	}
	// 失败形状仍是参考同形（含 PVF 不可用这种服务端侧失败）。
	assertEpicProductionReferenceFailurePacket(t, connection)
}

// TestHandleCurrentEpicProductionStartStillBlocksForeignCharacter 钉安全边界没被放松：
// 别人的角色照样拒绝，且回包仍是 `00 03`。
func TestHandleCurrentEpicProductionStartStillBlocksForeignCharacter(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   epicProductionSessionAccount,
		Job:         "14",
		Stats:       map[string]int64{},
	}
	service, session, connection, logger, logPath := newEpicProductionStartTestService(t, character)
	session.accountID = "dnf:10005"

	if err := service.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
		t.Fatal(err)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "reason=character_owner_mismatch") ||
		!strings.Contains(logged, "character_account="+epicProductionSessionAccount) ||
		!strings.Contains(logged, "session_account=dnf:10005") {
		t.Fatalf("owner mismatch diagnostics missing:\n%s", logged)
	}
	assertEpicProductionReferenceFailurePacket(t, connection)
}

// TestHandleCurrentEpicProductionStartSharedScopeKillSwitch 钉一键回退：
// `DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE=shared` 必须逐字节回到改动前行为
// （拿共享兜底账号比对 ⇒ 令牌绑定的会话又被判越权 ⇒ `00 03`）。
func TestHandleCurrentEpicProductionStartSharedScopeKillSwitch(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   epicProductionSessionAccount,
		Job:         "14",
		Stats:       map[string]int64{},
	}
	service, session, connection, logger, logPath := newEpicProductionStartTestService(t, character)
	service.options.epicProductionAccountScope = epicProductionAccountScopeShared

	if err := service.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
		t.Fatal(err)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "reason=character_owner_mismatch") ||
		!strings.Contains(logged, "account_scope=shared") {
		t.Fatalf("kill switch did not restore the legacy account scope:\n%s", logged)
	}
	assertEpicProductionReferenceFailurePacket(t, connection)
}

// TestHandleCurrentEpicProductionStartMalformedRequestKeepsReferenceFailureShape 钉
// "非法请求也是参考同形失败"：body 长度不对（3 字节）、目标武器为 0（4 字节全零）
// 都回同一条 `00 03`，绝不弹别的形状。
func TestHandleCurrentEpicProductionStartMalformedRequestKeepsReferenceFailureShape(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   epicProductionSessionAccount,
		Job:         "14",
		Stats:       map[string]int64{},
	}
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "short body", body: []byte{0x08, 0xf5, 0x6f}},
		{name: "zero target item", body: make([]byte, currentEpicProductionStartRequestSize)},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, connection, _, _ := newEpicProductionStartTestService(t, character)
			if err := service.handleCurrentEpicProductionStart(session, test.body); err != nil {
				t.Fatal(err)
			}
			assertEpicProductionReferenceFailurePacket(t, connection)
		})
	}
}

// TestBuildCurrentEpicProductionInfoBodyMatchesVendor916Fixture 是**厂商夹具**钉子：
// 我方 op1355 的 body 必须与厂商 9.16 原版在参考环境里发的那两条逐字节相同，
// 且整包的前 7 字节（class=0 / type=1355 / len=48）也必须相同。
func TestBuildCurrentEpicProductionInfoBodyMatchesVendor916Fixture(t *testing.T) {
	for _, test := range []struct {
		name         string
		packetHex    string
		characterID  uint16
		remaining    uint32
		targetItemID uint32
		chargePoint  uint32
	}{
		{
			name:         "vendor char 1",
			packetHex:    epicProductionVendor916InfoPacketHexChar1,
			characterID:  1,
			remaining:    7,
			targetItemID: 0,
			chargePoint:  0,
		},
		{
			name:         "vendor char 2",
			packetHex:    epicProductionVendor916InfoPacketHexChar2,
			characterID:  2,
			remaining:    7,
			targetItemID: 0,
			chargePoint:  0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := epicProductionVendor916InfoBody(t, test.packetHex)
			got := buildCurrentEpicProductionInfoBody(test.characterID, test.targetItemID, test.chargePoint, test.remaining, 0, 0)
			if !bytes.Equal(got, want) {
				t.Fatalf("op1355 body=%x want(厂商)=%x", got, want)
			}

			connection := &bufferConn{}
			service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16}}
			session := &gameSession{conn: connection, selectedCharacterID: test.characterID}
			if err := service.sendGameUpperRawClass(session, currentEpicProductionInfoMsgID, got, 0); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
			if len(rest) != 0 {
				t.Fatalf("unexpected trailing bytes=%d", len(rest))
			}
			if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(1355) {
				t.Fatalf("op1355 header class=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
			}
			// 厂商那条 op1355 整包就是 48 字节（16 字节 class0 头 + 32 字节 body）。
			if got := connection.write.Len(); got != 48 {
				t.Fatalf("op1355 packet len=%d want=48 (厂商夹具)", got)
			}
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("op1355 wire body=%x want(厂商)=%x", packet.Body, want)
			}
		})
	}
}

// TestSendSelectedCurrentEpicProductionInfoPushModeShortCircuit 钉 op1355 的第二处修法：
// 默认档（always = 厂商形状）在**还没选武器**时也要往下走（夹具里没有 PVF，于是必然
// 走到"目录不可用"并降级为一条 `game-upper-epic-production-info-skipped` 事件，
// **不打断进城链路**）；回退档 `selected_only` 才在 target=0 时提前返回、什么都不发。
func TestSendSelectedCurrentEpicProductionInfoPushModeShortCircuit(t *testing.T) {
	newService := func(mode epicProductionInfoPushMode) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
		repositories := dnfrepomemory.NewMemoryGroup()
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: "3",
			AccountID:   epicProductionSessionAccount,
			Job:         "14",
			Stats:       map[string]int64{epicProductionTestStatSeed: 1},
		}); err != nil {
			t.Fatal(err)
		}
		logger, logPath := newPeerEndpointTestLogger(t)
		t.Cleanup(func() { _ = logger.Close() })
		connection := &bufferConn{}
		service := &Service{
			options: options{
				accountID:              epicProductionSharedAccount,
				pvfPath:                filepath.Join(t.TempDir(), "missing-Script.pvf"),
				epicProductionInfoPush: mode,
				gameUpperHeader:        gameUpperHeaderServer16,
			},
			packetLog: logger,
			repositoryProvider: func() (dnfrepo.Group, bool) {
				return repositories, true
			},
		}
		return service, &gameSession{conn: connection, selectedCharacterID: 3, accountID: epicProductionSessionAccount}, connection, logger, logPath
	}

	alwaysService, alwaysSession, alwaysConn, alwaysLogger, alwaysLogPath := newService(epicProductionInfoPushAlways)
	if err := alwaysService.sendSelectedCurrentEpicProductionInfo(alwaysSession, "test"); err != nil {
		t.Fatalf("always mode must not fail town entry when the PVF catalog is unavailable: %v", err)
	}
	alwaysLog := readPeerEndpointTestLog(t, alwaysLogger, alwaysLogPath)
	if !strings.Contains(alwaysLog, "game-upper-epic-production-info-skipped") ||
		!strings.Contains(alwaysLog, "info_push=always") {
		t.Fatalf("always mode did not reach the PVF stage (legacy target<=0 short-circuit still active):\n%s", alwaysLog)
	}
	if alwaysConn.write.Len() != 0 {
		t.Fatalf("always mode wrote %d bytes without a PVF", alwaysConn.write.Len())
	}

	selectedService, selectedSession, selectedConn, selectedLogger, selectedLogPath := newService(epicProductionInfoPushSelectedOnly)
	if err := selectedService.sendSelectedCurrentEpicProductionInfo(selectedSession, "test"); err != nil {
		t.Fatalf("selected_only mode err=%v", err)
	}
	selectedLog := readPeerEndpointTestLog(t, selectedLogger, selectedLogPath)
	if strings.Contains(selectedLog, "game-upper-epic-production-info-skipped") {
		t.Fatalf("selected_only mode must short-circuit before the PVF stage:\n%s", selectedLog)
	}
	if selectedConn.write.Len() != 0 {
		t.Fatalf("selected_only mode wrote %d bytes", selectedConn.write.Len())
	}
	if selectedService.epicProductionInfoPush().String() != epicProductionInfoPushValueSelectedOnly ||
		alwaysService.epicProductionInfoPush().String() != epicProductionInfoPushValueAlways {
		t.Fatalf("push mode labels always=%q selected=%q",
			alwaysService.epicProductionInfoPush().String(), selectedService.epicProductionInfoPush().String())
	}
	if mode := readEpicProductionInfoPushMode(" SELECTED_ONLY "); mode != epicProductionInfoPushSelectedOnly {
		t.Fatalf("read selected_only mode=%v", mode)
	}
	for _, raw := range []string{"", "always", "bogus"} {
		if mode := readEpicProductionInfoPushMode(raw); mode != epicProductionInfoPushAlways {
			t.Fatalf("read mode(%q)=%v want always", raw, mode)
		}
	}
}

// 下面两条走**真 PVF**（`DNFBRIDGE_REAL_PVF_SMOKE` 指向 runtime\data\dnf\Script.pvf），
// 复现操作者那一次点击的完整链路：job 14（`[at priest]`）+ 席蒿待罪 108000520。

func newCurrentEpicProductionRealPVFService(t *testing.T, accountID string, scope epicProductionAccountScopeMode, push epicProductionInfoPushMode) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   accountID,
		Job:         "14",
		Level:       90,
		Stats:       map[string]int64{epicProductionTestStatSeed: 1},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	logger, _ := newPeerEndpointTestLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	service := &Service{
		options: options{
			accountID:                  epicProductionSharedAccount,
			epicProductionAccountScope: scope,
			epicProductionInfoPush:     push,
			gameUpperHeader:            gameUpperHeaderServer16,
		},
		packetLog:               logger,
		initialEquipmentArchive: archive,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	return service, &gameSession{conn: connection, selectedCharacterID: 3, accountID: accountID}, connection
}

// TestCurrentEpicProductionRealPVFWeaponSelectionUsesSessionAccount 是操作者现场的
// **端到端复现**：真 PVF、真实武器 108000520（席蒿待罪）、角色属于 dnf:10001，
// 而服务端共享兜底账号是 dnf:1。修好后应当成功选中，回包 body =
// u8 1 + u32 武器号 + u32 序号 0，并把目标写进角色 stats。
func TestCurrentEpicProductionRealPVFWeaponSelectionUsesSessionAccount(t *testing.T) {
	service, session, connection := newCurrentEpicProductionRealPVFService(
		t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)

	if err := service.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != uint16(1417) || packet.Header.Classification != 1 {
		t.Fatalf("response msg=%d class=%d rest=%d", packet.Header.MsgID, packet.Header.Classification, len(rest))
	}
	want := upperSuccessBody(buildCurrentEpicProductionStartSuccessBody(epicProductionLiveTargetItemID))
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("success body=%x want=%x", packet.Body, want)
	}
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group missing")
	}
	saved, found, err := repositories.Character.Load(context.Background(), "3")
	if err != nil || !found || saved.Stats[currentEpicProductionTargetStat] != int64(epicProductionLiveTargetItemID) {
		t.Fatalf("persisted target=%d found=%t err=%v", saved.Stats[currentEpicProductionTargetStat], found, err)
	}

	// 回退档（shared）必须逐字节回到现场症状：`00 03`。
	legacyService, legacySession, legacyConnection := newCurrentEpicProductionRealPVFService(
		t, epicProductionSessionAccount, epicProductionAccountScopeShared, epicProductionInfoPushAlways)
	if err := legacyService.handleCurrentEpicProductionStart(legacySession, epicProductionStartRequestBody(t, epicProductionLiveTargetItemID)); err != nil {
		t.Fatal(err)
	}
	assertEpicProductionReferenceFailurePacket(t, legacyConnection)
}

// TestCurrentEpicProductionRealPVFInfoPushMatchesVendorBeforeSelection 钉真 PVF 下
// op1355 的整包形状：还没选武器（target=0）时也发，body 与厂商 9.16 夹具逐字节相同。
func TestCurrentEpicProductionRealPVFInfoPushMatchesVendorBeforeSelection(t *testing.T) {
	service, session, connection := newCurrentEpicProductionRealPVFService(
		t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
	if err := service.sendSelectedCurrentEpicProductionInfo(session, "test_vendor_alignment"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != uint16(1355) || packet.Header.Classification != 0 {
		t.Fatalf("op1355 msg=%d class=%d rest=%d", packet.Header.MsgID, packet.Header.Classification, len(rest))
	}
	// 厂商夹具只覆盖 character_id=1/2；同形状里把角色号换成 3 后逐字节比对
	// （body = u32 角色号 + 28 字节状态，厂商两条样本的状态段全为 `07 00 00 00` + 24×00）。
	want := epicProductionVendor916InfoBody(t, epicProductionVendor916InfoPacketHexChar1)
	binary.LittleEndian.PutUint32(want[0:4], 3)
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("op1355 body=%x want(厂商形状)=%x", packet.Body, want)
	}
}

// TestEpicProductionHandlersUseSessionAccountEverywhere 是源码级防回归：
// epic_production.go 里**不允许**再出现 `s.accountID()`（共享兜底账号）作为
// 史诗制作链路的账号来源；所有账号都必须经 epicProductionAccountIDForSession。
func TestEpicProductionHandlersUseSessionAccountEverywhere(t *testing.T) {
	source, err := os.ReadFile("epic_production.go")
	if err != nil {
		t.Fatal(err)
	}
	for lineNumber, line := range strings.Split(string(source), "\n") {
		if !strings.Contains(line, "s.accountID()") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		t.Fatalf("epic_production.go:%d still uses the shared fallback account directly: %s",
			lineNumber+1, strings.TrimSpace(line))
	}
}
