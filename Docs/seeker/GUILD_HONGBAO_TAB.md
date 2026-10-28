# 公会红包页签入口与栈清理

累计 V187 的红包页签回调仍按本地 `IsExist(0x94A)` 决定启用状态。服务端红包交易链已经实现，客户端入口需要配套启用。原调用以 `ret 4` 清理此前压入的参数，直接替换为 `mov al,1` 加 NOP 会导致尾声恢复 EDI/ESI 错位。

`scripts/client/patch_guild_hongbao_tab_v187.py` 只接受精确 V187 输入，将该调用替换为同长的 `mov al,1; add esp,4`，仅另行重算 PE checksum。其余字节、节表、来源通知和既有累计修复保持。工具只创建新文件，拒绝未知输入、重复修补和覆盖已有输出。

```text
python scripts/client/patch_guild_hongbao_tab_v187.py --input <V187.exe> --output <new-candidate.exe>
```

验证入口为 `scripts/client/test_guild_hongbao_tab_v187.py`，通过环境变量 `DNF_HONGBAO_TAB_CLIENT` 指向原始 V187。需要 Unicorn；回调、控件查找、智能指针复制和引用计数使用原生机器码，全局访问器、控件效果、后续刷新和 CRT 分配释放使用明确适配器。覆盖旧 NOP 补丁的寄存器恢复失败、启用/缺控件路径、重复切换、最后引用释放、接收门矩阵、精确差异、Windows checksum 和只读映像映射。

本修复不改变服务端权限、钱包、库存或结算规则。正式发布以最新累计 EXE 为输入，并核对玩家实际通道。游戏内验收：打开/关闭和重复切换页签、四档购买与实际扣费、分份发放、双方领取、重复领取拒绝、历史显示及重登状态。自动验证与玩家实测分别记录。
