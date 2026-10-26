# 卢克能源图的成员完成入口

能源控制室5063与能源储藏室5066通过PVF定义的被动对象销毁完成。原路径只完成报告者；共享场景已清除仍不能代表其他成员已有个人结果入口或奖励。

新增分派只在销毁事件被接受后执行，并逐成员校验当前绑定代次、权威队伍成员、同一场景与run、账号/频道及相同PVF对象条件，然后调用该成员原有完成owner。被动对象不伪装成hostile，不发送伪造的op38，不替成员提交个人结果或奖励。

当前公共基底已支持普通完成入口写失败恢复。Luke 校验通过后复用该共享 owner，boss完成已接受且个人入口尚未发送时续发；普通 nil-accept caller 的既有恢复能力保留。个人结果、资产事务和队长返回扇出规则保持。

## 验证

公开基底232dc427的对照：两人各自上报两图通过；仅一个成员上报两图失败。候选目标45叶通过、0失败、0跳过，包含两图完整入场/完成/个人资产结算/队长72回城、连续两次op31失败恢复、退队与身份代次/owner/run拒绝、保存/最终提交失败回滚，以及普通caller保持对照和既有回归。

真实资源测试需显式设置 `DNFBRIDGE_LUKE_PARTY_RETURN_PVF`。未设置时跳过，显式指定缺失或摘要不符时失败；适配资源SHA256为 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。两图真实对象分别为64062、64060。

从go-server运行：

```text
go test -buildvcs=false -count=1 -run "^TestR151Luke(EnergyPartyReturn|SelfReportControl|CompletionBoundaries)$|^TestR151PublicOrdinary" ./internal/services/dnfbridge
```

测试采用公开内存fixture和完整upper handler。角色与前序raid状态由fixture建立，测试请求不是客户端现场捕获；GUI表现和实际玩家问题仍需独立验收。故障注入不证明历史线上发生过写失败。本变更未部署，不改变团本132或statistic既有规则。

## 累计候选的资源与回归补充

能源控制室5063的boss地图74232声明唯一被动对象64062；能源储藏室5066的boss地图74247声明唯一被动对象64060。两张dgn的 `[clear condition][destroy object]` 与摆放一致。当前合法74B op39（owner=ffff、count=0）走被动对象完成入口；旧实现仅完成报告者，提前返回，未触发普通怪的成员完成分派。测试上，双方分别上报可以完成个人结算并回城；仅一人上报时队员收不到op31。

修复只把这两图已接受的销毁事实送到当前同实例成员的完成owner。报告者解锁后分派；接收方事件循环持本人的锁复核角色绑定和代次、权威队伍成员、同一Room/Scene/地图/迷宫、同一raid run及参与者和真实PVF条件。复用既有个人完成及op31失败恢复，不发被动对象op38，不模拟成员op46/op123，不替成员领取奖励，不修改回城或资产事务规则。

验证入口：`DNFBRIDGE_LUKE_PARTY_RETURN_PVF=<上述精确PVF路径>`，运行 `TestR149LukeEnergyPartyReturn`、`TestR149LukeSelfReportControl`、`TestR149LukeCompletionBoundaries`。未选择资源时跳过；明确选择后缺失/摘要不符失败。新测试复用累计仓库的raid fixture和R133断言helper，公开master兼容不在本次声明内。

完整目标链为op16组队入场、37/45真实资源房间、单方39、成员个人31、各自46/123、实际结算plan/事务、队长72。认证、组队大厅及前序团本历史为合法内存fixture，目标副本和奖励链由真实handler执行；codec形状请求不是原生客户端抓包。

保留：未报个人结果不能随队长出图；两次op31写失败后的同对象重放只续发一次入口；退队/旧代次/错误run/owner拒绝；保存及最终提交失败保持个人奖励未提交、不带出、三聚合回滚，恢复后同入口可完成。R133普通怪/shared clear与个人结算回归继续执行。

边界：缺op123时个人回城门与既有队长fanout的判据不同，op132团本路线也不等价op72。这两项仅独立观察，本次不修改其语义。此前测试fixture缺Skill和exp导致的plan错误不计业务红。原始报障的客户端销毁发送时序、结果界面及实际回城仍需实机验收；不将本地可复现缺口直接认定为历史现场唯一根因。
