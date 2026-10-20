package dnfbridge

import (
	"fmt"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件承载「收集箱（COLLECT_BOX_WINDOW）」的**服务端目录**读取。
//
// 真源是运行期 Script.pvf 的 `localization/collectbox/collectboxinfo.etc`，
// 与 86JP / A21 两套参考实现（Server/DfoServer/Game/Inventory/CollectBoxDataService.cs、
// Tool/PvfLib/Models/CollectBoxFile.cs）读的是同一份文档。客户端那份
// `ui/collectbox/collectbox.xui` 里的三个 `CNUIControlText` 默认值就是
// `STR:test string1/2/3`（第三个 TextColor=255,0,0 红字），**只有服务端把箱子状态
// 下发到位，客户端才会用本文件的 [UI Explain 1/2/3] 覆盖它们**。所以
// `test stringN` 永远不会、也不允许成为服务端的取值来源：本文件里没有任何
// 占位串，用例 `TestCurrentCollectboxCatalogHasNoClientPlaceholderText` 钉住这一点。
const (
	// currentCollectboxInfoPVFPath 是收集箱槛位/到期时间/UI 文案的唯一真源。
	currentCollectboxInfoPVFPath = "localization/collectbox/collectboxinfo.etc"
	// currentCollectboxBuffInfoPVFPath 是每个槛位等级对应的属性加成（86JP 未在
	// 下发路径上使用；保留它只为取证与后续"服务端算加成"留口）。
	currentCollectboxBuffInfoPVFPath = "localization/collectbox/collectboxbuffinfo.etc"
	// currentCollectboxSectionName 是 .etc 里每个箱子的段落名（5 个箱子 = 5 段）。
	currentCollectboxSectionName = "Collect Box Info"
	// currentCollectboxExpiryLocation 是 PVF 到期时间的时区。
	//
	// 判据（厂商 9.16 参考环境 packet_log 逐字节对照）：箱 5 的
	// `[base expiration date] = 2028-12-31 06:00:00`，厂商在
	// 2026-09-25T10:19:36.2400554Z 下发 class0/op1027 的 u32 = 0x0442eca7
	// = 71494823；71494823 s 正是 2028-12-30T22:00:00Z − 该时刻（= 东八区
	// 2028-12-31 06:00:00，误差 <1s 为取整），所以到期时间按 UTC+8 解释。
	currentCollectboxExpiryLocationOffsetSeconds = 8 * 3600
)

// currentCollectboxExpiryLocation 是固定 UTC+8（无夏令时），与参考实现的
// DateTime.Now（中国区机器）一致。
var currentCollectboxExpiryLocation = time.FixedZone("UTC+8", currentCollectboxExpiryLocationOffsetSeconds)

// currentCollectboxSlot 是箱子里的一个槛位（[Item Position Info] 的一对）。
// ItemID <= 0（实测为 -1）表示该槛位在 PVF 里是空位。
type currentCollectboxSlot struct {
	ItemID int32
	// PositionCode 是 PVF 里紧跟物品 id 的那个数（实测 1/2/3），参考实现把它读作
	// slot.Count；本实现只原样保留，不参与任何下发判断。
	PositionCode int32
}

// currentCollectboxBox 是一个收集箱（PVF 的一个 [Collect Box Info] 段）。
type currentCollectboxBox struct {
	Index int32
	// Title / BuffFormat / ExpiryFormat 分别对应 PVF 的 [UI Explain 1/2/3]，
	// 也正是客户端 xui 里 test string1/2/3 三个文本控件的替换值来源。
	Title        string
	BuffFormat   string
	ExpiryFormat string
	Explain      string
	BackImage    string

	BaseExpiration    time.Time
	HasBaseExpiration bool
	MaxExpiration     time.Time
	HasMaxExpiration  bool

	ExtendItemIDs []int32

	SlotRows    int
	SlotsPerRow int
	Slots       []currentCollectboxSlot
}

// SlotCount 是 PVF 声明的槛位总数（[Item Position Info] 的首两个数是
// 行数与每行个数，总数 = 行数 × 每行个数，不是行数——参考实现为此专门留了注释）。
func (b currentCollectboxBox) SlotCount() int {
	return b.SlotRows * b.SlotsPerRow
}

// HasSlotItem 报告 itemID 是否属于本箱的槛位清单（用来把"下发的条目 id"钉在
// PVF 上；不属于 PVF 的 id 一律不下发）。
func (b currentCollectboxBox) HasSlotItem(itemID int32) bool {
	if itemID <= 0 {
		return false
	}
	for _, slot := range b.Slots {
		if slot.ItemID == itemID {
			return true
		}
	}
	return false
}

// currentCollectboxCatalog 是同一份 .etc 解析出的全部箱子，按 PVF 出现顺序。
type currentCollectboxCatalog struct {
	Path  string
	Boxes []currentCollectboxBox
}

// Indexes 按 PVF 顺序返回全部箱号（厂商 9.16 的初始化就是按这个顺序逐箱下发）。
func (c currentCollectboxCatalog) Indexes() []int32 {
	indexes := make([]int32, 0, len(c.Boxes))
	for _, box := range c.Boxes {
		indexes = append(indexes, box.Index)
	}
	return indexes
}

// Box 按箱号取箱子。
func (c currentCollectboxCatalog) Box(index int32) (currentCollectboxBox, bool) {
	for _, box := range c.Boxes {
		if box.Index == index {
			return box, true
		}
	}
	return currentCollectboxBox{}, false
}

// loadCurrentCollectboxCatalog 从 PVF 源读取并解析收集箱目录。
func loadCurrentCollectboxCatalog(source dnfpvf.Source) (currentCollectboxCatalog, error) {
	if source == nil {
		return currentCollectboxCatalog{}, fmt.Errorf("collectbox pvf source is required")
	}
	text, err := source.ReadText(currentCollectboxInfoPVFPath)
	if err != nil {
		return currentCollectboxCatalog{}, fmt.Errorf("read %s: %w", currentCollectboxInfoPVFPath, err)
	}
	return parseCurrentCollectboxCatalog(currentCollectboxInfoPVFPath, text)
}

// parseCurrentCollectboxCatalog 解析 `localization/collectbox/collectboxinfo.etc`。
//
// 文档结构（实测真档，5 段）：每个箱子以 [Collect Box Info] 开始，段内依次是
// [Index] / [base expiration date] / [max expiration date] / [Extend Item] /
// [Item Position Info] / [UI Back Image] / [UI Slot Image] / [UI Slot Image Info] /
// [UI Btn Image] / [Box Explain] / [UI Explain 1..3]，以 [/Collect Box Info] 结束。
// 解析器是扁平的，所以这里按顺序遍历全部 section，用 [Collect Box Info] 切分箱子。
func parseCurrentCollectboxCatalog(path, text string) (currentCollectboxCatalog, error) {
	document, err := dnfpvf.Parse(path, text)
	if err != nil {
		return currentCollectboxCatalog{}, fmt.Errorf("parse %s: %w", path, err)
	}
	catalog := currentCollectboxCatalog{Path: path}
	wanted := strings.ToLower(currentCollectboxSectionName)
	var current *currentCollectboxBox
	flush := func() {
		if current != nil && current.Index > 0 {
			catalog.Boxes = append(catalog.Boxes, *current)
		}
		current = nil
	}
	for _, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		tokens := currentCollectboxSectionTokens(document, section)
		switch {
		case strings.HasPrefix(name, "/"):
			if name == "/"+wanted {
				flush()
			}
			continue
		case name == wanted:
			flush()
			current = &currentCollectboxBox{}
			continue
		}
		if current == nil {
			continue
		}
		switch name {
		case "index":
			if value, ok := currentCollectboxFirstInt(tokens); ok {
				current.Index = int32(value)
			}
		case "base expiration date":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.BaseExpiration, current.HasBaseExpiration = parseCurrentCollectboxDate(raw)
			}
		case "max expiration date":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.MaxExpiration, current.HasMaxExpiration = parseCurrentCollectboxDate(raw)
			}
		case "extend item":
			for idx := 0; idx+1 < len(tokens); idx += 2 {
				if tokens[idx].Kind != dnfpvf.TokenInt || tokens[idx+1].Kind != dnfpvf.TokenInt {
					continue
				}
				current.ExtendItemIDs = append(current.ExtendItemIDs, int32(tokens[idx].Int))
			}
		case "item position info":
			currentCollectboxParsePositionInfo(current, tokens)
		case "ui explain 1":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.Title = raw
			}
		case "ui explain 2":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.BuffFormat = raw
			}
		case "ui explain 3":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.ExpiryFormat = raw
			}
		case "box explain":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.Explain = raw
			}
		case "ui back image":
			if raw, ok := currentCollectboxFirstText(tokens); ok {
				current.BackImage = raw
			}
		}
	}
	flush()
	if len(catalog.Boxes) == 0 {
		return currentCollectboxCatalog{}, fmt.Errorf("%s has no %q section", path, currentCollectboxSectionName)
	}
	return catalog, nil
}

