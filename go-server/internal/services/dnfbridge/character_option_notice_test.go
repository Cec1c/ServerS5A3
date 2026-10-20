package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场回归：**每角色选项（op447）只落库、从不回放 ⇒ 重登后被重置**。
//
// 操作者报障「穿脱装备 选择角色 重新登录后 Buff强化换装栏被重置」属于这一族：
// 客户端对这份 512 字节的每角色选项**没有本地存储**，权威副本只在服务端，
// 只在登录/进城窗口按 class0/0x0187 回放（ENUM_NOTIPACKET_CHARACTER_OPTION）
// 才能让玩家重登后看到自己保存过的界面状态。
//
// 现场形状（HK packet_log 2026-09-24，`packet_log.20260924-195607.txt:120/136/6977`）：
//
//	RECV kind=game-legacy cmd=1 type=447 runtime_cmd_name=ENUM_CMDPACKET_SAVE_CHARACTER_OPTION
//	     header_seq=29 body_len=516 hex=01bf011102000082cf83c91d0000020000ffff...
//	                ^^^^^^^^ = u32 len 512 (0x200) + 512B 选项 blob
//
// 厂商同血统 9.16 参考实现（`ServerS4A21`）：
//   - `Network/Core/PacketTypes.cs:398 CHARACTER_OPTION = 0x0187 // 391:
//     ENUM_NOTIPACKET_CHARACTER_OPTION`（S2C 通知号）；
//   - `Network/Handlers/SettingsHandler.cs:154 Handle_SAVE_CHARACTER_OPTION` →
//     `_characterStateRepository.SaveCharacterOption(characterId, body)`（**原样**存整个请求体）；
//   - `Network/Builders/Init/CharacterOptionBodyBuilder.cs`（NotiType `0x0187`）在选角初始化里
//     把存下来的 body **原样**回放。

// 现场 516 字节帧：u32 len=512 + 512B blob（首四字节取自现场 hex）。
func liveCharacterOptionFrame() []byte {
	blob := testGameOptionBlob(512, 0xFF)
	copy(blob, []byte{0x11, 0x02, 0x00, 0x00, 0x82, 0xCF, 0x83, 0xC9})
	return testLengthPrefixedBlob(blob)
}

// 落库必须保存 op447 的**完整帧**（参考实现的原样语义），回放必须逐字节相同。
func TestCharacterOptionNoticeReplaysOp447FrameVerbatim(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	frame := liveCharacterOptionFrame()

	handled, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, frame)
	if err != nil || !handled {
		t.Fatalf("handleGameOptionCommand handled=%t err=%v", handled, err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("op447 answered with %d bytes; the current client sends no request id", connection.write.Len())
	}
	record, found, err := repos.Settings.Load(ctx, characterOptionScope("19"))
	if err != nil || !found {
		t.Fatalf("character option settings found=%t err=%v", found, err)
	}
	if got := record.Values["character_option_body_hex"]; got != hex.EncodeToString(frame) {
		t.Fatalf("stored frame len=%d, want the verbatim %d-byte op447 request", len(got)/2, len(frame))
	}
	if got := record.Values["character_option_len"]; got != "512" {
		t.Fatalf("character_option_len=%q, want 512", got)
	}

	connection.write.Reset()
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("character option notices = %d, want exactly one 0x0187", len(packets))
	}
	if packets[0].msgID != currentCharacterOptionNoticeMsgID {
		t.Fatalf("msg_id = %#x, want %#x", packets[0].msgID, currentCharacterOptionNoticeMsgID)
	}
	if string(packets[0].body) != string(frame) {
		t.Fatalf("0x0187 body = %x, want the verbatim saved frame %x", packets[0].body, frame)
	}
	if len(packets[0].body) != 516 {
		t.Fatalf("0x0187 body_len = %d, want the live 516-byte frame", len(packets[0].body))
	}
}

