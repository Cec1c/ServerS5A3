# v130 按类别开放接收编号

用户要求在已发布v129基础上追加：class0 1483–1491，class1 658、668、820、889。输入固定为v129 SHA256 `37b230cd6f047ccc001501cbc02c3709d0c6ee5e0059f66f2d656023045040fa`，111808512字节；输出 SHA256 `6e1dce9076bcd5988bf3454184436c47eec256fa6247f80dd32f5cce93625ccc`，111812608字节。

## 实现

旧接收门0x6e83610的位图只按编号、不区分类别，且最大编号0x5c2/1474。为精确满足类别范围，不扩大共享位图或上限。在当前接收函数0x25239a0已核证的call 0x2523a13处，改调新增RX节`.clsgate`里的72字节适配器。EDI为原报头，`[EDI]`为class，原push参数为u16编号；适配器只在class0且1483..1491或class1且指定四号时返回1，其余尾跳原0x376e6c0继续原判定。保留原类分发、报文解析、处理器及调用栈约定。

新增节VA 0x6ea1000，raw offset111808512，raw size4096，权限0x60000020（RX，无写权限）。原文件改动范围仅call五字节、NumberOfSections、SizeOfCode、SizeOfImage、40字节新节表及PE checksum。原共享门与位图、v129十一位、v128自动回顾、475通信与交易补丁保持。

## 验证

`test_patch_class_gates_v129.py`执行两镜像实际收包call片段0x2523a0c..0x2523a18；对class0/1/2/255逐个穷举65536编号，共524288次调用，要求class0只新增1483..1491、class1只新增658/668/820/889、其它两个类零变化。逐次核对栈平衡与callee-saved寄存器。另核对完整原字节保留范围、RX节权限、独立PE校验和、输入摘要/重复应用/覆盖保护。最终结果在发布包evidence。

生成器`patch_class_gates_v129.py`依赖同目录`patch_receivers_v128.py`中的PE工具。测试需Unicorn及DNF_CLASS_INPUT_EXE/DNF_CLASS_CANDIDATE_EXE，可通过DNF_CLASS_GATE_RECEIPT输出原生证据。

本次仅新增接收准入，不补造游戏功能或处理器。1483/1484/1490未从既有常见注册字节模式唯一定位，不据此宣称相关玩法可用。实际分发消费者、网络交互与UI留待实机验收。配套资源完整继承v129/v128的manifest，PVF fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525；不把仓库EXE发布视为玩家通道已更新或服务端已部署。
