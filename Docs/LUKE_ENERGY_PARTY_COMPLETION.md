# 卢克能源图的成员完成入口

能源控制室5063与能源储藏室5066通过PVF定义的被动对象销毁完成。原路径只完成报告者；共享场景已清除仍不能代表其他成员已有个人结果入口或奖励。

新增分派只在销毁事件被接受后执行，并逐成员校验当前绑定代次、权威队伍成员、同一场景与run、账号/频道及相同PVF对象条件，然后调用该成员原有完成owner。被动对象不伪装成hostile，不发送伪造的op38，不替成员提交个人结果或奖励。

公开基底的普通完成分派没有部分op31失败恢复。候选仅在新增Luke校验通过、boss完成已接受且个人入口尚未发送时复用同一owner续发；普通nil-accept caller保持原行为。个人结果、资产事务和队长返回扇出规则保持。

## 验证

公开基底232dc427的对照：两人各自上报两图通过；仅一个成员上报两图失败。候选目标45叶通过、0失败、0跳过，包含两图完整入场/完成/个人资产结算/队长72回城、连续两次op31失败恢复、退队与身份代次/owner/run拒绝、保存/最终提交失败回滚，以及普通caller保持对照和既有回归。

真实资源测试需显式设置 `DNFBRIDGE_LUKE_PARTY_RETURN_PVF`。未设置时跳过，显式指定缺失或摘要不符时失败；适配资源SHA256为 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。两图真实对象分别为64062、64060。

从go-server运行：

```text
go test -buildvcs=false -count=1 -run "^TestR151Luke(EnergyPartyReturn|SelfReportControl|CompletionBoundaries)$|^TestR151PublicOrdinary" ./internal/services/dnfbridge
```

测试采用公开内存fixture和完整upper handler。角色与前序raid状态由fixture建立，测试请求不是客户端现场捕获；GUI表现和实际玩家问题仍需独立验收。故障注入不证明历史线上发生过写失败。本变更未部署，不改变团本132或statistic既有规则。
