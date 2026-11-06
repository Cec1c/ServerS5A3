# 单人安徒恩最终翻牌后自动回城

单人安徒恩完成最终地图并显示已提交的团队牌后，奖励完成队列只刷新牌面，角色仍留在心脏地下城。重复投影被去重；客户端也不会发出普通地下城翻牌菜单的回城请求。`115` 是首领死亡确认，不能用单字节通知代替城镇切换。

本修复在原会话完成队列中调用现有的角色重建及城镇切换流程，使用入场时冻结的城镇落点。它是服务端回城衔接，不生成奖励、不重置周次数、不修改客户端或 PVF。单人最终团本开窗的客户端配方已有 [独立实现](../scripts/client/anton-single-reward-window/README.md)；开窗与回城分别验证。

## 准入与展示时间

- 仅限单人安徒恩的最终地图；普通地图、团队安徒恩、卢克和超时空沿用原流程。
- 重新确认账号、角色、会话代次、实例、run、phase、attempt、地图、入场参与及已接受通关记录。
- 周次数及两类奖励均已提交，当前会话已成功收到对应结果和最终团队牌。
- 等待 PVF 的完整牌面展示窗口。当前配置为 20 秒；迟到的投影从本会话实际显示时刻计时，重复投影不延长时间。
- 延迟回调回到原会话事件队列并再次核对归属；旧会话或已替换实例不得被带回城。准备失败保留原实例供原有完成队列重试，完成后不重复切图或发奖。

## 验证

使用匹配的完整资源运行现有桥接包专项，数据库仅使用内存仓储：

```text
DNFBRIDGE_REAL_PVF_SMOKE=/path/to/Script.pvf go -C go-server test -buildvcs=false -count=1 -run 'TestRealPVFSingleAnton(FinishedCardsReturnToTownOnce|CompletionQueueReturnsAfterFinalDisplay|RewardReturnRejectsStaleAndUncommittedOwners|ReturnPreparationFailureRetainsOwnedRuntime|LateFinalCardsKeepTheirDisplayWindow)$' ./internal/services/dnfbridge
```

没有指定资源时专项会跳过，跳过不计为通过。用实际 socket 字节检查城镇切换及牌面不重发，并比较奖励资产。其他团本和普通地图的既有回归同时保留。

下游累计实例于 2026-11-06 获玩家确认：真正的最终团本翻牌正常，稍作等待后自动回城；日志中团队牌发送至城镇队伍恢复间隔约 20.4 秒。该确认仅覆盖单人安徒恩最终翻牌至回城；卢克、超时空、多人和稀有金牌仍需各自验收。公开补丁仍应先通过常规 PR 检查，再按部署流程应用。
