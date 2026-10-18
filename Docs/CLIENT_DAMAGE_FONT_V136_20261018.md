# v136 伤害字体列表接收准入候选

本候选基于公开提交 `c5534848f1d02425b21d22daa47dfa4c4d9cad74` 的 v135 源码树独立生成。只针对已核实的 v135 收包缺口追加 class0/1239；不改变服务端、资源、正文构造或上行发送。

| 项目 | 值 |
| --- | --- |
| 输入 EXE | v135 `DNF.exe` |
| 输入 SHA256 | `8de790c1974fe74d3b74ec6095b7281ea337759a4adcdfebdd874bf67a94126f` |
| 输入大小 | 111812608 字节 |
| 输出 SHA256 | `22ec56fe82317d0419dfdd6b76e3ee79b287856cf7cccb499177b4dc636e782f` |
| 输出大小 | 111812608 字节 |
| 接收 call | `0x2523a13` |
| 复用适配节 | `.clsgate`，VA `0x6ea1000`，RAW `0x6aa1000` |

## 缺口与改动

实际 v135 收包片段对 class0/1239 返回 0：1239 不在 v135 的显式 class0 列表，共享 bitmap 对应位也为 0，因此服务端真实下发的 44 字节字体列表在进入 native handler 前被丢弃。class1/1288 已由共享 bitmap 放行，保持 1。

v136 只把 1239 追加到现有 `.clsgate` 的 class0 显式列表。class1 列表、class0 1483–1491、class1 658/668/820/868/889、570/377/1266 的排除规则以及未命中尾跳 `0x376e6c0` 的原生 consumer 全部保持不变。没有修改共享 bitmap、类别切换逻辑或其它类别，也没有新增节或改变文件大小。

文件差异仅限：PE checksum 4 字节、`.clsgate` 节 VirtualSize 4 字节（206→217）、适配器正文 217 字节（前 206 字节由旧 adapter 改为新 adapter，随后 11 字节由零变为新增比较与跳转）。其余输入字节逐字节保持一致。

## 验证

`test_patch_damage_font_gate_v136.py` 对精确输入和输出镜像执行实际收包 call 片段，穷举 class 0/1/2/255 的全部 u16 编号，共 **524288** 次（65536 × 4 × 2）。结果：

- 唯一变化为 class0/1239，`0→1`。
- class1/1288 保持 `1→1`。
- class2 与 class255 的所有判定不变。
- class0 1483–1491、class1 51/658/668/820/868/889 保持放行。
- 每次执行均校验返回点为 `0x2523a18`、栈和 EBX/EBP/ESI/EDI 保留。

范围专项核对 RX 属性、节表、SizeOfImage、call 五字节、共享 bitmap、共享 gate 和精确文件差异范围；独立 PE 校验和与 Windows ImageHlp 校验均通过。负输入（错误 SHA/长度）、同路径覆盖和已有输出覆盖均被拒绝，已有输出保持原字节。

## 边界

本候选不包含真实游戏启动、发布、部署、玩家通道或玩家数据操作。native reader 的正文布局审计已确认与服务端 `buildCurrentDamageFontSkinListBody` 的 `u16 selected + u16 count + {u32 index,u32 expires_at}*N` 一致；本补丁只解决收包准入。完整 UI、native 1239 回放、发布预览和服务端配套由父代理继续。
