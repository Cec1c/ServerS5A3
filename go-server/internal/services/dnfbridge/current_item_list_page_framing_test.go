package dnfbridge

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 【2026-09-24 紧急回滚后的契约，临时】装扮栏（list 1）的容器页每行 = 0x77 + (socket,color)
// 两段长度前缀 = 127 字节。收尾批次一度把页面行压到 0x77（想让"装扮页同一套时装显示多份"
// 消失），但全量封包日志里**唯一一次**那样发的登录（22:04 conn game-000003，
// packet_log.20260924-220805.txt，entry_size=119）客户端当场 0xC0000005 崩在原生 EXE 的
// 定长读行帧里（eip=0x03B10747 / 返回地址 0x034E3AD9），其余所有会话都是 127 且登录正常。
// 恢复计划写在这一节对应的 changlog.md 条目里：装扮页重复必须用客户端实测重定契约。
func TestAvatarContainerPageKeepsOptionalBlobPrefixes(t *testing.T) {
	entries := []currentItemListEntry{{}, {}}
	for i := range entries {
		entries[i].avatarSocketData = []byte{0xEF, 0xFF, 0, 0, 0, 0, 0xEF, 0xFF, 0, 0, 0, 0}
		entries[i].avatarColorData = []byte{1, 2, 3, 4}
	}
	page := buildCurrentItemListBody(1, entries, dnfrepo.CharacterContainerState{AvatarExpansion: 56})
	perRow := currentItemListEntryWireSize + (4 + len(entries[0].avatarSocketData)) + (4 + len(entries[0].avatarColorData))
	wantPage := 1 + 2 + 2 + len(entries)*perRow
	if len(page) != wantPage {
		t.Fatalf("list-1 page body = %d bytes, want %d (listType + expansion + count + N*(0x77 + two length-prefixed blobs))", len(page), wantPage)
	}
	if got := currentItemListEntrySizeForType(1); got != currentItemListEntryWireSize+8 {
		t.Fatalf("list-1 page row size = %d, want %d", got, currentItemListEntryWireSize+8)
	}
	if page[0] != 1 || page[3] != 2 {
		t.Fatalf("list-1 page header changed: % X", page[:5])
	}

	// msg 14 单行载体同样保留两段长度前缀的可选 blob（开孔/镶嵌/购买时的孔位向量靠它下发）。
	row := buildCurrentItemUpdateBody(1, entries[:1])
	wantRow := 1 + 2 + currentItemListEntryWireSize + (4 + len(entries[0].avatarSocketData)) + (4 + len(entries[0].avatarColorData))
	if len(row) != wantRow {
		t.Fatalf("list-1 update row body = %d bytes, want %d (row + two length-prefixed optional blobs)", len(row), wantRow)
	}
}

// 其它 list 的页面行数不变（防止这次改动顺手改错）：
// list 0/1/2 的页头是 listType + u16 容器档位 + u16 行数（5 字节），
// list 7/38 只有 listType + u16 行数（3 字节），list 2 末尾还有一个空分组字节。
func TestOtherContainerPagesKeepFixedRowSize(t *testing.T) {
	entries := []currentItemListEntry{{}, {}, {}}
	for _, tc := range []struct {
		listType byte
		header   int
		trailer  int
	}{{0, 5, 0}, {2, 5, 1}, {7, 3, 0}, {38, 3, 0}} {
		page := buildCurrentItemListBody(tc.listType, entries, dnfrepo.CharacterContainerState{})
		want := tc.header + len(entries)*currentItemListEntryWireSize + tc.trailer
		if len(page) != want {
			t.Fatalf("list %d page body = %d bytes, want %d", tc.listType, len(page), want)
		}
	}
}
