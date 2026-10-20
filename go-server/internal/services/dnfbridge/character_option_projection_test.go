package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件只覆盖 **DNFBRIDGE_CHARACTER_OPTION_PROJECTION** 这条新链路：
//
//	(a) 默认档逐字节等于今天（原样回放）；
//	(b) project 档对 **character:3 的真实存档 blob** 的输出，等于按厂商
//	    `AccountSettings.ProjectCharacterOptionBlob`（AccountSettings.cs:124-173）
//	    手算出来的差异；
//	(c) 畸形输入/缺输入不崩、回落原样，并留下可 grep 的日志；
//	(d) 除目标 option index 之外**逐字节不动**。
//
// 既有 op447/391 用例（character_option_notice_test.go / game_option_settings_test.go）
// 的钉死期望值**一个都没有改**：默认档就是"路径不存在"。

// liveCharacterOptionFrameHex 是 character:3 在线上库 `dnf_setting_values`
// （scope=`character:3:character_option`，entry_key=`character_option_body_hex`）
//
//	u32 LE 512 + 512 B payload；payload 里只有
//	option index 2 == 0x0020(32)（Buff 强化登记留下的那一个语义值）
//	与 option index 43 == 0x0000，其余全是厂商的"无值"字 0xFFFF。
const liveCharacterOptionFrameHex = "" +
	"00020000ffffffff2000ffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffff0000ffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" +
	"ffffffff"

func liveCharacter3OptionFrame(t *testing.T) []byte {
	t.Helper()
	frame, err := hex.DecodeString(liveCharacterOptionFrameHex)
	if err != nil {
		t.Fatalf("decode the live character:3 frame: %v", err)
	}
	if len(frame) != 516 {
		t.Fatalf("live character:3 frame = %d B, want the 516 B stored in the live DB", len(frame))
	}
	// 现场不变量（只读 SELECT 的结果，报告里逐字列出）：index 0 是 0xFFFF 而**不是 1**。
	if got := binary.LittleEndian.Uint16(frame[4:]); got != 0xFFFF {
		t.Fatalf("live frame index 0 = %#04x, want 0xFFFF (the vendor's forced index0=1 is overwritten by the saved copy)", got)
	}
	if got := binary.LittleEndian.Uint16(frame[4+2*2:]); got != 0x0020 {
		t.Fatalf("live frame index 2 = %#04x, want 0x0020 (the Buff-swap registration)", got)
	}
	if got := binary.LittleEndian.Uint16(frame[4+43*2:]); got != 0x0000 {
		t.Fatalf("live frame index 43 = %#04x, want 0x0000", got)
	}
	return frame
}

// (a) 默认档：开关未设置/空/任何其它值都必须**逐字节等于输入**，且不产生任何
// "变化的 index"（= 路径不存在，逐字节今天）。
func TestCharacterOptionProjectionDefaultModeIsByteIdentical(t *testing.T) {
	frame := liveCharacter3OptionFrame(t)
	knownContext := characterOptionProjectionContext{VisibleBits: 0x03, VisibleBitsKnown: true}

	// 大小写与首尾空白按既有约定归一化（`Project ` == `project`），但**只有**
	// 归一化后等于 `project` 才换档：`projection`/`projet` 这类近似值仍是默认档。
	for _, raw := range []string{"", " ", "verbatim", "on", "true", "1", "yes", "off", "0", "projection", "projet"} {
		t.Setenv(currentCharacterOptionProjectionEnv, raw)
		if got := currentCharacterOptionProjectMode(); got != characterOptionProjectionVerbatim {
			t.Fatalf("env=%q normalised to %q, want %q (only an explicit `%s` may leave the default)",
				raw, got, characterOptionProjectionVerbatim, currentCharacterOptionProjectionValue)
		}
		// 即使上下文完全可用，默认档也不许动一个字节。
		body, projection, changedIndexes, changedHex, failure := characterOptionNoticeFrameForProjection(
			frame, "op447_verbatim_frame", knownContext, currentCharacterOptionProjectMode())
		if failure != nil {
			t.Fatalf("env=%q default mode failed: %v", raw, failure)
		}
		if projection != characterOptionProjectionVerbatim {
			t.Fatalf("env=%q projection=%q, want %q", raw, projection, characterOptionProjectionVerbatim)
		}
		if len(changedIndexes) != 0 || len(changedHex) != 0 {
			t.Fatalf("env=%q default mode reported changes %v %v", raw, changedIndexes, changedHex)
		}
		if !bytes.Equal(body, frame) {
			t.Fatalf("env=%q default mode body differs from the input frame", raw)
		}
		if &body[0] != &frame[0] {
			t.Fatalf("env=%q default mode copied the frame; it must hand back the same slice untouched", raw)
		}
	}
	// 显式 project 才换档。
	t.Setenv(currentCharacterOptionProjectionEnv, currentCharacterOptionProjectionValue)
	if got := currentCharacterOptionProjectMode(); got != characterOptionProjectionProject {
		t.Fatalf("explicit %q normalised to %q", currentCharacterOptionProjectionValue, got)
	}
}

