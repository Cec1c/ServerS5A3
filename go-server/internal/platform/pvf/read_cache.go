package pvf

// ReleaseReadCaches 释放可重建的解压块和脚本文本，保留归档字节、字符串池及文件索引。
// 已返回的字符串和原始数据副本仍然有效；之后读取会按相同的解码逻辑重新填充缓存。
// 可与读取并发调用，但在途读取可能在清理后重新填充，所以启动预载结束是最佳调用点。
func (a *Archive) ReleaseReadCaches() {
	if a == nil {
		return
	}
	a.texts.Clear()
	a.chunks.Clear()
	if cache := a.readBudget.Load(); cache != nil {
		cache.texts.clear()
		cache.chunks.clear()
	}
}
