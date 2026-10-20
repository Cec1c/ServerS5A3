# 绝望之塔结果页操作与配套EXE

用户test99于23:18反馈结果仅时间无物品，结果页返回城镇/再次挑战/选择其它地下城点击无效，且再次挑战应置灰。验收服当时仍是!59结果/进度版fe12bdd，!60奖励尚未部署，故348仅7字节空列表。日志23:16:28/23:17:18/23:17:48连续通关11008/11009/11010，并在选择器从持久化记录得到2/3层；不将日志推进代替用户全部验收。

23:16:38 op72 {1,2}、23:17:26 {1,1}、23:17:54 {1,0}被普通卡牌屏障拒绝，layout/selection/reward均false、phase=result_shown。focus {2,x}有ACK；另走op42的返城成功。本改动为tower marker=1的op72添加专用操作路径，继续要求准确角色/completion plan、已持久化完成、46/123统计、ResultShown及348成功发送。只允许town/selector，retry及未知操作不通过。不制造普通卡奖励或放宽普通副本门槛。town复用既有冻结路线，失败保留owner，续发不重复成功前缀或ACK；selector复用现有op15/疲劳/op27流程，仍受队长/所有权检查。

## 当前EXE取证与配套

基于已发布v111 SHA256 f9a2f9f24e80c1a6ee824f100434668c90782a9767a35441cdfe4870373f236f。用户又明确要求“EXE 557 跨界结果包完成后发布”。客户端557的COMMAND接收函数是0x2126160，经0x4454063注册至COMMAND wrapper0x251e1c0；成功分支读四个u16和一个u8结果码。当前统一入站位图屏蔽557，新候选只增加557接收位，全65536编号穷举确认无其它放行变化。这不意味着游戏服务端本分支已实现跨界事务；本仓仅有CmdPacketCargoTransportItem枚举，不能据此宣布跨界玩法验收通过。

塔UI1055资源ui/towerofdespair/reward.xui只有Time控件，无按钮开关，不能臆改PVF。当前NOTI348的0x1c577a0在kind=1分支打开1055并投影物品，0x1c57acc是该分支清理落点。主父核对共享结果操作面板getter0x32b49c0、原生重试状态setter0x359c1f0(+0x784)及refresh0x359ee40：ready=9使option0按钮可用，既有默认未就绪态0使该按钮禁用；其余选图/返城按钮保持原状态。不要混淆卡牌禁用字节+0x79d/+0x79e，也不要套用不同EXE旧地址32bf720。

配套EXE只在塔kind1分支清理处增加一个独立RX段.dsprtry的短跳转，保存/恢复所有整数寄存器与flags，调用既有getter/setter/refresh，再执行被覆盖的mov [ebp-4],10并回原清理。UI管理器/面板为空直接回原路。不改普通或其它塔分支、奖励读写格式、PVF或NPK。候选SHA256 c82cb80374edcbbd8497f20f5517d89439bcb37499954a35294ef3e77d16ab1a。原文件范围差异限定于557位、钩子和必要PE头，新段代码另记；逐字节可还原v111。

离线Unicorn实际执行钩子、getter、setter及retry refresh，覆盖旧ready9可用、新塔0禁用、空manager/空panel，核对栈、寄存器、flags和SEH清理状态。世界/队伍/tooltip/绘制依赖用隔离stub，这是原生片段验证，不能替代完整游戏原生验收。外部证据E:/90/repair-round15-20261016/client/native-receipt.json、stub.asm.txt和native/；完整EXE不进入服务端仓库。

## 验证与验收

服务端回归覆盖真实op72 ACK+城镇序列、ACK+15/36/27选图、无普通卡、retry/缺统计/缺348/未持久化拒绝及op24失败续发。完整list/domain/bridge/control/vet、双平台构建、真PVF塔奖励/层选择/保留功能和BAT preflight需以本轮最终回执为准。

部署后完全退出游戏，启动器preview更新新EXE再登录。验收新通关物品、retry灰且不可点、town返回、另一局selector可用与下一层/重登持久化。历史空奖励不自动补发；未擅改test99资产。MR保持开放由用户验收后合并。
