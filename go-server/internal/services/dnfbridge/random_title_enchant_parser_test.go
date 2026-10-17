package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 随机称号附魔（2017 劳动节）解析回归。
//
// 覆盖 etc/randomtitleenchant.etc 的 [index] 结构解析与加权行抽取：
//   - 配置头 + 行头（rowID/选项数/权重）+ 选项（职业/技能/等级）的结构；
//   - 0 权重行永远不参与抽取，区间边界按真实权重切分；
//   - 全零（含无行）配置、int64 总权重溢出、损坏行必须拒绝而不是抽样或回退；
//   - 真实运行时 PVF 的 14 个配置 10001..10014、14 颗宝珠
//     490700960/490700964..490700976 与三个称号 400330094/400330096/400330101
//     走通 resolver，且随机结果必须落在该配置的真实正权重行集合里。
//
// 真实表允许同一个配置被声明多次；解析保留首次声明，因此真实 PVF 用例不断言配置总数，
// 也不假设每个配置 ID 只声明一次。

// randomTitleEnchantParserSource 是 etc/randomtitleenchant.etc 的内存 PVF 来源。
type randomTitleEnchantParserSource map[string]string

func (source randomTitleEnchantParserSource) ReadText(relativePath string) (string, error) {
	text, ok := source[relativePath]
	if !ok {
		return "", fmt.Errorf("read %s: %w", relativePath, os.ErrNotExist)
	}
	return text, nil
}

// randomTitleEnchantIndexDocument 是一份覆盖解析形状的合成 [index] 表：
// 重复声明的配置、同一配置内重复声明的行、一个两选项行、一个 0 权重行、
// 非 index 段，以及不能被当成配置头的段（0 / 非整数 / 超出 uint16）。
const randomTitleEnchantIndexDocument = "" +
	"[index]\n" +
	"10001 1 1 100 `[swordman]` 4 1 2 1 30 `[swordman]` 6 2\n" +
	"1 1 7 `[swordman]` 9 9\n" +
	"3 1 0 `[swordman]` 9 3\n" +
	"[/index]\n" +
	"[random title enchant]\n" +
	"30001 1 1 100 `[swordman]` 4 1\n" +
	"[/random title enchant]\n" +
	"[index]\n" +
	"10001 1 1 999 `[swordman]` 4 1\n" +
	"2 1 5 `[swordman]` 7 1\n" +
	"[/index]\n" +
	"[index]\n" +
	"10002 4 2 9 `[swordman]` 10 1 `[fighter]` 11 2\n" +
	"[/index]\n" +
	"[index]\n" +
	"0 1 1 5 `[swordman]` 9 3\n" +
	"[/index]\n" +
	"[index]\n" +
	"`[swordman]` 5 1 1 100\n" +
	"[/index]\n" +
	"[index]\n" +
	"70000 1 1 5 `[swordman]` 9 3\n" +
	"[/index]\n"

