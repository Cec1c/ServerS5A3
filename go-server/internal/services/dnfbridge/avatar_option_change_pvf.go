package dnfbridge

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// parseAvatarSelectAbilityChangeGrades 读 stackable 的
// `[avatar select ability change]` 段，返回每行的 AvatarGrade 列。
//
// # 段形状（真档 PVF 实测，2026-09-26）
//
// 47 个 stackable 带该段（全部箱子），每行形如
//
//	`[source scope]` `[target scope]` <avatar grade> <param>
//
// 例：稀有箱 `stackable/cash/avatar_optionchange1.stk`（item 2660452，现场抓包
// 里被右键的就是它）= “ `[all]` `[all]` 3 0 “；高级箱
// `stackable/cash/avatar_optionchange11.stk`（item 2660834）= “ `[all]` `[all]` 2 1 “。
// 字段与厂商 PvfLib `StackableItemFile.ParseAvatarSelectAbilityChanges`
// （`Tool/PvfLib/Models/StackableItemFile.cs:1137-1168`）逐列一致：
// SourceScope / TargetScope 是前两个反引号作用域，AvatarGrade = 该行第一个整数，
// Param = 第二个整数。
//
// **这里只取 AvatarGrade**：厂商的业务判定
// `InventoryAvatarOptionChangeService.CanChangeAvatarGrade` 只比
// `entry.AvatarGrade == avatarGrade`，两个 scope 一概不校验。多拦一条就会把现场
// 已验证合法的操作（例如 `avatar_optionchange2.stk` 的 source scope 是 `[swordman]`）
// 变成服务端拒绝，因此按厂商口径不引入 scope 白名单。
//
// 解析按 token 行号分行（PVF 解析器给每个 token 带 Line）：一行里没有整数时，它只
// 是在声明作用域（厂商同一处理：`ParseInts` 为空则记下 pendingSourceScope）。
func parseAvatarSelectAbilityChangeGrades(document *dnfpvf.Document) []int64 {
	if document == nil {
		return nil
	}
	section, ok := document.Section("avatar select ability change")
	if !ok {
		return nil
	}
	grades := make([]int64, 0, 1)
	currentLine := 0
	lineHasInt := false
	for _, token := range section {
		if token.Kind == dnfpvf.TokenSection {
			// 段内出现新的 `[xxx]` 头时，本段的取值部分已经结束。
			break
		}
		if token.Line != currentLine {
			currentLine = token.Line
			lineHasInt = false
		}
		if token.Kind != dnfpvf.TokenInt {
			continue
		}
		if !lineHasInt {
			grades = append(grades, token.Int)
			lineHasInt = true
		}
	}
	return grades
}
