package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件是「NPC 金币费用 → 材料」转换规则的**只读 sidecar**：从运行时 PVF 的
// npc/npc.lst 解出 NPC id → 文档引用，再读该 .npc 文档的
// [upgrade gold convert to material] 段。它只回答"这个 npcID 在 PVF 里是什么"，
// 不报价、不扣费、不读角色或背包——领域原子报价与结算在父代理的路径里。

// 真档取证（2026-10-15，只读）：
//   - npc/npc.lst：单行 token 流「id + 反引号包裹的 .npc 文件名」，共 1144 条、
//     32997 字节；476 → 476_Jonathan.npc（**大写 J**）。归档里的实际路径是小写
//     npc/476_jonathan.npc，platformpvf 的 ReadText 走 pathKey 小写查找，所以
//     lst 里的大小写原样拿去读也能命中。
//   - map/town/centralpark/centralpark_metrocenter.map： [NPC]476 位置 (873,200)
//     ⇒ 客户端 op50 尾部那个 476 就是 npc.lst 的 476，不是别的编号空间。
//   - npc/476_Jonathan.npc 的段体就是两个正整数：10158964 与 100000。
//   - 对整个 npc/ 前缀做文本扫描，"convert to material" 只命中 npc/476_jonathan.npc
//     一个文件 ⇒ 真档里只有这一只 NPC 声明该规则，普通 NPC（如 id 1 Linus.npc、
//     id 2 Seria.npc）都没有这一段。

// 语义边界（调用方必须按三档区分，不要合并）：
//   - converts=true,  err=nil：规则存在，返回 (材料 id, 单位金币)；
//   - converts=false, err=nil：NPC 存在但没有这一段（合法普通 NPC）；
//   - err!=nil：NPC 不在 npc.lst / 文档读不到 / 段畸形 / 段重复 / 引用越界。

// 三条"不静默"的加固（对应 [upgrade gold convert to material] 是**单值**规则这件事）：
//  1. 段出现两次（哪怕内容相同）⇒ 报错：.npc 里 [dialog] 这类可重复段是合法的，所以
//     "重复"必须逐个判，不能按第一个命中就静默取用。
//  2. 段体不是「恰两个正整数」⇒ 报错（少一个、多一个、浮点、字符串、0、负数都算坏）。
//  3. 两个值必须落在当前 wire/PVF 的 int32 内（材料 id 与金币金额都按 int32 走）⇒ 超上限报错。
// 另外 lst 引用必须留在 list 目录以内：绝对路径、含 .. 的引用、以及拼出来越出 npc/ 的
// 路径都报错，绝不去读范围外的文档（一份被改坏的 npc.lst 不能把我们指到别的资源上）。

// 0xFFFF（无 NPC）与"外层根本没有 NPC"由调用方处理：本函数只按 id 查 PVF，不替调用方
// 决定"这次请求算不算有 NPC"。
const currentUpgradeNPCListPath = "npc/npc.lst"

// currentUpgradeNPCConversionSection 是「金币费用转材料」的段名（真档 476 的段体为
// 10158964 100000 = 材料 id + 单位金币）。
const currentUpgradeNPCConversionSection = "upgrade gold convert to material"

var (
	// errCurrentUpgradeNPCConversionUnavailable：运行时 PVF 目录拿不到（Service 未装配或打开失败）。
	errCurrentUpgradeNPCConversionUnavailable = errors.New("dnf current upgrade NPC conversion is unavailable")
	// errCurrentUpgradeNPCUnknown：这个 id 不在 npc/npc.lst 里（含 0xFFFF）。
	errCurrentUpgradeNPCUnknown = errors.New("dnf current upgrade NPC is not listed in npc/npc.lst")
)

// currentUpgradeNPCConversionRule 是一条已解析规则；converts=false 时两个值都是 0。
type currentUpgradeNPCConversionRule struct {
	MaterialItemID int64
	UnitGold       int64
	Converts       bool
}

// currentUpgradeNPCConversionCacheKey 把缓存钉在**这一个运行时 PVF 目录**上
// （与 currentUpgradeProtectTicketCacheKey 同口径）：同一个 npcID 在不同 PVF 下可能
// 不是同一只 NPC，只用 npcID 做键会在换档后读到上一版的规则。
type currentUpgradeNPCConversionCacheKey struct {
	archive *platformpvf.Archive
	npcID   uint16
}

var (
	currentUpgradeNPCConversionMu    sync.Mutex
	currentUpgradeNPCConversionCache = make(map[currentUpgradeNPCConversionCacheKey]currentUpgradeNPCConversionRule)

	// currentUpgradeNPCIndexCache 按目录缓存 npc.lst 的 id → 引用路径。
	currentUpgradeNPCIndexMu    sync.Mutex
	currentUpgradeNPCIndexCache = make(map[*platformpvf.Archive]map[uint16]string)
)