func TestParseCurrentRandomTitleEnchantCatalogIndexStructure(t *testing.T) {
	catalog, err := parseCurrentRandomTitleEnchantCatalog(randomTitleEnchantParserSource{
		currentRandomTitleEnchantPath: randomTitleEnchantIndexDocument,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint16]currentRandomTitleEnchantConfig{
		10001: {
			ID: 10001,
			Rows: []currentRandomTitleEnchantRow{
				{ID: 1, Weight: 100, Options: []currentRandomTitleEnchantOption{{Job: "[swordman]", SkillID: 4, Level: 1}}},
				{ID: 2, Weight: 30, Options: []currentRandomTitleEnchantOption{{Job: "[swordman]", SkillID: 6, Level: 2}}},
				{ID: 3, Weight: 0, Options: []currentRandomTitleEnchantOption{{Job: "[swordman]", SkillID: 9, Level: 3}}},
			},
		},
		10002: {
			ID: 10002,
			Rows: []currentRandomTitleEnchantRow{
				{ID: 4, Weight: 9, Options: []currentRandomTitleEnchantOption{
					{Job: "[swordman]", SkillID: 10, Level: 1},
					{Job: "[fighter]", SkillID: 11, Level: 2},
				}},
			},
		},
	}
	if !reflect.DeepEqual(catalog.Configs, want) {
		t.Fatalf("catalog configs = %+v, want %+v", catalog.Configs, want)
	}
	// 重复声明的配置必须保留首次声明，不能被后面的声明替换，也不能合并两组行。
	rows := catalog.Configs[10001].Rows
	if len(rows) != 3 || rows[0].Weight != 100 {
		t.Fatalf("duplicate config declaration changed the retained rows: %+v", rows)
	}
	// 同一配置内重复声明的行同样保留首次声明。
	if rows[1].ID != 2 || rows[1].Weight != 30 {
		t.Fatalf("duplicate row declaration changed the retained rows: %+v", rows)
	}
	if _, ok := catalog.Configs[30001]; ok {
		t.Fatal("a non-index section was parsed as a random title enchant config")
	}
	// 两选项行按顺序保留每个选项的职业/技能/等级。
	if got := catalog.Configs[10002].Rows[0].Options; len(got) != 2 || got[1].Job != "[fighter]" || got[1].SkillID != 11 || got[1].Level != 2 {
		t.Fatalf("two-option row options = %+v", got)
	}
}

func TestParseCurrentRandomTitleEnchantCatalogSourceFailures(t *testing.T) {
	if _, err := parseCurrentRandomTitleEnchantCatalog(nil); !errors.Is(err, errRandomTitleEnchantCatalogUnavailable) {
		t.Fatalf("nil source error = %v, want %v", err, errRandomTitleEnchantCatalogUnavailable)
	}
	if _, err := parseCurrentRandomTitleEnchantCatalog(randomTitleEnchantParserSource{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing document error = %v, want the read failure", err)
	}
	// 未闭合的 PVF 字符串是文档级损坏，必须作为解析失败上报，不能被当成行错误或合法数据。
	damaged := randomTitleEnchantParserSource{
		currentRandomTitleEnchantPath: "[index]\n10001 1 1 100 `[swordman] 4 1\n",
	}
	if _, err := parseCurrentRandomTitleEnchantCatalog(damaged); err == nil || errors.Is(err, errRandomTitleEnchantRowsInvalid) {
		t.Fatalf("unterminated PVF string error = %v, want a document parse failure", err)
	}
}

func TestParseCurrentRandomTitleEnchantCatalogRejectsDamagedRows(t *testing.T) {
	cases := []struct {
		name string
		rows string
	}{
		{name: "row header without weight", rows: "1 100"},
		{name: "row id zero", rows: "0 1 100 `[swordman]` 4 1"},
		{name: "row id beyond uint16", rows: "70000 1 100 `[swordman]` 4 1"},
		{name: "row without options", rows: "1 0 100"},
		{name: "row with three options", rows: "1 3 100 `[swordman]` 4 1 `[swordman]` 5 1 `[swordman]` 6 1"},
		{name: "negative weight", rows: "1 1 -1 `[swordman]` 4 1"},
		{name: "option without level", rows: "1 1 100 `[swordman]` 4"},
		{name: "second option without level", rows: "1 2 100 `[swordman]` 4 1 `[swordman]` 5"},
		{name: "option skill zero", rows: "1 1 100 `[swordman]` 0 1"},
		{name: "option level zero", rows: "1 1 100 `[swordman]` 4 0"},
		{name: "option job blank", rows: "1 1 100 `` 4 1"},
		{name: "option job not a value", rows: "1 1 100 5 4 1"},
		{name: "leftover row header tokens", rows: "1 1 100 `[swordman]` 4 1 5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := randomTitleEnchantParserSource{
				currentRandomTitleEnchantPath: "[index]\n10001 " + tc.rows + "\n[/index]\n",
			}
			if _, err := parseCurrentRandomTitleEnchantCatalog(source); !errors.Is(err, errRandomTitleEnchantRowsInvalid) {
				t.Fatalf("error = %v, want %v", err, errRandomTitleEnchantRowsInvalid)
			}
		})
	}
}

