// 本文件定义权威父记录存在性读取能力。
package repository

import "context"

// RecordPresenceReader 只判断权威父记录是否存在，不读取子表。
type RecordPresenceReader interface {
	Exists(context.Context, string) (bool, error)
}
