package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件是"真档 PVF"回归：`DNFBRIDGE_REAL_PVF_SMOKE` 指向 runtime 的 Script.pvf 时才跑。
//
// 它钉死三件互相独立的事实（数字全部来自 2026-09-25 对
// `runtime\data\dnf\Script.pvf` 的全量取证）：
//
//  1. 目录真源 `etc/epicpieceinfo.etc [equipment piece drop info]/[piece list]`
//     有 **966** 组 (成品装备 id, 碎片 id) —— 与厂商 9.16 参考服务端自己的
//     `EVENT kind=dnf-epic-book-catalog-loaded entries=966` 逐数相等；
//  2. 每个碎片 id 在 `stackable/stackable.lst` 里都映射到 `epicPiece/<id>.stk`
//     （即"碎片"是一族有自己路径前缀的可堆叠品，而不是猜出来的 id 区间）；
//  3. **根因夹具**：同一批碎片 id 还出现在 `Etc/Independent_Drop.etc` 里
//     （独立掉落表），所以它们本来就会被当普通材料投到地面、捡进背包 ——
//     这正是操作者报的"史诗碎片进背包"。
func TestRealScriptPVFEpicPieceCatalogCoversTheIndependentDropFragments(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := loadCurrentEpicPieceCatalog(archive)
	if err != nil {
		t.Fatalf("load real %s: %v", currentEpicPieceInfoPath, err)
	}
	// (1) 966 = 厂商 `dnf-epic-book-catalog-loaded entries=966`。
	if catalog.count() != 966 {
		t.Fatalf("real PVF epic-piece catalog entries=%d want 966 (re-derive from %s and the vendor log)",
			catalog.count(), currentEpicPieceInfoPath)
	}
	for _, piece := range []struct {
		id     uint32
		output uint32
	}{
		{10300000, 450004},    // 普拉塔尼的黄金石碎片（[piece list] 首行）
		{10304121, 116030153}, // 阿迪奥斯碎片（[piece list] 末段）
	} {
		if !catalog.isEpicPiece(piece.id) {
			t.Fatalf("real PVF lost epic piece %d", piece.id)
		}
		if output, found := catalog.outputFor(piece.id); !found || output != piece.output {
			t.Fatalf("outputFor(%d)=%d found=%t want %d", piece.id, output, found, piece.output)
		}
	}
	// 反向：10303749 是 `stackable/10303001/10303749.stk`（装备增幅保护券），
	// 同处 1030xxxx 区间但**不是**史诗碎片。它是本次线上库里唯一一件该区间的道具，
	// 因此这条负例同时防止"按 id 区间判断"这种错误实现复活。
	if catalog.isEpicPiece(10303749) {
		t.Fatal("real PVF: 10303749 is the amplification-protection ticket, not an epic piece")
	}

	// (2) 每个碎片 id 都指向 `stackable/epicPiece/<id>.stk`。
	stackablePaths, _, err := loadDungeonDropList(archive, dungeonDropStackableList)
	if err != nil {
		t.Fatalf("load %s: %v", dungeonDropStackableList, err)
	}
	mismatched := 0
	for _, pieceID := range catalog.pieces {
		path, found := stackablePaths[int64(pieceID)]
		if !found || !strings.HasPrefix(strings.ToLower(strings.TrimPrefix(path, "stackable/")), "epicpiece/") {
			mismatched++
		}
	}
	if mismatched != 0 {
		t.Fatalf("%d/%d epic pieces are not stackable/epicPiece entries in %s",
			mismatched, catalog.count(), dungeonDropStackableList)
	}

	// (3) 根因夹具：碎片 id 出现在独立掉落表里 ⇒ 本服务端会把它们当普通材料投到地面。
	dropText, err := archive.ReadText(currentDungeonIndependentDropPath)
	if err != nil {
		t.Fatalf("read %s: %v", currentDungeonIndependentDropPath, err)
	}
	document, err := dnfpvf.Parse(currentDungeonIndependentDropPath, dropText)
	if err != nil {
		t.Fatalf("parse %s: %v", currentDungeonIndependentDropPath, err)
	}
	dropIDs := make(map[uint32]struct{})
	for _, token := range document.Tokens {
		if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > 0xffffffff {
			continue
		}
		dropIDs[uint32(token.Int)] = struct{}{}
	}
	overlap := 0
	for pieceID := range catalog.pieceSet {
		if _, found := dropIDs[pieceID]; found {
			overlap++
		}
	}
	// 2026-09-25 实测：该表出现 244 个 1030xxxx 道具 id，其中 236 个是碎片。
	if overlap < 200 {
		t.Fatalf("only %d/%d epic pieces appear in %s; the root cause evidence no longer holds",
			overlap, catalog.count(), currentDungeonIndependentDropPath)
	}
	t.Logf("real PVF epic pieces=%d independent-drop overlap=%d", catalog.count(), overlap)
}
