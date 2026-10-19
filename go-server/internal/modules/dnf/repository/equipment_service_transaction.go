package repository

import "context"

// EquipmentServiceUnitOfWork 将封装、跨界的扣材、来源删除和目标邮件放在同一事务。
// 两个角色必须同属指定账号；返回的角色快照只用于校验，不能写入其它角色。
// Group 仅暴露指定账号、来源库存和目标邮箱，不得嵌套其它事务。
type EquipmentServiceUnitOfWork interface {
	WithinEquipmentServiceAssets(context.Context, string, string, string,
		func(Group, CharacterRecord, CharacterRecord) error) error
}
