package mail

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是**邮件下行包体的长度/字段数不变量审计**。
//
// 背景（2026-09-25 现场）："收邮件概率闪退" 的前一轮结论是 op95 领取后补发的
// 整份 op13 容器页把正在使用的邮箱 UI 弄崩（`DNF.exe+0x23082D3`，同一处
// `std::wstring` 取值崩溃重复四次）。概率性崩溃无法在本地复现，所以这里改成
// **把每一个邮件包体构造器的"字段数 × 宽度"钉死**：任何会让客户端多读/少读的
// 形状变化都必须在这里先红。
//
// 0x61（邮箱列表）是唯一由服务端拼字段数的包体：一个 u8 摘要行数 + 逐行
// 可变长记录 + 两个 u16 计数 + 逐封详情行。摘要行除发送者名字 DSTR 之外的
// 尾段必须恒定 152 字节（[creature] 附件按当前客户端 type-26 分支多 5 字节
// = 157），任何一行少写字节都会让客户端从下一行开始错位，最终在 UI 侧读到
// 野指针。行头固定 12 字节 = u32 邮件 id + u32 发送者 id + u32 名字长度。
const (
	invMailboxSummaryHeadWidth     = 12
	invMailboxSummaryTailWidth     = 152
	invMailboxSummaryCreatureWidth = 157
)

type invCursor struct {
	data    []byte
	off     int
	nameLen int
}

func (c *invCursor) need(t *testing.T, n int, label string) []byte {
	t.Helper()
	if n < 0 || c.off+n > len(c.data) {
		t.Fatalf("mailbox 0x61 body over-read at %s: offset=%d want=%d len=%d", label, c.off, n, len(c.data))
	}
	slice := c.data[c.off : c.off+n]
	c.off += n
	return slice
}

func (c *invCursor) u8(t *testing.T, label string) byte {
	t.Helper()
	return c.need(t, 1, label)[0]
}

func (c *invCursor) u16(t *testing.T, label string) uint16 {
	t.Helper()
	return binary.LittleEndian.Uint16(c.need(t, 2, label))
}

func (c *invCursor) u32(t *testing.T, label string) uint32 {
	t.Helper()
	return binary.LittleEndian.Uint32(c.need(t, 4, label))
}

// dstring 读取一个 u32 长度前缀字符串，并校验长度前缀与实际可读字节一致。
func (c *invCursor) dstring(t *testing.T, label string) []byte {
	t.Helper()
	length := int(c.u32(t, label+" length"))
	c.nameLen = length
	return c.need(t, length, label)
}

type invSummarySpan struct {
	start, end int
	hasItem    bool
	width      int
}

type invWalkResult struct {
	summaryCount int
	summaries    []invSummarySpan
	notLoaded    uint16
	totalMails   uint16
	detailCount  int
	consumed     int
}

// invWalkMailboxList 按当前客户端 sub_1D913C0 的读取顺序严格走一遍 0x61 包体。
// creatureFlags 是每个摘要行是否带 type-26 前缀的**期望值**（由输入邮件决定），
// 长度必须与包体里的摘要行数一致——这本身就是"字段数"断言的一半。
func invWalkMailboxList(t *testing.T, body []byte, creatureFlags []bool) invWalkResult {
	t.Helper()
	cursor := &invCursor{data: body}
	result := invWalkResult{}
	result.summaryCount = int(cursor.u8(t, "summary count"))
	cursor.u8(t, "clear flag")
	if result.summaryCount != len(creatureFlags) {
		t.Fatalf("summary count=%d, want %d", result.summaryCount, len(creatureFlags))
	}
	for index := 0; index < result.summaryCount; index++ {
		start := cursor.off
		cursor.u32(t, "summary mail id")
		cursor.u32(t, "summary sender id")
		cursor.dstring(t, "summary sender name")
		tailStart := cursor.off
		cursor.u32(t, "summary gold")
		itemID := cursor.u32(t, "summary item id")
		hasItem := cursor.u8(t, "summary has item")
		cursor.u32(t, "summary item count")
		cursor.u16(t, "summary item durability")
		cursor.u8(t, "summary item bind")
		cursor.u32(t, "summary item expiration")
		cursor.u8(t, "summary item byte 0x12")
		cursor.u8(t, "summary item byte 0x13")
		cursor.u16(t, "summary item word 0x14")
		if creatureFlags[index] {
			cursor.u32(t, "type-26 detail dword")
			cursor.u8(t, "type-26 detail flag")
		}
		cursor.need(t, mailboxItemRawSize, "summary 0x77 item row")
		cursor.u32(t, "summary remaining seconds")
		cursor.u32(t, "summary claim mail id")
		cursor.u8(t, "summary mail type")
		span := invSummarySpan{start: start, end: cursor.off, hasItem: hasItem == 1, width: cursor.off - start}
		tail := cursor.off - tailStart
		wantTail := invMailboxSummaryTailWidth
		if creatureFlags[index] {
			wantTail = invMailboxSummaryCreatureWidth
		}
		if tail != wantTail {
			t.Fatalf("summary[%d] tail width=%d, want %d", index, tail, wantTail)
		}
		if span.width != invMailboxSummaryHeadWidth+cursor.nameLen+wantTail {
			t.Fatalf("summary[%d] width=%d, want head %d + name %d + tail %d",
				index, span.width, invMailboxSummaryHeadWidth, cursor.nameLen, wantTail)
		}
		if hasItem == 1 && itemID == 0 {
			t.Fatalf("summary[%d] claims an item row with item id 0", index)
		}
		if hasItem == 0 && itemID != 0 {
			t.Fatalf("summary[%d] has no item row but carries item id %d", index, itemID)
		}
		result.summaries = append(result.summaries, span)
	}
	result.notLoaded = cursor.u16(t, "not loaded count")
	result.totalMails = cursor.u16(t, "total mail count")
	for index := 0; index < int(result.totalMails); index++ {
		cursor.u32(t, "detail mail id")
		cursor.u32(t, "detail sender id")
		cursor.dstring(t, "detail sender name")
		cursor.dstring(t, "detail body")
		cursor.u32(t, "detail created at")
		cursor.u16(t, "detail letter stat")
		cursor.u8(t, "detail mail type")
		result.detailCount++
	}
	result.consumed = cursor.off
	if cursor.off != len(body) {
		t.Fatalf("mailbox 0x61 body has %d trailing byte(s): consumed=%d len=%d", len(body)-cursor.off, cursor.off, len(body))
	}
	return result
}