// parseCurrentUpgradeNPCList 把 npc/npc.lst 解析成 id → 引用路径。
//
// 重复 id 直接报错：id→文档必须是单值映射，出现第二份同 id 就说明这份表被改坏了
// （口径同 worldmap/npc_enter_town.go 的 refs 解析）。超出 uint16 的 id（真档里有
// 400000012 这类 9 位 id）与空引用直接跳过：前者永远无法被 uint16 查询命中。
func parseCurrentUpgradeNPCList(listPath, rawText string) (map[uint16]string, error) {
	document, err := dnfpvf.Parse(listPath, rawText)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", listPath, err)
	}
	refs := make(map[uint16]string)
	for _, entry := range dnfpvf.ParseList(document) {
		reference := strings.TrimSpace(entry.Path)
		if reference == "" || entry.ID <= 0 || entry.ID > math.MaxUint16 {
			continue
		}
		id := uint16(entry.ID)
		if existing, ok := refs[id]; ok {
			return nil, fmt.Errorf("%s: duplicate npc id %d (%q and %q)", listPath, id, existing, reference)
		}
		refs[id] = reference
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("%s: no npc entries", listPath)
	}
	return refs, nil
}

// resolveCurrentUpgradeNPCReference 把 lst 引用拼成归档内路径，并守住"只读 list 目录以内"。
//
// 拼接口径与 worldmap/source_load.go 的 resolveSourceReference 相同（引用本身已带该目录就
// 原样用，否则按 list 所在目录拼接）；不同之处是这里**失败关闭**：空引用、绝对路径、
// 含 .. 段、或最终路径越出 list 目录，都返回 error，绝不静默去读范围外的文档。
func resolveCurrentUpgradeNPCReference(listPath, reference string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(reference, "\\", "/"))
	if raw == "" {
		return "", fmt.Errorf("%s: empty npc reference", listPath)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%s: npc reference %q must be relative", listPath, reference)
	}
	cleanReference := strings.TrimPrefix(raw, "./")
	if cleanReference == "" {
		return "", fmt.Errorf("%s: empty npc reference", listPath)
	}
	for _, segment := range strings.Split(cleanReference, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%s: npc reference %q must not traverse upwards", listPath, reference)
		}
	}
	listDirectory := strings.TrimSuffix(path.Dir(strings.ReplaceAll(listPath, "\\", "/")), "/")
	if listDirectory == "." || listDirectory == "" {
		if cleanReference != path.Base(cleanReference) {
			return "", fmt.Errorf("%s: npc reference %q must stay inside the list directory", listPath, reference)
		}
		return cleanReference, nil
	}
	resolved := cleanReference
	lowerReference := strings.ToLower(cleanReference)
	lowerDirectory := strings.ToLower(listDirectory)
	if lowerReference != lowerDirectory && !strings.HasPrefix(lowerReference, lowerDirectory+"/") {
		resolved = path.Join(listDirectory, cleanReference)
	}
	resolved = path.Clean(resolved)
	lowerResolved := strings.ToLower(resolved)
	if lowerResolved != lowerDirectory && !strings.HasPrefix(lowerResolved, lowerDirectory+"/") {
		return "", fmt.Errorf("%s: npc reference %q resolves outside %s", listPath, reference, listDirectory)
	}
	return resolved, nil
}

// parseCurrentUpgradeNPCConversionRule 从已解析的 .npc 文档取转换规则。
//
//   - 没有这一段 ⇒ (0, 0, false, nil)：合法的普通 NPC，不是错误；
//   - 段出现两次及以上 ⇒ error（单值规则，绝不静默取第一个）；
//   - 段体不是「恰两个正整数」或超出 int32 ⇒ error。
func parseCurrentUpgradeNPCConversionRule(document *dnfpvf.Document) (int64, int64, bool, error) {
	if document == nil {
		return 0, 0, false, errors.New("upgrade npc conversion: document is nil")
	}
	matching := make([][]dnfpvf.Token, 0, 1)
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), currentUpgradeNPCConversionSection) {
			continue
		}
		if section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
			return 0, 0, false, fmt.Errorf("%s: [%s] section range is invalid (%d..%d)", document.Path, currentUpgradeNPCConversionSection, section.Start, section.End)
		}
		matching = append(matching, document.Tokens[section.Start:section.End])
	}
	switch len(matching) {
	case 0:
		return 0, 0, false, nil
	case 1:
	default:
		return 0, 0, false, fmt.Errorf("%s: [%s] is declared %d times, want exactly one", document.Path, currentUpgradeNPCConversionSection, len(matching))
	}
	values := make([]int64, 0, 2)
	for _, token := range matching[0] {
		if token.Kind != dnfpvf.TokenInt {
			return 0, 0, false, fmt.Errorf("%s: [%s] must contain exactly two positive integers, found %s %q",
				document.Path, currentUpgradeNPCConversionSection, token.Kind, token.Raw)
		}
		values = append(values, token.Int)
	}
	if len(values) != 2 {
		return 0, 0, false, fmt.Errorf("%s: [%s] must contain exactly two positive integers, found %d",
			document.Path, currentUpgradeNPCConversionSection, len(values))
	}
	if values[0] <= 0 || values[1] <= 0 {
		return 0, 0, false, fmt.Errorf("%s: [%s] values must be positive, found %d %d",
			document.Path, currentUpgradeNPCConversionSection, values[0], values[1])
	}
	if values[0] > math.MaxInt32 || values[1] > math.MaxInt32 {
		return 0, 0, false, fmt.Errorf("%s: [%s] values must fit the current wire int32 range (max %d), found %d %d",
			document.Path, currentUpgradeNPCConversionSection, math.MaxInt32, values[0], values[1])
	}
	return values[0], values[1], true, nil
}

