# 称号点击请求的返回地址修复

当前发布客户端的称号扩展把 CALL 地址当成保存的返回地址。实际 x86 CALL 压栈的是下一条指令，因此五条称号路径都漏掉了手动请求标记，服务端仍可能把请求当成穿戴状态复述。

`scripts/client/title-request/repair_published.py`严格绑定完整v173摘要，只修正五个比较立即数（CALL+5）和PE校验和，保留目的槽13、数量1及已定位父调用者限制。原MR中的默认技能指令、他人面板刷新继续保留。

`analyze_native.py --published-v173`生成当前文件的全部直接调用者和静态父路径证据；`verify_candidate.py --published-v173 --evidence <static.json>`执行真实CALL/生产者序言及标记判断，包含原路径、错误槽/数量/父返回和所有其他直接调用者，共48项通过。构造器和继续执行地址使用ABI桩；这不是完整GUI验收。

服务端`TestCurrentUIPlayerQuickTitleCyclesCommitWithoutOtherEquipment`使用真实PVF称号、实际桥接入口和仓储事务，连续三次卸下/穿上，无需先操作其他装备；同时保留原护甲和时装回归。

验收：刚登录、快速连点、换图后分别穿脱称号，称号及属性立即对应真实库存；不依赖其他部位操作。原MR中已验收的三段斩及他人面板功能需保留。最终客户端须从固定干净提交构建并核对玩家实际更新源，游戏验收另记。