func invMail(mailID string, mutate func(*dnfrepo.MailRecord)) dnfrepo.MailRecord {
	record := dnfrepo.MailRecord{
		MailID:      mailID,
		SenderName:  "GM",
		Title:       "t",
		Body:        "body",
		CreatedAt:   time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		Metadata:    map[string]string{},
		Attachments: nil,
	}
	if mutate != nil {
		mutate(&record)
	}
	return record
}

func invCreatureAttachment(itemID int64) dnfrepo.MailAttachment {
	return dnfrepo.MailAttachment{
		ItemID: itemID,
		Count:  1,
		Extra:  map[string]string{"mailbox_equipment_type": "[creature]"},
	}
}

// TestMailboxListBodyKeepsFieldCountsAndRowWidths 覆盖所有摘要行形状：
// 纯文本（seedOnly）、纯金币、单附件、同封多附件、[creature] 附件、已领取。
func TestMailboxListBodyKeepsFieldCountsAndRowWidths(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		mails         []dnfrepo.MailRecord
		creatureFlags []bool
		wantSummaries int
	}{
		{
			name:          "text only mail uses one seed summary",
			mails:         []dnfrepo.MailRecord{invMail("1", nil)},
			creatureFlags: []bool{false},
			wantSummaries: 1,
		},
		{
			name:          "gold only mail",
			mails:         []dnfrepo.MailRecord{invMail("2", func(m *dnfrepo.MailRecord) { m.Gold = 50 })},
			creatureFlags: []bool{false},
			wantSummaries: 1,
		},
		{
			name: "single stackable attachment",
			mails: []dnfrepo.MailRecord{invMail("3", func(m *dnfrepo.MailRecord) {
				m.Attachments = []dnfrepo.MailAttachment{{ItemID: 490701730, Count: 5}}
			})},
			creatureFlags: []bool{false},
			wantSummaries: 1,
		},
		{
			name: "creature attachment takes the type-26 prefix",
			mails: []dnfrepo.MailRecord{invMail("4", func(m *dnfrepo.MailRecord) {
				m.Attachments = []dnfrepo.MailAttachment{invCreatureAttachment(25)}
			})},
			creatureFlags: []bool{true},
			wantSummaries: 1,
		},
		{
			name: "two attachments on one mail keep gold on the first row only",
			mails: []dnfrepo.MailRecord{invMail("5", func(m *dnfrepo.MailRecord) {
				m.Gold = 7
				m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}, invCreatureAttachment(25)}
			})},
			creatureFlags: []bool{false, true},
			wantSummaries: 2,
		},
		{
			name: "claimed mail projects as an empty receipt",
			mails: []dnfrepo.MailRecord{invMail("6", func(m *dnfrepo.MailRecord) {
				m.Gold = 500
				m.Claimed = true
				m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}}
			})},
			creatureFlags: []bool{false},
			wantSummaries: 1,
		},
		{
			name: "mixed mailbox",
			mails: []dnfrepo.MailRecord{
				invMail("7", func(m *dnfrepo.MailRecord) {
					m.Gold = 11
					m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2}}
				}),
				invMail("8", nil),
				invMail("9", func(m *dnfrepo.MailRecord) { m.Claimed = true }),
			},
			creatureFlags: []bool{false, false, false},
			wantSummaries: 3,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := buildMailboxListNotification(testCase.mails, 2, now)
			if err != nil {
				t.Fatalf("buildMailboxListNotification: %v", err)
			}
			walked := invWalkMailboxList(t, body, testCase.creatureFlags)
			if walked.summaryCount != testCase.wantSummaries {
				t.Fatalf("summaries=%d, want %d", walked.summaryCount, testCase.wantSummaries)
			}
			if walked.detailCount != len(testCase.mails) || int(walked.totalMails) != len(testCase.mails) {
				t.Fatalf("detail rows=%d u16 total=%d, want %d", walked.detailCount, walked.totalMails, len(testCase.mails))
			}
			if walked.notLoaded != 2 {
				t.Fatalf("not_loaded=%d, want 2", walked.notLoaded)
			}
			exportCurrentMailboxNativeFixture(t, testCase.name, body, testCase.creatureFlags)
		})
	}
}