func TestChooseCurrentRandomTitleEnchantRowSkipsZeroWeightRows(t *testing.T) {
	rows := []currentRandomTitleEnchantRow{
		{ID: 10, Weight: 3},
		{ID: 11, Weight: 0},
		{ID: 12, Weight: 2},
		{ID: 13, Weight: 1},
	}
	// 区间边界：0 权重行不占区间，后面的行顺延。
	for roll, want := range map[int64]uint16{0: 10, 2: 10, 3: 12, 4: 12, 5: 13} {
		if got := chooseCurrentRandomTitleEnchantRow(rows, roll); got.ID != want {
			t.Fatalf("roll %d selected row %d, want %d", roll, got.ID, want)
		}
	}
	counts := map[uint16]int{}
	for roll := int64(0); roll < 6; roll++ {
		got := chooseCurrentRandomTitleEnchantRow(rows, roll)
		if got.ID == 11 {
			t.Fatalf("roll %d selected the zero-weight row", roll)
		}
		counts[got.ID]++
	}
	// 抽取区间按真实权重守恒：权重 3/0/2/1 必须给出 3/–/2/1 的覆盖次数。
	if counts[10] != 3 || counts[12] != 2 || counts[13] != 1 {
		t.Fatalf("row counts = %+v, want the declared weights 3/2/1", counts)
	}
	// 超出总权重的 roll 不属于任何行，返回空行而不是最后一行。
	if got := chooseCurrentRandomTitleEnchantRow(rows, 6); got.ID != 0 {
		t.Fatalf("roll beyond the total weight selected row %d, want the empty row", got.ID)
	}
	allZero := []currentRandomTitleEnchantRow{{ID: 20, Weight: 0}, {ID: 21, Weight: 0}}
	for _, roll := range []int64{0, 1, 5} {
		if got := chooseCurrentRandomTitleEnchantRow(allZero, roll); got.ID != 0 {
			t.Fatalf("all-zero rows selected row %d for roll %d", got.ID, roll)
		}
	}
	if got := chooseCurrentRandomTitleEnchantRow(nil, 0); got.ID != 0 {
		t.Fatalf("empty rows selected row %d", got.ID)
	}
}

