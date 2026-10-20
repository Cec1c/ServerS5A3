package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func accountFriendRequestFixture(op uint16, target, text string, action byte) []byte {
	var w packetWriter
	if op == 1439 {
		w.writeByte(action)
	}
	_ = writeCurrentAccountFriendDstr(&w, target)
	if op == 1436 || op == 1440 {
		_ = writeCurrentAccountFriendDstr(&w, text)
	}
	return w.bytes()
}
func TestCurrentAccountFriendCodecNativeShapeAndBounds(t *testing.T) {
	for _, tc := range []struct {
		op           uint16
		target, text string
		action       byte
	}{{1436, "冒险团", "你好", 0}, {1437, "冒险团", "", 0}, {1438, "角色名", "", 0}, {1439, "冒险团", "", 4}, {1439, "冒险团", "", 5}, {1440, "角色名", "备注 空格", 0}, {1440, "角色名", "", 0}} {
		body := accountFriendRequestFixture(tc.op, tc.target, tc.text, tc.action)
		r, err := decodeCurrentAccountFriendRequest(tc.op, body)
		if err != nil || r.Target != tc.target || r.Text != tc.text || r.Action != tc.action {
			t.Fatal("native shape", r, err)
		}
		tail, err := buildCurrentAccountFriendSuccessTail(tc.op, r)
		wantTail := accountFriendRequestFixture(tc.op, tc.target, tc.text, tc.action)
		if tc.op == 1436 {
			wantTail = accountFriendRequestFixture(1437, tc.target, "", 0)
		}
		if err != nil || !bytes.Equal(wantTail, tail) {
			t.Fatal("ACK tail", tc.op, tail, err)
		}
		for _, bad := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0), {}} {
			if _, err := decodeCurrentAccountFriendRequest(tc.op, bad); err == nil {
				t.Fatal("truncation/trailing accepted", tc.op)
			}
		}
	}
	for _, tc := range []struct {
		op           uint16
		target, text string
		action       byte
	}{{1439, "冒险团", "", 3}, {1436, "冒险团", "", 0}, {1436, "冒险团", strings.Repeat("字", 41), 0}, {1440, "角色名", "line\nbreak", 0}, {1437, "有 空格", "", 0}, {1438, strings.Repeat("字", 9), "", 0}} {
		if _, err := decodeCurrentAccountFriendRequest(tc.op, accountFriendRequestFixture(tc.op, tc.target, tc.text, tc.action)); err == nil {
			t.Fatal("invalid input accepted", tc)
		}
	}
}

func TestCurrentAccountFriendCodecExportsNativeReaderFixtures(t *testing.T) {
	fixtures := make(map[string][]byte)
	rows := []currentAccountFriendNativeRow{{Head: 0, AdventureName: "好友冒险团", Level: 90, Job: 1, Grow: 0x21, Channel: 42, CharacterName: "来来来", Remark: "个人备注"}, {Head: 0, AdventureName: "好友冒险团", Level: 85, Job: 0, Grow: 0x12, CharacterName: "离线角色"}}
	for _, n := range []int{0, 1, 2} {
		body, err := buildCurrentAccountFriendInfoBody(rows[:n])
		if err != nil {
			t.Fatal(err)
		}
		fixtures[[]string{"account-friend-info-empty.bin", "account-friend-info-one.bin", "account-friend-info-two.bin"}[n]] = body
	}
	pending := []currentAccountFriendPendingRow{{Kind: 0, AdventureName: "收到邀请", Message: "邀请留言", ExpiresUnix: 1791936000}, {Kind: 1, AdventureName: "已发邀请", Message: "另一条留言", ExpiresUnix: 1791936100}}
	longRow := rows[0]
	longRow.Remark = strings.Repeat("字", 40)
	longBody, longErr := buildCurrentAccountFriendInfoBody([]currentAccountFriendNativeRow{longRow})
	if longErr != nil {
		t.Fatal(longErr)
	}
	fixtures["account-friend-info-long-remark.bin"] = longBody
	longPending := pending[0]
	longPending.Message = strings.Repeat("字", 40)
	longBody, longErr = buildCurrentAccountFriendPendingBody([]currentAccountFriendPendingRow{longPending})
	if longErr != nil {
		t.Fatal(longErr)
	}
	fixtures["account-friend-pending-long-message.bin"] = longBody
	for _, n := range []int{0, 2} {
		body, err := buildCurrentAccountFriendPendingBody(pending[:n])
		if err != nil {
			t.Fatal(err)
		}
		name := "account-friend-pending-empty.bin"
		if n == 2 {
			name = "account-friend-pending-two.bin"
		}
		fixtures[name] = body
	}
	body, err := buildCurrentAccountFriendDeleteBody("离线角色")
	if err != nil {
		t.Fatal(err)
	}
	fixtures["account-friend-delete.bin"] = body
	for _, op := range []uint16{1436, 1437, 1438, 1439, 1440} {
		r := currentAccountFriendRequest{Target: "好友团", Text: "备注", Action: 4}
		if op != 1439 {
			r.Action = 0
		}
		if op != 1440 {
			r.Text = ""
		} else {
			r.Text = strings.Repeat("字", 40)
		}
		tail, err := buildCurrentAccountFriendSuccessTail(op, r)
		if err != nil {
			t.Fatal(err)
		}
		fixtures["account-friend-ack-"+[]string{"1436", "1437", "1438", "1439", "1440"}[op-1436]+".bin"] = append([]byte{1}, tail...)
	}
	if _, err := buildCurrentAccountFriendInfoBody(make([]currentAccountFriendNativeRow, 256)); err == nil {
		t.Fatal("u8 overflow")
	}
	if _, err := buildCurrentAccountFriendInfoBody([]currentAccountFriendNativeRow{{AdventureName: "团", CharacterName: "角色", Remark: strings.Repeat("字", 41)}}); err == nil {
		t.Fatal("remark buffer overflow")
	}
	if dir := os.Getenv("DNF90_ACCOUNT_FRIEND_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for name, body := range fixtures {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCurrentAccountFriendCodecReadsActualNativeProducerFixtures(t *testing.T) {
	dir := os.Getenv("DNF90_ACCOUNT_FRIEND_NATIVE_DIR")
	if dir == "" {
		t.Skip("explicit native proof fixtures only")
	}
	for _, tc := range []struct {
		op                 uint16
		action             byte
		file, target, text string
	}{
		{1436, 0, "account-friend-request-1436-0.bin", "目标冒险团", "中文消息 空格"},
		{1437, 0, "account-friend-request-1437-0.bin", "目标冒险团", ""},
		{1438, 0, "account-friend-request-1438-0.bin", "来来来", ""},
		{1439, 4, "account-friend-request-1439-4.bin", "目标冒险团", ""},
		{1439, 5, "account-friend-request-1439-5.bin", "目标冒险团", ""},
		{1440, 0, "account-friend-request-1440-0.bin", "来来来", "备注不截断"},
	} {
		raw, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		r, err := decodeCurrentAccountFriendRequest(tc.op, raw)
		if err != nil || r.Target != tc.target || r.Text != tc.text || r.Action != tc.action {
			t.Fatal("actual EXE producer mismatch", tc.file, r, err)
		}
	}
}