// (b) project 档对 **character:3 的真实 blob**：厂商投影只改 index 126，
// 0xFFFF -> 0x0001（`bits & 0x08 == 0`）。index 0 **保持 0xFFFF**——厂商
// `NormalizeCharacterOptionBlob` 的第 4 步 `body[4]=1`（AccountSettings.cs:150）
// 会被第 6 步的整段拷贝覆盖（:156-170），所以真实存档下厂商发的也是 0xFFFF。
func TestCharacterOptionProjectionOnLiveCharacter3Blob(t *testing.T) {
	frame := liveCharacter3OptionFrame(t)
	body, projection, changedIndexes, changedHex, failure := characterOptionNoticeFrameForProjection(
		frame, "op447_verbatim_frame",
		characterOptionProjectionContext{VisibleBits: 0x03, VisibleBitsKnown: true},
		characterOptionProjectionProject)
	if failure != nil {
		t.Fatalf("projection failed: %v", failure)
	}
	if projection != characterOptionProjectionProject {
		t.Fatalf("projection=%q, want %q", projection, characterOptionProjectionProject)
	}
	if len(body) != 516 {
		t.Fatalf("projected body = %d B, want the vendor's 516 B", len(body))
	}
	if len(changedIndexes) != 1 || changedIndexes[0] != vendorCharacterFullAvatarIndex {
		t.Fatalf("changed indexes = %v, want exactly [%d]", changedIndexes, vendorCharacterFullAvatarIndex)
	}
	if len(changedHex) != 1 || changedHex[0] != "126:0xFFFF->0x0001" {
		t.Fatalf("changed words = %v, want [126:0xFFFF->0x0001]", changedHex)
	}
	// 逐字节差异：只有 frame 字节 256..257（= 4 + 126*2）从 ff ff 变成 01 00。
	if got := body[256:258]; !bytes.Equal(got, []byte{0x01, 0x00}) {
		t.Fatalf("projected bytes 256..258 = % x, want 01 00", got)
	}
	// u32 长度前缀不变，index 0 / 2 / 43 一个都不许动。
	if got := binary.LittleEndian.Uint32(body[0:4]); got != 512 {
		t.Fatalf("projected u32 prefix = %d, want the vendor's constant 512", got)
	}
	for _, index := range []int{0, 1, 2, 3, 4, 43, 125, 127, 128} {
		offset := 4 + index*2
		if binary.LittleEndian.Uint16(body[offset:]) != binary.LittleEndian.Uint16(frame[offset:]) {
			t.Fatalf("projection touched option index %d outside the vendor field table", index)
		}
	}
	// index 127（称号透明度）必须保持 0xFFFF：本仓拿不到账号包 packedMain，
	// 就照厂商 `packedMain == null` 分支处理，不许编一个默认值。
	if got := binary.LittleEndian.Uint16(body[4+vendorCharacterHonorOpacityIndex*2:]); got != vendorCharacterOptionInvalidWord {
		t.Fatalf("index 127 = %#04x, want 0xFFFF (packedMain is unobtainable in this tree)", got)
	}
	// 隐藏全身时装位置位时厂商写 0 而不是 1。
	hidden, _, _, _, err := characterOptionNoticeFrameForProjection(
		frame, "op447_verbatim_frame",
		characterOptionProjectionContext{VisibleBits: 0x0B, VisibleBitsKnown: true},
		characterOptionProjectionProject)
	if err != nil {
		t.Fatalf("projection with hide-full-avatar set failed: %v", err)
	}
	if got := binary.LittleEndian.Uint16(hidden[4+vendorCharacterFullAvatarIndex*2:]); got != 0 {
		t.Fatalf("hide-full-avatar index 126 = %#04x, want 0", got)
	}
}