// TestMailboxListBodyEmptyMailboxIsExactlySixBytes 钉死空邮箱的最小形状：
// u8 摘要数 0 + u8 clear 0 + u16 未加载 + u16 总数 0。
func TestMailboxListBodyEmptyMailboxIsExactlySixBytes(t *testing.T) {
	body, err := buildMailboxListNotification(nil, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("buildMailboxListNotification: %v", err)
	}
	if len(body) != 6 || body[0] != 0 || body[1] != 0 {
		t.Fatalf("empty mailbox body=% X, want 6 bytes starting 00 00", body)
	}
	walked := invWalkMailboxList(t, body, nil)
	if walked.summaryCount != 0 || walked.detailCount != 0 || walked.totalMails != 0 {
		t.Fatalf("empty mailbox walk=%+v", walked)
	}
	exportCurrentMailboxNativeFixture(t, "empty mailbox", body, nil)
}

// Opt-in offline fixtures use the production writer; no live repository is touched.
func exportCurrentMailboxNativeFixture(t *testing.T, name string, body []byte, creatures []bool) {
	t.Helper()
	dir := os.Getenv("DNF_MAIL_NATIVE_EXPORT")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(struct {
		Name      string
		Creatures []bool
	}{name, creatures})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".bin"), body, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), meta, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentMailboxRemoveNativeFixtures(t *testing.T) {
	dir := os.Getenv("DNF_MAIL_NATIVE_EXPORT")
	for index, ids := range [][]uint32{nil, {17}, {17, 29, 31}} {
		body := buildMailboxRemoveNotification(ids)
		if int(binary.LittleEndian.Uint32(body)) != len(ids) || len(body) != 4+4*len(ids) {
			t.Fatal("invalid removal body")
		}
		for n, id := range ids {
			if binary.LittleEndian.Uint32(body[4+4*n:]) != id {
				t.Fatal("invalid removal identity")
			}
		}
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, "remove"), 0755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, "remove", fmt.Sprint(index))
		if err := os.WriteFile(name+".bin", body, 0644); err != nil {
			t.Fatal(err)
		}
		meta, err := json.Marshal(ids)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name+".json", meta, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMailboxListBodySummaryCountMatchesU8Bound 钉死摘要行数的 u8 上限：
// 超过 255 行必须整体失败（回一个错误 ACK），绝不能写出被截断的行数。
func TestMailboxListBodySummaryCountMatchesU8Bound(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	mails := make([]dnfrepo.MailRecord, 0, 256)
	flags := make([]bool, 0, 256)
	for index := 0; index < 256; index++ {
		mails = append(mails, invMail(fmt.Sprintf("%d", index+1), func(m *dnfrepo.MailRecord) {
			m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}}
		}))
		flags = append(flags, false)
	}
	if _, err := buildMailboxListNotification(mails, 0, now); err == nil {
		t.Fatal("256 attachment summaries must be rejected instead of truncating the u8 count")
	}
	body, err := buildMailboxListNotification(mails[:255], 0, now)
	if err != nil {
		t.Fatalf("255 summaries must fit the u8 count: %v", err)
	}
	walked := invWalkMailboxList(t, body, flags[:255])
	if walked.summaryCount != 255 {
		t.Fatalf("summary count=%d, want 255", walked.summaryCount)
	}
}

