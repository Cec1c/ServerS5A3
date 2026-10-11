package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-10-02 (V22)：这条跳过日志以前**无论真实原因是什么都写 `party_member_channel_mismatch`**，
// 而且只打两个频道号 —— 线上因此出现过 `source_channel=11 member_channel=11` 却写着"频道不符"
// 的行（真实原因是场景桶不同，例如一方在每角色一份的私有落点房里）。
//
// 真实判据是 `PeerInSameArea`：城镇在线表的**四元桶键 town/area/channel/instance**。
// 本用例把两种情形分开钉死：
//   - 同频道、不同 area ⇒ `reason=party_member_area_mismatch`，并把两边的桶键并排打出来；
//   - 真跨频道        ⇒ `reason=party_member_channel_mismatch`（保持原语义）。
func TestPartyEnterSelectSkipLogDistinguishesAreaFromChannelMismatch(t *testing.T) {
	for _, row := range []struct {
		name       string
		memberArea byte
		memberCh   int
		wantReason string
		wantSource string
		wantMember string
	}{
		{name: "same_channel_different_area", memberArea: 3, memberCh: 42,
			wantReason: partyMemberAreaMismatchReason,
			wantSource: "38/2/ch42/inst0", wantMember: "38/3/ch42/inst0"},
		{name: "different_channel", memberArea: 2, memberCh: 43,
			wantReason: partyMemberChannelMismatchReason,
			wantSource: "38/2/ch42/inst0", wantMember: "38/2/ch43/inst0"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, service, leader, member, _, _ := newTownCoPresenceTestService(t)
			storeRuntimePartyState(leader, townCoPresencePartyState())
			storeRuntimePartyState(member, townCoPresencePartyState())

			logPath := filepath.Join(t.TempDir(), "packet.log")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			service.packetLog = logger

			// 把队员挪到目标桶：会话频道是权威来源（resolvePresenceChannelID 取会话）。
			member.channel.ID = row.memberCh
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{
				CharacterID: 1002, AccountID: "dnf:2", Name: "member",
				TownID: 38, AreaID: row.memberArea, PositionX: 452, PositionY: 236,
				Direction: 5, AreaState: 3, Session: member,
			})
			if err := service.synchronizeRuntimePartyMembersEnterSelect(leader, "test_skip_log"); err != nil {
				t.Fatalf("selector sync: %v", err)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.Contains(text, "kind=game-party-enter-select-member-skipped") {
				t.Fatalf("skip event missing:\n%s", text)
			}
			if !strings.Contains(text, "reason="+row.wantReason) {
				t.Fatalf("reason must follow the real difference (%s):\n%s", row.wantReason, text)
			}
			if !strings.Contains(text, "source_area="+row.wantSource) ||
				!strings.Contains(text, "member_area="+row.wantMember) {
				t.Fatalf("area buckets must be printed side by side (%s vs %s):\n%s",
					row.wantSource, row.wantMember, text)
			}
		})
	}
}