// currentUpgradeNPCIndex 读取并缓存 npc/npc.lst 的 id → 引用路径。
func (s *Service) currentUpgradeNPCIndex(archive *platformpvf.Archive) (map[uint16]string, error) {
	if archive == nil {
		return nil, errCurrentUpgradeNPCConversionUnavailable
	}
	currentUpgradeNPCIndexMu.Lock()
	index, ok := currentUpgradeNPCIndexCache[archive]
	currentUpgradeNPCIndexMu.Unlock()
	if ok {
		return index, nil
	}
	text, err := archive.ReadText(currentUpgradeNPCListPath)
	if err != nil {
		return nil, fmt.Errorf("upgrade npc conversion: read %s: %w", currentUpgradeNPCListPath, err)
	}
	parsed, err := parseCurrentUpgradeNPCList(currentUpgradeNPCListPath, text)
	if err != nil {
		return nil, err
	}
	currentUpgradeNPCIndexMu.Lock()
	currentUpgradeNPCIndexCache[archive] = parsed
	currentUpgradeNPCIndexMu.Unlock()
	return parsed, nil
}

// currentUpgradeNPCConversion 回答"这个 npcID 在运行时 PVF 里的金币→材料转换规则是什么"。
//
// 这是本文件唯一的对外入口。它不报价、不扣费、不读角色与背包，也不判断"这次请求有没有
// NPC"：调用方拿 converts 与 err 自行决定（见文件头三档语义）。
func (s *Service) currentUpgradeNPCConversion(npcID uint16) (int64, int64, bool, error) {
	if s == nil {
		return 0, 0, false, errCurrentUpgradeNPCConversionUnavailable
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return 0, 0, false, fmt.Errorf("upgrade npc conversion: runtime PVF unavailable: %w", err)
	}
	index, err := s.currentUpgradeNPCIndex(archive)
	if err != nil {
		return 0, 0, false, err
	}
	reference, listed := index[npcID]
	if !listed {
		return 0, 0, false, fmt.Errorf("%w: npc %d", errCurrentUpgradeNPCUnknown, npcID)
	}
	cacheKey := currentUpgradeNPCConversionCacheKey{archive: archive, npcID: npcID}
	currentUpgradeNPCConversionMu.Lock()
	cached, ok := currentUpgradeNPCConversionCache[cacheKey]
	currentUpgradeNPCConversionMu.Unlock()
	if ok {
		return cached.MaterialItemID, cached.UnitGold, cached.Converts, nil
	}
	documentPath, err := resolveCurrentUpgradeNPCReference(currentUpgradeNPCListPath, reference)
	if err != nil {
		return 0, 0, false, err
	}
	text, err := archive.ReadText(documentPath)
	if err != nil {
		return 0, 0, false, fmt.Errorf("upgrade npc conversion: read %s (npc %d): %w", documentPath, npcID, err)
	}
	document, err := dnfpvf.Parse(documentPath, text)
	if err != nil {
		return 0, 0, false, fmt.Errorf("upgrade npc conversion: parse %s (npc %d): %w", documentPath, npcID, err)
	}
	materialID, unitGold, converts, err := parseCurrentUpgradeNPCConversionRule(document)
	if err != nil {
		return 0, 0, false, err
	}
	// 只缓存成功解析（含 converts=false）：读/解析/畸形失败不缓存，失败要么复现要么进日志。
	parsed := currentUpgradeNPCConversionRule{MaterialItemID: materialID, UnitGold: unitGold, Converts: converts}
	currentUpgradeNPCConversionMu.Lock()
	currentUpgradeNPCConversionCache[cacheKey] = parsed
	currentUpgradeNPCConversionMu.Unlock()
	return parsed.MaterialItemID, parsed.UnitGold, parsed.Converts, nil
}
