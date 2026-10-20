package worldmap

// HasSection 报告这份 area 文档是否**声明过**名为 name 的段落（大小写不敏感，
// 前后空白忽略；`[hell quest]` 这种块状段落与单行段落都算）。
//
// 用途：把"解析器读到了 0 条 `[hell quest]` 任务号"与"文档里根本没有这一段"
// 区分开。两者对进图门槛的判定都是"该区域没有深渊门槛"，但日志要给得出
// `pvf_status`，不能把"缺段落"含混成"解析失败"。判定只看 ParseArea 保留的
// SourceSections（真档 PVF 的原始段落清单），不新造任何数据。
func (area Area) HasSection(name string) bool {
	key := sectionKey(name)
	if key == "" {
		return false
	}
	for _, section := range area.SourceSections {
		if sectionKey(section.Name) == key {
			return true
		}
	}
	return false
}
