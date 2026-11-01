# 忏悔之雷目标查找的空场景修复

## 问题与基线

被动对象 26119 的消息 29 回调处理子命令 0/1 时，取得所属场景后直接查询目标。场景已脱离或所属对象缺失时，场景 getter 可以返回 NULL；目标 finder 仍读取场景的对象数组，在 VA `0x0287A022` 读取 `0x10` 并崩溃。队友接收技能消息也会进入该回调。

对应回调为 `0x00D39420`，两处查询入口 `0x00D395F0` / `0x00D396C5`；目标 finder 为 `0x0287A000`，返回后的类型转换原本就支持 NULL，回调也已有空目标退出分支。

工具只接受两份完整摘要与长度匹配的累计输入：

- `534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24`，112541696 字节。
- `6091f9ac92e588643226a8f61d313cb798b308d530bf3013cab0bda1d0cd0ece`，112545792 字节，累计第七翼动修复。

## 修复范围

`scripts/client/patch_cleansing_thunder_null.py` 在两处查询加入场景空值检查。非空时保留 EFLAGS 与原调用；空值返回 NULL，并按原 finder 的约定清理 12 字节参数，随后走原类型转换和回调退出。补丁使用独立只读可执行节 `.ctnull`，不修改全局 finder、对象生命周期、PVF 或服务端协议。

输入输出必须为不同路径，输出文件须不存在。工具核对摘要、代码指纹、节表空位、对齐、文件末尾和实际变更范围，重算 PE checksum；未知或重复打补丁的输入拒绝。已有通知及累计节逐字节保留。

## 验证

`scripts/client/test_cleansing_thunder_null.py` 使用 Unicorn 执行完整回调、实际 owner/scene getter、报文整数读取、目标 finder 和 NULL 类型转换及 SEH 恢复。容器查找、全局上下文、报文绑定、非空目标类型转换与效果构造使用显式适配器。用于 SEH 的零页在被测 finder 前取消映射，原版必须真实触发 `0x0287A022 → 0x10`，不能用映射零页掩盖故障。

每份输入 32 组：两个子命令、三种对齐、无所属对象/无场景/空场景/目标不匹配/有效目标，以及消息或对象类型不匹配。原版 12 组精确崩溃，候选返回；其余正常路径寄存器、栈和效果与原版一致。另验证 3 类错误输入拒绝。两输入合计 64 组通过。累计候选通过 Windows 无执行映像映射、PE checksum 和通知 CLI 黑盒 verify。

输出候选与源码验证完成后，按现有累计发布流程交付。发布资产、玩家通道更新与游戏内验收分别记录。实机仍需覆盖释放者与队友、技能期间换房/离图、正常目标效果及连续施放；本回归不等于完整游戏验收。

复现命令：

```text
python scripts/client/test_cleansing_thunder_null.py --input <verified-exe> --report <private-report.json>
python scripts/client/patch_cleansing_thunder_null.py --input <verified-exe> --output <new-candidate.exe>
```