// TestMailboxListBodyTextLimitFailsClosed 钉死正文长度上限：
// 超出上限的正文必须让整份列表构造失败（返回错误 ACK），而不是写出被截断的
// DSTR——截断会让客户端按错误长度继续读下去。
func TestMailboxListBodyTextLimitFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	long := make([]byte, 0, maxMailboxMessageBytes)
	for len(long) < maxMailboxMessageBytes {
		long = append(long, 'a')
	}
	okMail := invMail("11", func(m *dnfrepo.MailRecord) { m.Body = string(long) })
	body, err := buildMailboxListNotification([]dnfrepo.MailRecord{okMail}, 0, now)
	if err != nil {
		t.Fatalf("exactly %d body bytes must be accepted: %v", maxMailboxMessageBytes, err)
	}
	invWalkMailboxList(t, body, []bool{false})

	tooLong := invMail("12", func(m *dnfrepo.MailRecord) { m.Body = string(long) + "b" })
	if _, err := buildMailboxListNotification([]dnfrepo.MailRecord{tooLong}, 0, now); err == nil {
		t.Fatal("an over-long body must fail closed instead of writing a truncated DSTR")
	}
}

// TestMailboxListBodyRejectsInvalidMailIdentity 钉死"没有长度就整份失败"：
// 邮件 id 不是正整数、附件数量非法时构造器必须报错，绝不发一份字段数不完整的
// 列表给客户端。
func TestMailboxListBodyRejectsInvalidMailIdentity(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if _, err := buildMailboxListNotification([]dnfrepo.MailRecord{invMail("0", nil)}, 0, now); err == nil {
		t.Fatal("mail id 0 must be rejected")
	}
	if _, err := buildMailboxListNotification([]dnfrepo.MailRecord{invMail("abc", nil)}, 0, now); err == nil {
		t.Fatal("non-numeric mail id must be rejected")
	}
	invalid := invMail("13", func(m *dnfrepo.MailRecord) {
		m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 0}}
	})
	if _, err := buildMailboxListNotification([]dnfrepo.MailRecord{invalid}, 0, now); err == nil {
		t.Fatal("attachment count 0 must be rejected")
	}
}

// TestMailboxSmallNotificationBodiesKeepTheirFixedWidths 钉死其余下行包体的
// 宽度：0x62 删除通知 = u32 数量 + 4×n；0x63 未读提醒 = 2 字节；
// op95 领取 ACK = 1 + 4 + 12×n；op134 状态 ACK = 1 + 4 + 6×n。
func TestMailboxSmallNotificationBodiesKeepTheirFixedWidths(t *testing.T) {
	if body := buildMailboxAlarmNotification(-1); len(body) != 2 || binary.LittleEndian.Uint16(body) != 0 {
		t.Fatalf("alarm body=% X, want one zero word", body)
	}
	if body := buildMailboxAlarmNotification(70000); len(body) != 2 || binary.LittleEndian.Uint16(body) != 0xffff {
		t.Fatalf("clamped alarm body=% X, want ffff", body)
	}
	ids := []uint32{7, 8, 9}
	remove := buildMailboxRemoveNotification(ids)
	if len(remove) != 4+4*len(ids) || binary.LittleEndian.Uint32(remove) != uint32(len(ids)) {
		t.Fatalf("remove body=% X", remove)
	}
	for index, id := range ids {
		got := binary.LittleEndian.Uint32(remove[4+index*4:])
		if got != id {
			t.Fatalf("remove[%d]=%d, want %d", index, got, id)
		}
	}
	extract := buildExtractAck(ids)
	if len(extract) != 5+12*len(ids) || extract[0] != 1 || binary.LittleEndian.Uint32(extract[1:]) != uint32(len(ids)) {
		t.Fatalf("extract ack=% X", extract)
	}
	for index := range ids {
		offset := 5 + index*12
		if binary.LittleEndian.Uint32(extract[offset:]) != ids[index] || binary.LittleEndian.Uint32(extract[offset+4:]) != ids[index] || binary.LittleEndian.Uint32(extract[offset+8:]) != 0 {
			t.Fatalf("extract ack row %d must match current summary claim object and keep mode zero: % X", index, extract[offset:offset+12])
		}
	}
	state := buildChangeStateAck(ids, 2)
	if len(state) != 5+6*len(ids) || state[0] != 1 || binary.LittleEndian.Uint32(state[1:]) != uint32(len(ids)) {
		t.Fatalf("state ack=% X", state)
	}
	for index := range ids {
		offset := 5 + index*6
		if binary.LittleEndian.Uint16(state[offset+4:]) != 2 {
			t.Fatalf("state ack row %d status=%d, want 2", index, binary.LittleEndian.Uint16(state[offset+4:]))
		}
	}
	// 打开 ACK 固定三字节：0x01 + u16 未加载数量。
	if body := buildMailboxOpenAck(70000); len(body) != 3 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:]) != 0xffff {
		t.Fatalf("open ack=% X", body)
	}
}
