// 本文件提供 MySQL 落库路径上"偏离默认口径、但仍然正确"的分支的事件出口。
//
// 为什么需要这一层：O-A 的批量钳位、批量语句过大时回落逐行、O-B 的"增量 key 集合
// 不可用 ⇒ 回落整容器写"，都是**沉默地仍然正确、但会变慢**的路径。它们必须可观测，
// 否则操作者无法判断开关是否真的生效（也无法区分"增量写生效"与"回退到整容器")。
//
// 本层拿不到 dnfbridge 的 logger（仓储不反向依赖服务层），因此默认写 slog 默认日志，
// 与 platform/adminops/adminops.go:140 的 slog.Default().Error、以及
// services/dnfbridge/item_expiration_projection.go:381-384 的
// "生产实现写 slog 默认日志"同形。组合根可以在需要时用 SetWriteEventObserver 把它
// 接到 packet_log 事件上；本文件不引入任何跨层依赖。
package mysql

import (
	"log/slog"
	"sync"
)

// WriteEvent 是落库路径上的一条可观测事件。
type WriteEvent struct {
	// Kind 取值：
	//   insert_batch_invalid            批量开关取值非法 ⇒ 已回落逐行（= 今天）
	//   insert_batch_clamped            批量开关超过硬上限 ⇒ 已钳到上限
	//   insert_batch_packet_too_large   多行 INSERT 被服务端/驱动拒绝 ⇒ 本批回落逐行
	//   item_stack_keys_unavailable     增量 slot key 为空 ⇒ 整容器重写
	//   equipment_entry_keys_unavailable 增量 entry key 为空 ⇒ 整容器重写
	Kind       string
	Table      string
	Collection string
	OwnerID    string
	Rows       int
	Requested  int
	Detail     string
	Err        error
}

var (
	writeEventMu       sync.RWMutex
	writeEventObserver = defaultWriteEventObserver
)

// SetWriteEventObserver 替换事件观察者；传 nil = 恢复默认（slog 默认日志）。
// 默认观察者永远非 nil，所以这些分支不会静默。
func SetWriteEventObserver(observer func(WriteEvent)) {
	writeEventMu.Lock()
	defer writeEventMu.Unlock()
	if observer == nil {
		writeEventObserver = defaultWriteEventObserver
		return
	}
	writeEventObserver = observer
}

func notifyWriteEvent(event WriteEvent) {
	writeEventMu.RLock()
	observer := writeEventObserver
	writeEventMu.RUnlock()
	if observer == nil {
		return
	}
	observer(event)
}

func defaultWriteEventObserver(event WriteEvent) {
	fields := []any{"kind", event.Kind, "table", event.Table}
	if event.Collection != "" {
		fields = append(fields, "collection", event.Collection)
	}
	if event.OwnerID != "" {
		fields = append(fields, "owner_id", event.OwnerID)
	}
	if event.Rows > 0 {
		fields = append(fields, "rows", event.Rows)
	}
	if event.Requested > 0 {
		fields = append(fields, "requested", event.Requested)
	}
	if event.Detail != "" {
		fields = append(fields, "detail", event.Detail)
	}
	if event.Err != nil {
		fields = append(fields, "error", event.Err)
	}
	slog.Default().Warn("dnf-mysql-write-event", fields...)
}
