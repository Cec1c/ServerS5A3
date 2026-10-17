# 魔界裂缝门票显示与预检候选

对应最新《已知问题收集2》段43：实际依次扣2、4、6个精炼的时空石，但选图始终显示2。当前PVF的316为dungeon/MetroCenter/Hell/Hell.dgn，required item10158964×2，etc/particularhellparty.etc的required item weight为1。来源脚本数据保持。

当前v121的829/830接收门都已实际执行并确认开放。实际get-used函数2969340只有三个直接调用点：158f6bc选图要求、1591c9f次数显示、1fab5c2原生入场预检。选图要求消费方此前未定位，本轮已定位到158f6c1..158f728，并执行原始算术。

原生getter1f5d3e0读取float权重；1441140签名是(float base,int exponent)的整数幂，原生预检调用顺序为(weight,used+1)。选图内联SSE循环也是weight**(used+1)。真实权重1时两处均为2/2/2。旧Go注释/旧报告把它说成(used+1)**weight有误。后端现存公式和用户观察到的扣票序列为2/4/6；本候选使这两处客户端算式与后端一致，不改实际扣票、次数上限、免费票选择或资产事务。

补丁仅接受完整v121 SHA9145c197eaa8fab4747e82b0285880558dacdf69163a8f86024211e2d24065db，添加独立只读可执行.riftfee节。预检只改1fab5d2的call；选图只改已证实的103字节算术span，保留EBX=used、ESI=used+1、原getter、原计数读取、原后续次数限制与渲染调用。其它副本/非particular分支不经过这些位置。

共同helper保持原调用签名(float weight,int usedPlusOne)，通过x87 log2/exp2计算正整数base的浮点幂，回到float32再供原乘法使用；维持调用栈、非易失寄存器、x87栈深度。实际当前cutoff最多used2，合法weight1，候选两处输出2/4/6。额外控制矩阵weight0/0.5/1/1.5/2/3用于反转参数和FPU边界验证，不声称生产存在这些权重。

验证：同一原生行为用例对旧v121红为[2,2,2]!=[2,4,6]；新候选4项全绿、无skip，覆盖两处原始调用链、候选helper、栈/寄存器/FPU、精确字节写集、65536编号双镜像131072次实际接收门执行、错误输入及拒绝覆盖。算术/条件无hook，used/count/loadedweight为隔离夹具，不是完整UI/玩家验收；full原生PVF parser与UI manager没有运行。

输入与候选SHA：

- 输入：9145c197eaa8fab4747e82b0285880558dacdf69163a8f86024211e2d24065db
- 候选：15a24675cea77f526dd8b93f6ec781122e6f148ffd42c8aff9fb0b99267efce4

命令：

```text
python scripts/client/patch_rift_ticket_v121.py --input <v121 DNF.exe> --output <new DNF.exe>
DNF_RIFT_INPUT_EXE=<v121 path> DNF_RIFT_CANDIDATE_EXE=<new path> python scripts/client/test_patch_rift_ticket_v121.py
```

EXE候选另行作为preview发布；本仓不提交二进制。PVF、其他客户端补丁、接收门和后端运行行为保持。发布、完整检查、线上MR状态和实机验收必须查看外部Round40回执，不从本说明推定已部署或已验收。夏日券只完成物品/类型/分组定位，未修复。梅娅仍待原现场复测。