// 本改动之前落库的行只有 `character_option_hex`（无长度前缀的 blob）：回放必须按
// `u32 len + blob` 重建出与客户端原帧相同的 516 字节，老数据不能被丢掉。
func TestCharacterOptionNoticeRebuildsFrameForLegacyBlobRows(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	frame := liveCharacterOptionFrame()
	blob := frame[4:]
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: characterOptionScope("19"),
		Values: map[string]string{
			"character_option_hex": hex.EncodeToString(blob),
			"character_option_len": "512",
			"source":               "current_exe_op447",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 || packets[0].msgID != currentCharacterOptionNoticeMsgID {
		t.Fatalf("packets=%d msg_id=%#x", len(packets), packets[0].msgID)
	}
	if got := binary.LittleEndian.Uint32(packets[0].body[0:4]); got != 512 {
		t.Fatalf("rebuilt length prefix = %d, want 512", got)
	}
	if string(packets[0].body) != string(frame) {
		t.Fatalf("rebuilt 0x0187 body = %x, want %x", packets[0].body, frame)
	}
}

// 没有保存过任何每角色选项时**一个字节都不发**：与 0x00AD 同口径，让"从没改过选项"
// 的角色保持逐字节不变的流量（参考实现会发 `u32 0`，那等于主动要求客户端回默认值）。
func TestCharacterOptionNoticeSendsNothingWithoutSavedOption(t *testing.T) {
	service, session, connection, _ := testGameOptionFixture(t)
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("unsaved character option produced %d bytes", connection.write.Len())
	}

	// 只有一条没有 blob 的残行时同样不发。
	if err := service.saveSettingsValues(session, characterOptionScope("19"), map[string]string{
		"source": "seed_without_blob",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("blob-less character option row produced %d bytes", connection.write.Len())
	}
}

// 一键回退：DNFBRIDGE_CHARACTER_OPTION_NOTI 关掉后只停回放，落库不受影响。
func TestCharacterOptionNoticeDisabledByEnv(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, liveCharacterOptionFrame()); err != nil {
		t.Fatal(err)
	}
	t.Setenv(currentCharacterOptionNoticeEnv, "0")
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("disabled character option replay still produced %d bytes", connection.write.Len())
	}
	if _, found, err := repos.Settings.Load(ctx, characterOptionScope("19")); err != nil || !found {
		t.Fatalf("disabled replay must not affect persistence found=%t err=%v", found, err)
	}
}

// A/B 档 `empty`：参考实现 `CharacterOptionBodyBuilder` 在无存档时发 `u32 0`。
// 默认档不发（保持逐字节不变），只有显式选这一档才发出那个 4 字节空帧——
// 用于现场判断"客户端是否需要一次显式空帧才会清掉旧的本地状态"。
func TestCharacterOptionNoticeEmptyFrameMode(t *testing.T) {
	service, session, connection, _ := testGameOptionFixture(t)
	if mode := currentCharacterOptionNoticeMode(); mode != characterOptionNoticeSavedOnly {
		t.Fatalf("default mode = %d, want saved-only", mode)
	}
	t.Setenv(currentCharacterOptionNoticeEnv, currentCharacterOptionNoticeEmptyValue)
	if mode := currentCharacterOptionNoticeMode(); mode != characterOptionNoticeWithEmpty {
		t.Fatalf("empty mode = %d", mode)
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 || packets[0].msgID != currentCharacterOptionNoticeMsgID {
		t.Fatalf("packets=%d msg_id=%#x", len(packets), packets[0].msgID)
	}
	if string(packets[0].body) != string([]byte{0, 0, 0, 0}) {
		t.Fatalf("empty-mode body = %x, want 00000000", packets[0].body)
	}

	// 有存档时这一档照样回放真帧，不发空帧。
	connection.write.Reset()
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, liveCharacterOptionFrame()); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets = drainUpperPackets(t, connection)
	if len(packets) != 1 || string(packets[0].body) != string(liveCharacterOptionFrame()) {
		t.Fatalf("empty-mode saved replay = %x", packets[0].body)
	}
}

// 进场窗口守卫对 op447 的影响与 op197 一致：窗口没开时既不落库也不回放
// （客户端在进场时会推一次工厂模板，落库会把玩家存档冲掉）。
func TestCharacterOptionNoticeStaysEmptyDuringEntryWindow(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	session.initialTownRouteStage = currentInitialTownRouteIdle
	session.townSceneReadyCharacterID = 0

	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, liveCharacterOptionFrame()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repos.Settings.Load(ctx, characterOptionScope("19")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("entry-window op447 was persisted")
	}
	if err := service.sendCharacterOptionNotice(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("entry-window replay produced %d bytes", connection.write.Len())
	}
}
