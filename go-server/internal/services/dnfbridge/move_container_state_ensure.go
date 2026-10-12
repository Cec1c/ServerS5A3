package dnfbridge

// 567r2 候选（本席自有目录；不覆盖 567）。op19 在进入域处理（库存持久化 / ACK /
// 刷新构建）之前，按**唯一工厂** newCharacterContainerStateSettings 做一次
// missing-only 的容器状态准备。
//
// 授权边界完全复述既有代码，不新增任何 gate：
//   - class：Registry.Route 的 `dnfenum.GameCmd(cmd) != GameCmdCommand` 不处理；
//   - 路由：`alignedcmd.Classify(typ)` 必须登记且不是 ActionBlocked；
//   - body：必须是当前 28 字节语义形（legacy 侧已由 normalizeLegacyGameBody 归一化）；
//   - 角色：域层 Owner.Move 以 SelectedCharacterID==0 拒绝（ErrCharacterRequired）。
// 只有全部满足、且命令确实涉及 list 0/1/2 时才介入；否则返回 nil 让旧路径照旧处理，
// 一个字节都不写（错误 class / 畸形 body / 未选角色 / 非 0/1/2 容器都保持旧语义）。
//
// 介入后语义与 inventory.ContainerStateOwner.Ensure 一致（不新造第二种口径）：
// 缺失→用该工厂建合法初值；合法→原样不动；非法(不在当前 EXE 容量表内)→错误，
// 不覆盖错误数据；settings 读失败 / 仓储不可用→错误。任一出错由调用方拒绝本次
// 移动：不发 ACK、不落库存、不凭空补容量。

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var errMoveContainerStateUnavailable = errors.New("op19 container state repository is unavailable")

// op19ContainerStateEnsureApplies 复述既有授权/形状边界（见文件顶注）。
func op19ContainerStateEnsureApplies(cmd byte, typ uint16, session *gameSession, body []byte) bool {
	if session == nil || session.selectedCharacterID == 0 {
		// 与域层 ErrCharacterRequired 同层：未选角色不介入。
		return false
	}
	if dnfenum.GameCmd(cmd) != dnfenum.GameCmdCommand {
		return false
	}
	decision, known := alignedcmd.Classify(typ)
	if !known || decision.Action == alignedcmd.ActionBlocked {
		return false
	}
	parsed, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return false
	}
	for _, listType := range []byte{parsed.SourceListType, parsed.DestinationListType} {
		switch listType {
		case 0, 1, 2:
			return true
		}
	}
	return false
}

// prepareMoveContainerState 是唯一写入点：最多一次 Ensure，绝不重复。
func (s *Service) prepareMoveContainerState(session *gameSession, cmd byte, typ uint16, repos dnfrepo.Group, body []byte) error {
	if !op19ContainerStateEnsureApplies(cmd, typ, session, body) {
		return nil
	}
	if repos.Settings == nil {
		// 仓储不可用属 fail-closed：不把"读不到"当成合法空状态继续移动。
		return errMoveContainerStateUnavailable
	}
	owner, err := dnfinventory.NewContainerStateOwner(repos.Settings)
	if err != nil {
		return err
	}
	if _, err := owner.Ensure(context.Background(), dnfinventory.EnsureContainerStateCommand{
		CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10),
		Initial:     newCharacterContainerStateSettings,
	}); err != nil {
		return err
	}
	return nil
}