// (d) 除目标 index 之外**逐个 index** 对比：任何别的 u16 变了都是回归。
func TestCharacterOptionProjectionTouchesOnlyTheVendorIndexes(t *testing.T) {
	frame := liveCharacter3OptionFrame(t)
	body, _, changedIndexes, _, failure := characterOptionNoticeFrameForProjection(
		frame, "op447_verbatim_frame",
		characterOptionProjectionContext{VisibleBits: 0x03, VisibleBitsKnown: true},
		characterOptionProjectionProject)
	if failure != nil {
		t.Fatal(failure)
	}
	changed := map[int]bool{}
	for _, index := range changedIndexes {
		changed[index] = true
	}
	for index := 0; index < vendorCharacterOptionPayloadLength/2; index++ {
		offset := 4 + index*2
		same := binary.LittleEndian.Uint16(body[offset:]) == binary.LittleEndian.Uint16(frame[offset:])
		if same == changed[index] {
			t.Fatalf("option index %d: same=%t but reported changed=%t", index, same, changed[index])
		}
	}
	// 长度与前缀也必须逐字节一致（否则就是"半截帧"）。
	if len(body) != len(frame) || !bytes.Equal(body[0:4], frame[0:4]) {
		t.Fatalf("projection changed the framing: len %d->%d prefix % x -> % x",
			len(frame), len(body), frame[0:4], body[0:4])
	}
}

// (c) 畸形/缺输入：纯函数必须报错而不是崩；回放外壳必须回落**原样帧**并记日志。
func TestCharacterOptionProjectionFailsSafeOnMalformedInput(t *testing.T) {
	frame := liveCharacter3OptionFrame(t)
	knownContext := characterOptionProjectionContext{VisibleBits: 0x03, VisibleBitsKnown: true}

	cases := []struct {
		name    string
		saved   []byte
		context characterOptionProjectionContext
	}{
		{name: "nil frame", saved: nil, context: knownContext},
		{name: "empty frame", saved: []byte{}, context: knownContext},
		{name: "3-byte frame", saved: []byte{0x00, 0x02, 0x00}, context: knownContext},
		{name: "prefix claims 8 KiB", saved: func() []byte {
			bad := append([]byte(nil), frame...)
			binary.LittleEndian.PutUint32(bad[0:4], 8<<10)
			return bad
		}(), context: knownContext},
		{name: "unknown visible bits", saved: frame, context: characterOptionProjectionContext{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := projectCharacterOptionFrame(testCase.saved, testCase.context); err == nil {
				t.Fatal("malformed input produced no error")
			}
			body, projection, changedIndexes, changedHex, failure := characterOptionNoticeFrameForProjection(
				testCase.saved, "op447_verbatim_frame", testCase.context, characterOptionProjectionProject)
			if failure == nil {
				t.Fatal("the replay shell swallowed the projection failure")
			}
			if projection != characterOptionProjectionVerbatim {
				t.Fatalf("projection=%q on failure, want %q", projection, characterOptionProjectionVerbatim)
			}
			if len(changedIndexes) != 0 || len(changedHex) != 0 {
				t.Fatalf("failed projection reported changes %v %v", changedIndexes, changedHex)
			}
			if !bytes.Equal(body, testCase.saved) {
				t.Fatalf("failed projection did not fall back to the verbatim frame: % x", body)
			}
		})
	}
}

// (c′) 回放路径上的 fail-safe + 可 grep 日志：投影失败时掉进
// `game-character-option-projection-fallback`，字段里同时有
// `character_option_projection=verbatim` 与 `projection_error=`。
func TestCharacterOptionProjectionFallbackLogsGrepFields(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	frame := liveCharacter3OptionFrame(t)

	// 先按默认档落库（落库链路完全不看这个开关）。
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, frame); err != nil {
		t.Fatal(err)
	}
	// 让角色记录里**没有** user_state_bits：投影缺输入，必须回落而不是猜 3。
	if err := repos.Character.Save(t.Context(), characterRecordWithoutUserStateBits()); err != nil {
		t.Fatal(err)
	}

	logger, logPath := testCharacterOptionPacketLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	t.Setenv(currentCharacterOptionProjectionEnv, currentCharacterOptionProjectionValue)

	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("notices = %d, want the one 0x0187", len(packets))
	}
	if !bytes.Equal(packets[0].body, frame) {
		t.Fatalf("fallback body = %x, want the verbatim stored frame", packets[0].body)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged := readCharacterOptionPacketLog(t, logPath)
	for _, want := range []string{
		"kind=game-character-option-projection-fallback",
		"character_option_projection=verbatim",
		"projection_error=",
		"frame_source=op447_verbatim_frame",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("fallback log is missing %q; got:\n%s", want, logged)
		}
	}
}

