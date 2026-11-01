package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	protocol "longheng.io/server/internal/modules/dnf/protocol"
)

// 取自配套EXE真实新增发送函数，经原生组包/克隆/CRC循环直到socket调用边界。
// 内存、环形缓冲、CRC表读取和socket均为隔离夹具，不能作为游戏实机验收。
func TestPlayerStatsGameTransportNativeWire(t *testing.T) {
	text, err := os.ReadFile("testdata/player_stats_game_native_frame.hex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatal(err)
	}
	if protocol.Checksum(raw[11:]) != binary.LittleEndian.Uint32(raw[7:11]) {
		t.Fatal("原生CRC循环与服务端校验不符")
	}
	// Keep the captured body/CRC; only the separate header opcode is remapped
	// to avoid colliding with the existing private activity/festival routes.
	binary.LittleEndian.PutUint16(raw[1:3], playerStatsGameOpcode)
	for cut := 0; cut <= len(raw); cut++ {
		var pending []byte
		var result []protocol.LatestGameStreamPacket
		for _, part := range [][]byte{raw[:cut], raw[cut:]} {
			pending = append(pending, part...)
			frames, tail, skipped, err := protocol.SplitLatestGameStream(pending, 65535)
			if err != nil || skipped != 0 {
				t.Fatal(cut, err, skipped)
			}
			result = append(result, frames...)
			pending = append([]byte(nil), tail...)
		}
		if len(result) != 1 || len(pending) != 0 || result[0].Kind != protocol.LatestGameStreamLegacy || !bytes.Equal(result[0].Data, raw) {
			t.Fatal("真实请求拆流不符", cut)
		}
	}
	s, x, _, c := playerStatsGameFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.callGameSession(ctx, x, "真实统计帧", func() error { return s.handleLegacyGamePacket(x, raw) }); err != nil {
		t.Fatal(err)
	}
	b := playerStatsPacket(t, c)
	if b[28] != 0 || binary.LittleEndian.Uint32(b[20:24]) != 0x12345678 || binary.LittleEndian.Uint32(b[24:28]) != 9 || string(b[36:40]) != "PSA2" {
		t.Fatal("真实分派没有返回对应统计")
	}
}