func TestCurrentRandomTitleEnchantCatalogChooseRejections(t *testing.T) {
	overflow := fmt.Sprintf("%d", int64(math.MaxInt64))
	catalog, err := parseCurrentRandomTitleEnchantCatalog(randomTitleEnchantParserSource{
		currentRandomTitleEnchantPath: "" +
			"[index]\n10001 1 1 0 `[swordman]` 4 1 2 1 0 `[swordman]` 6 2\n[/index]\n" +
			"[index]\n10002\n[/index]\n" +
			"[index]\n10003 1 1 " + overflow + " `[swordman]` 4 1 2 1 " + overflow + " `[swordman]` 6 2\n[/index]\n" +
			"[index]\n10004 1 1 0 `[swordman]` 4 1 2 1 5 `[swordman]` 6 2\n[/index]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 全零权重配置与没有行的配置都必须拒绝：没有可抽取的行时不能回退到任何行。
	for _, configID := range []uint16{10001, 10002} {
		if _, err := catalog.choose(configID); !errors.Is(err, errRandomTitleEnchantRowsInvalid) {
			t.Fatalf("choose(%d) error = %v, want %v", configID, err, errRandomTitleEnchantRowsInvalid)
		}
	}
	// 总权重溢出 int64 必须在抽样之前拒绝，不能回绕成一个负数或更小的选择区间。
	if _, err := catalog.choose(10003); !errors.Is(err, errRandomTitleEnchantRowsInvalid) {
		t.Fatalf("overflow choose error = %v, want %v", err, errRandomTitleEnchantRowsInvalid)
	}
	if _, err := catalog.choose(10015); !errors.Is(err, errRandomTitleEnchantConfigMissing) {
		t.Fatalf("missing config error = %v, want %v", err, errRandomTitleEnchantConfigMissing)
	}
	// 混合权重配置只允许抽出正权重行，抽出的行必须带着它的技能选项。
	for attempt := 0; attempt < 64; attempt++ {
		row, err := catalog.choose(10004)
		if err != nil {
			t.Fatalf("choose(10004): %v", err)
		}
		if row.ID != 2 || row.Weight != 5 || len(row.Options) != 1 {
			t.Fatalf("zero-weight row was drawable: %+v", row)
		}
	}
}

// TestRealPVFRandomTitleEnchantCatalogAndResolver 用当前客户端 Script.pvf 覆盖
// 2017 劳动节随机称号附魔的完整真实资源：14 个配置、14 颗宝珠和 3 个称号。
func TestRealPVFRandomTitleEnchantCatalogAndResolver(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	randomCatalog, err := parseCurrentRandomTitleEnchantCatalog(archive)
	if err != nil {
		t.Fatalf("parse %s: %v", currentRandomTitleEnchantPath, err)
	}
	beads := []struct {
		itemID   int64
		configID uint16
	}{
		{itemID: 490700960, configID: 10001},
		{itemID: 490700964, configID: 10002},
		{itemID: 490700965, configID: 10003},
		{itemID: 490700966, configID: 10004},
		{itemID: 490700967, configID: 10005},
		{itemID: 490700968, configID: 10006},
		{itemID: 490700969, configID: 10007},
		{itemID: 490700970, configID: 10008},
		{itemID: 490700971, configID: 10009},
		{itemID: 490700972, configID: 10010},
		{itemID: 490700973, configID: 10011},
		{itemID: 490700974, configID: 10012},
		{itemID: 490700975, configID: 10013},
		{itemID: 490700976, configID: 10014},
	}
	titles := []int64{400330094, 400330096, 400330101}

	// ① 每个配置都在，并且至少有一行正权重行；真实表确实声明了 0 权重行，
	// 抽取结果必须始终落在正权重集合内。
	positiveRows := make(map[uint16]map[uint16]currentRandomTitleEnchantRow, len(beads))
	for _, bead := range beads {
		config, ok := randomCatalog.Configs[bead.configID]
		if !ok {
			t.Fatalf("real config %d (bead %d) is missing from %s", bead.configID, bead.itemID, currentRandomTitleEnchantPath)
		}
		positive := make(map[uint16]currentRandomTitleEnchantRow, len(config.Rows))
		for _, row := range config.Rows {
			if row.Weight > 0 {
				positive[row.ID] = row
			}
		}
		if len(positive) == 0 {
			t.Fatalf("real config %d has no positive-weight row", bead.configID)
		}
		positiveRows[bead.configID] = positive
		for attempt := 0; attempt < 32; attempt++ {
			row, err := randomCatalog.choose(bead.configID)
			if err != nil {
				t.Fatalf("choose config %d: %v", bead.configID, err)
			}
			if row.ID == 0 {
				t.Fatalf("config %d drew the empty row", bead.configID)
			}
			if _, ok := positive[row.ID]; !ok {
				t.Fatalf("config %d drew row %d outside the real positive-weight rows", bead.configID, row.ID)
			}
			if len(row.Options) == 0 {
				t.Fatalf("config %d drew row %d without a skill option", bead.configID, row.ID)
			}
		}
	}

	// ② resolver：每颗宝珠配上每个真实称号都必须解析出一个真实、可抽取的
	// config/row 对，并且不把随机称号宝珠当成怪物卡片。
	t.Run("every real bead and title pair resolves to a drawable row", func(t *testing.T) {
		for _, bead := range beads {
			for _, title := range titles {
				resolution, err := resolveCurrentEnchantBeadMetadata(catalog, archive, bead.itemID, title)
				if err != nil {
					t.Fatalf("resolver bead=%d title=%d: %v", bead.itemID, title, err)
				}
				if resolution.RandomTitleEnchantConfigID != bead.configID {
					t.Fatalf("bead %d resolved config %d, want %d", bead.itemID, resolution.RandomTitleEnchantConfigID, bead.configID)
				}
				if resolution.CardItemID != 0 {
					t.Fatalf("random-title bead %d resolved as monster card %d", bead.itemID, resolution.CardItemID)
				}
				if !slices.Contains(resolution.TargetWhitelist, title) {
					t.Fatalf("bead %d whitelist %v does not carry title %d", bead.itemID, resolution.TargetWhitelist, title)
				}
				if resolution.TargetEquipmentType != "[title name]" || !slices.Contains(resolution.AllowedEquipmentTypes, "[title name]") {
					t.Fatalf("bead %d title %d target=%q allowed=%v", bead.itemID, title, resolution.TargetEquipmentType, resolution.AllowedEquipmentTypes)
				}
				if _, ok := positiveRows[bead.configID][resolution.RandomTitleEnchantRowID]; !ok {
					t.Fatalf("bead %d title %d resolved row %d outside the real positive-weight rows of config %d",
						bead.itemID, title, resolution.RandomTitleEnchantRowID, bead.configID)
				}
			}
		}
	})

	// ③ 拒绝：真实武器（存在且是装备）不在宝珠白名单里，因此不能产生任何 config/row 对。
	t.Run("real non-title equipment produces no catalog pair", func(t *testing.T) {
		const weaponItemID = int64(35381)
		definition, err := catalog.ResolveItem(uint32(weaponItemID))
		if err != nil {
			t.Fatalf("resolve weapon %d: %v", weaponItemID, err)
		}
		if definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("weapon %d kind = %s, want %s", weaponItemID, definition.Kind, dungeonDropItemEquipment)
		}
		for _, bead := range beads {
			resolution, err := resolveCurrentEnchantBeadMetadata(catalog, archive, bead.itemID, weaponItemID)
			if err != nil {
				t.Fatalf("resolver bead=%d weapon=%d: %v", bead.itemID, weaponItemID, err)
			}
			if resolution.RandomTitleEnchantConfigID != 0 || resolution.RandomTitleEnchantRowID != 0 {
				t.Fatalf("bead %d on weapon %d produced config=%d row=%d, want a refusal",
					bead.itemID, weaponItemID, resolution.RandomTitleEnchantConfigID, resolution.RandomTitleEnchantRowID)
			}
		}
	})
}
