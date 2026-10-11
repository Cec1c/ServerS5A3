package gmtool

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 邮箱只读快照。逐条对应上游 gmMailboxSnapshot（gm.go:597-640）的口径：
// 跳过 Deleted、MailID 空回退 map key、Claimable 的三元判断、Saved 读 Metadata、
// 最新优先 + MailID 数值降序。

func mailboxReadonlyFixture() *dnfrepo.MailboxRecord {
	base := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC)
	return &dnfrepo.MailboxRecord{
		Mails: map[string]dnfrepo.MailRecord{
			// 最新的一封：已领取，不可再领。
			"2": {
				MailID:    "2",
				Title:     "第二封",
				Gold:      100,
				Claimed:   true,
				CreatedAt: base.Add(2 * time.Hour),
			},
			// 同一时刻的两封：MailID 数值降序 -> "10" 在 "1" 前面。
			"1": {
				MailID:      "1",
				Title:       "第一封",
				Attachments: []dnfrepo.MailAttachment{{ItemID: 1, Count: 3, Bind: true}},
				Metadata:    map[string]string{"mailbox_saved": "TRUE"},
				Read:        true,
				CreatedAt:   base.Add(time.Hour),
			},
			"10": {
				MailID:    "10",
				Title:     "第十封",
				CreatedAt: base.Add(time.Hour),
			},
			// 已删除：必须被跳过。
			"3": {
				MailID:    "3",
				Title:     "已删除",
				Gold:      999,
				Deleted:   true,
				CreatedAt: base.Add(3 * time.Hour),
			},
			// MailID 字段为空：回退成 map key。
			"42": {
				Title:     "无编号字段",
				CreatedAt: base,
			},
		},
	}
}

func TestMailboxSnapshotMatchesUpstreamProjection(t *testing.T) {
	harness := newReadonlyHarness(t, readonlyFixture{
		source: readonlyItemSource(readonlyTestWeapon, nil),
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{},
		},
		mailbox: mailboxReadonlyFixture(),
	})
	_, response := harness.get(t, "/api/characters/1/mailbox", map[string]string{"id": "1"}, harness.server.handleMailboxSnapshot)
	requireOK(t, 200, response)

	if response["characterId"] != float64(1) {
		t.Fatalf("characterId = %v", response["characterId"])
	}
	mails := responseList(t, response, "mails")
	if len(mails) != 4 {
		t.Fatalf("mail count = %d, want 4 (the deleted one must be skipped): %+v", len(mails), mails)
	}
	order := make([]string, 0, len(mails))
	for _, entry := range mails {
		order = append(order, entry.(map[string]any)["mailId"].(string))
	}
	want := []string{"2", "10", "1", "42"}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("mail order = %v, want %v (CreatedAt 降序, 同刻 MailID 数值降序)", order, want)
		}
	}
	if response["count"] != float64(4) || response["claimableCount"] != float64(1) {
		t.Fatalf("count=%v claimableCount=%v, want 4 / 1", response["count"], response["claimableCount"])
	}
	if response["unreadCount"] != float64(3) {
		t.Fatalf("unreadCount = %v, want 3 (第一封 read=true；第二封/第十封/42 均未读)", response["unreadCount"])
	}
	if response["attachmentCount"] != float64(1) {
		t.Fatalf("attachmentCount = %v, want 1", response["attachmentCount"])
	}

	first := mails[0].(map[string]any)
	if first["title"] != "第二封" || first["claimed"] != true || first["claimable"] != false {
		t.Fatalf("claimed mail = %+v", first)
	}
	second := mails[1].(map[string]any)
	if second["mailId"] != "10" || second["claimable"] != false {
		t.Fatalf("empty mail = %+v (no gold, no attachments -> not claimable)", second)
	}
	third := mails[2].(map[string]any)
	if third["claimable"] != true || third["saved"] != true || third["read"] != true {
		t.Fatalf("asset mail = %+v (claimable + saved=TRUE 大小写不敏感)", third)
	}
	attachments := responseList(t, third, "attachments")
	if len(attachments) != 1 {
		t.Fatalf("attachments = %v", attachments)
	}
	attachment := attachments[0].(map[string]any)
	if attachment["itemId"] != float64(1) || attachment["count"] != float64(3) || attachment["bind"] != true {
		t.Fatalf("attachment = %+v", attachment)
	}
	// 名称来自本仓 PVF 物品索引；索引里有就带上（不许造名字）。
	if attachment["name"] != "测试消耗品" {
		t.Fatalf("attachment name = %v, want the PVF-index name", attachment["name"])
	}
	last := mails[3].(map[string]any)
	if last["mailId"] != "42" {
		t.Fatalf("mail without MailID must fall back to the map key: %+v", last)
	}
	if last["createdAt"] != float64(time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC).Unix()) {
		t.Fatalf("createdAt = %v, want Unix seconds", last["createdAt"])
	}
}

func TestMailboxSnapshotHandlesMissingMailboxAndCharacter(t *testing.T) {
	harness := newReadonlyHarness(t, readonlyFixture{
		source: readonlyItemSource(readonlyTestWeapon, nil),
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{},
		},
	})
	// 没有邮箱记录 = 空邮箱，不是错误（上游 gm.go:420-422）。
	_, response := harness.get(t, "/api/characters/1/mailbox", map[string]string{"id": "1"}, harness.server.handleMailboxSnapshot)
	requireOK(t, 200, response)
	if response["count"] != float64(0) {
		t.Fatalf("missing mailbox must read as empty: %+v", response)
	}
	if len(responseList(t, response, "mails")) != 0 {
		t.Fatalf("mails = %+v", response["mails"])
	}

	// 角色不存在 -> 本仓惯例的业务失败（200 + success:false）。
	_, response = harness.get(t, "/api/characters/77/mailbox", map[string]string{"id": "77"}, harness.server.handleMailboxSnapshot)
	requireFailure(t, 200, response)
}