// currentCollectboxParsePositionInfo 读 [Item Position Info]：前两个数是行数与
// 每行槛位数，随后是 行数×每行 对 (itemId, positionCode)。
func currentCollectboxParsePositionInfo(box *currentCollectboxBox, tokens []dnfpvf.Token) {
	if len(tokens) < 2 || tokens[0].Kind != dnfpvf.TokenInt || tokens[1].Kind != dnfpvf.TokenInt {
		return
	}
	box.SlotRows = int(tokens[0].Int)
	box.SlotsPerRow = int(tokens[1].Int)
	total := box.SlotCount()
	if total <= 0 {
		return
	}
	box.Slots = make([]currentCollectboxSlot, 0, total)
	for idx := 2; idx+1 < len(tokens) && len(box.Slots) < total; idx += 2 {
		if tokens[idx].Kind != dnfpvf.TokenInt || tokens[idx+1].Kind != dnfpvf.TokenInt {
			continue
		}
		box.Slots = append(box.Slots, currentCollectboxSlot{
			ItemID:       int32(tokens[idx].Int),
			PositionCode: int32(tokens[idx+1].Int),
		})
	}
}

func currentCollectboxSectionTokens(document *dnfpvf.Document, section dnfpvf.Section) []dnfpvf.Token {
	if document == nil || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return nil
	}
	return document.Tokens[section.Start:section.End]
}

func currentCollectboxFirstInt(tokens []dnfpvf.Token) (int64, bool) {
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			return token.Int, true
		}
	}
	return 0, false
}

func currentCollectboxFirstText(tokens []dnfpvf.Token) (string, bool) {
	for _, token := range tokens {
		if token.Kind != dnfpvf.TokenString {
			continue
		}
		value := strings.TrimSpace(token.Value)
		if value != "" {
			return value, true
		}
	}
	return "", false
}

// parseCurrentCollectboxDate 解析 PVF 的 `2028-12-31 06:00:00` 形式到期时间，
// 时区固定 UTC+8（判据见 currentCollectboxExpiryLocationOffsetSeconds）。
func parseCurrentCollectboxDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if value, err := time.ParseInLocation(layout, raw, currentCollectboxExpiryLocation); err == nil {
			return value, true
		}
	}
	return time.Time{}, false
}

// currentCollectboxBoxTitle 返回日志/用例里用的箱子名字（[UI Explain 1]，
// 例如箱 5 = 兽人守护神），取不到时回落到空串（绝不生成占位串）。
func currentCollectboxBoxTitle(box currentCollectboxBox) string {
	return strings.TrimSpace(box.Title)
}