// (c″) 端到端：project 档下 0x0187 的 body 就是投影后的帧（index 126 = 1），
// 且日志里能 grep 到 `character_option_projection=project` 与变化 index。
func TestCharacterOptionNoticeProjectsWhenEnabled(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	frame := liveCharacter3OptionFrame(t)

	// 夹具角色本身没带 user_state_bits。投影在线上**确实会生效**（只读 SELECT 实测
	// character:3 的镜像列 user_state_bits = 3），所以这里显式补上现场口径的值，
	// 免得把"缺输入回落"误当成"投影已生效"。
	if err := repos.Character.Save(t.Context(), characterRecordWithUserStateBits(0x03)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, frame); err != nil {
		t.Fatal(err)
	}

	logger, logPath := testCharacterOptionPacketLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	t.Setenv(currentCharacterOptionProjectionEnv, currentCharacterOptionProjectionValue)

	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 || packets[0].msgID != currentCharacterOptionNoticeMsgID {
		t.Fatalf("packets=%d, want one 0x0187", len(packets))
	}
	want, _, _, _, err := characterOptionNoticeFrameForProjection(
		frame, "op447_verbatim_frame",
		characterOptionProjectionContext{VisibleBits: 0x03, VisibleBitsKnown: true},
		characterOptionProjectionProject)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packets[0].body, want) {
		t.Fatalf("0x0187 body = %x, want the projected frame %x", packets[0].body, want)
	}
	if got := binary.LittleEndian.Uint16(packets[0].body[4+vendorCharacterFullAvatarIndex*2:]); got != 1 {
		t.Fatalf("projected index 126 = %#04x, want 1", got)
	}
	// 受影响区段的**确切字节**（帧内 254..262）：厂商只动 index 126，
	// 所以 254..255 与 260..261 保持 ff ff，只有 256..257 变成 01 00。
	// index 127（258..259）保持 0xFFFF —— 本仓拿不到账号包 packedMain。
	if tail := packets[0].body[254:262]; !bytes.Equal(tail, []byte{0xff, 0xff, 0x01, 0x00, 0xff, 0xff, 0xff, 0xff}) {
		t.Fatalf("projected bytes 254..262 = % x, want ff ff 01 00 ff ff ff ff", tail)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged := readCharacterOptionPacketLog(t, logPath)
	for _, want := range []string{
		"kind=game-character-option-projection-applied",
		"character_option_projection=project",
		"changed_indexes=[126]",
		"changed_words=[126:0xFFFF->0x0001]",
		"packed_main=absent",
		"body_len_in=516",
		"body_len_out=516",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("projection log is missing %q; got:\n%s", want, logged)
		}
	}
	// 成功行里**不许**出现 projection_error：否则 grep `projection_error=` 会把
	// "投影成功"和"投影失败回落"混成一条。
	if strings.Contains(logged, "projection_error=") {
		t.Fatalf("a successful projection logged projection_error; got:\n%s", logged)
	}
}

// (a′) 默认档在回放路径上也必须逐字节今天：project 未开启时不做投影、不多读仓储。
func TestCharacterOptionNoticeStaysVerbatimWithoutProjectionEnv(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	frame := liveCharacter3OptionFrame(t)
	if err := repos.Character.Save(t.Context(), characterRecordWithUserStateBits(0x03)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, frame); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("notices = %d, want 1", len(packets))
	}
	if !bytes.Equal(packets[0].body, frame) {
		t.Fatalf("default-mode body = %x, want the verbatim live frame", packets[0].body)
	}
}

// characterRecordWithUserStateBits 复制夹具角色（character 19 / account-1），
// 并补上 `user_state_bits` 镜像列（mysql_char.go:90，fallback 3；
// 线上 character:3 = 3；读取口径见 current_actor_user_state.go:55-63）。
func characterRecordWithUserStateBits(bits int64) dnfrepo.CharacterRecord {
	return dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"user_state_bits": bits},
	}
}

// characterRecordWithoutUserStateBits 是"投影缺输入"的那条记录：角色存在、账号正确，
// 但**没有** user_state_bits 镜像列。厂商在这里会兜 `?? (byte)3`；本仓必须明确失败
// 回落，不许替现场猜一个 3。
func characterRecordWithoutUserStateBits() dnfrepo.CharacterRecord {
	return dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Job:         "0",
		Level:       90,
	}
}

// testCharacterOptionPacketLogger 把 service.packetLog 换成写临时文件的真 logger，
// 好让"投影失败/生效"那两条具名事件可以被 grep 断言（生产里它们落在
// `runtime/log/packet_log.*.txt` 的 `EVENT kind=…` 行）。
func testCharacterOptionPacketLogger(t *testing.T) (*packetLogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "packet_log.projection-test.txt")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatalf("open packet logger: %v", err)
	}
	if logger == nil {
		t.Fatal("packet logger was nil")
	}
	return logger, path
}

func readCharacterOptionPacketLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read packet log: %v", err)
	}
	return string(data)
}
