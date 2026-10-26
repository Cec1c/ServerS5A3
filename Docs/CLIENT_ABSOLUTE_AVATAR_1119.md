# 梦想装扮合成的class1/1119接收补丁

服务端成功结果使用class1/1119。固定V162已有原生请求写者、结果读者及应答登记，但分类许可链会挡住1119；结果不应通过改服务端编号或借用class0通知发送。本补丁仅在真实接收调用前开放class1/1119，其余分类及编号回到原有分类链。

## 固定输入与复现

输入必须是已完成[!235映像空洞修复](CLIENT_PE_IMAGE_VALIDATION.md)的V162，大小111894528字节：

| 阶段 | SHA256 |
| --- | --- |
| 原V162，尚未修好错误193 | `04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad` |
| 本补丁输入，已修好映像 | `b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902` |
| 仅开放class1/1119的输出 | `ca9c300f8f941476fab61fd7094449920e9bc91d31397d3e3fe3451fe36cc125` |

持有原V162时，先生成映像修复输入，再生成独立接收候选。路径仅为示例，输出必须是新文件：

```text
python scripts/client/pe-image-gap/build_candidate.py --input DNF-v162.exe --output-dir image-fixed --verify-windows
python scripts/client/patch_absolute_avatar_1119.py --input image-fixed/DNF.exe --output absolute-1119/DNF.exe --verify-windows
```

脚本拒绝错误版本、未修复的错误193输入、已修改或已打补丁的输入，也拒绝覆盖源文件或已有输出。当前已含其它本机补丁的EXE不能套用这些偏移；新版本需重新匹配摘要及原生字节契约。

## 精确改动与验证

- 调用点VA `0x2523a13`转到原只读可执行节`.r195rx`内的VA `0x6ead080`；辅助代码33字节，仅比较接收头class与低16位编号。其它请求跳回原入口`0x6ead000`，保持原参数、ECX及非易失寄存器。
- 不改共用位图。原节VirtualSize由`0x76`扩至`0xa1`，保持44个节、文件大小、入口、RVA、原生1119请求/读者/结果应用及登记字节，仅修改调用、辅助代码、该VirtualSize及PE校验和。
- 真实夹具回归对class0、class1、class2、class255各遍历全部65536个编号，对照补丁前后真实分流；另检查实际调用栈和保存寄存器、输入拒绝、修改范围及Windows不执行映像映射。唯一许可变化应为class1/1119，903保持原状态。

`test_patch_absolute_avatar_1119.py`使用环境变量`DNF_ABSOLUTE_1119_INPUT_EXE`提供上述固定输入；真实分流与机器码ABI检查需要`unicorn`，Windows映像测试需要Windows。通过`DNF_ABSOLUTE_1119_GATE_RECEIPT`可保存分类对照结果。未提供夹具、依赖或平台时的跳过不能当作本轮真实映像验证通过。

服务端字段契约见[梦想合成说明](AVATAR_ABSOLUTE_COMPOUND.md)。接收许可不替代固定54字节结果块、事务扣料、属性及结果对象生命周期的核对。原本机组合已有游戏使用确认；此独立1119候选未另行安装或宣称全部GUI验收。

本MR提交补丁源码、测试与契约说明。EXE发行、玩家通道同步、服务端上线及游戏验收按[客户端发布流程](CLIENT_RELEASE_SYNC.md)分别执行，本次不发布或替换客户端。
