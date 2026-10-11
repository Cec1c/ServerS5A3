package quest

import (
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// questPresentationText 读取一个「展示用长文本」段，语义与上游 0.1.8
// dnfquest.questPresentationText 完全一致：
//
//   - 取该段的**全部**文本值，逐段 TrimSpace 后丢弃空段；
//   - 段内出现的 `%%` 还原成 `%`（PVF 的转义写法）；
//   - 多段用 "\n" 连接；段不存在或全为空时返回 ""。
//
// 它**不合成任何文案**：PVF 没有这个段就是空串，调用方（GM 面板）据此显示
// 「PVF 未提供」。90CN 真档实测的段存在率见 catalog.go 的 Definition 注释。
func questPresentationText(doc *dnfpvf.Document, section string) string {
	if doc == nil {
		return ""
	}
	parts := make([]string, 0, 1)
	for _, value := range doc.Texts(section) {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, strings.ReplaceAll(value, "%%", "%"))
		}
	}
	return strings.Join(parts, "\n")
}
