# v139 主场景聊天 Enter 去重候选

精确基线为已发布 v138，输入 EXE SHA256
`9363574786928ec58ab6f82be0d40847a814d6e4ef42753a1a54750134a09d9c`。
输出 `5499a71294e388d3efc6690a9e6ddaf31673d74aff5befd3de3c8fa6c9bc8b92`，
文件均为 111812608 字节。

## 现象与范围

只读状态记录确认普通 Return 产生 DI 有效按下边沿，聊天窗随后短暂显示并立即隐藏；
Shift+Return 的聊天窗能保持显示。输入、事件、聊天启用标志正常，观察期间 Enter
guard widget 不可见。记录不含聊天文本，也未修改进程。

原生通用按键发布器 `0x29ba8f0` 既处理文字输入 Return 标志，也轮询 DI 按键并发布
同一 event26/key16。主场景 `0x2a0e0c0` 在 `0x2a0e0f5` 调用该发布器，返回地址
为 `0x2a0e0fa`。后续主场景更新仍会读取文字框 Return 标志并执行提交/关闭。
Shift 会屏蔽普通 DI 边沿路径；这与观测差异相符，完整时序因果仍待实机候选验收。

本候选在原发布器仅 key16 会经过的 `0x29ba981` 分支增加调用者判定：
仅返回地址等于 `0x2a0e0fa` 时跳到下一键；其它调用者执行原 guard 指令和后续代码。
原生文字输入 Return 路径完整保留，不写全局按键状态、不强制显示窗口、不重置用户键位。
主场景以外界面及其它按键的发布决策保持原样。

复用 `.clsgate` RX 节偏移512的24字节 helper，原448字节接收适配器原样保留；
VirtualSize 448→536，容量4096不变。允许变更范围只有6字节跳转、24字节helper、
节VirtualSize与PE checksum。无服务端、PVF、资源或协议准入修改。

## 验证与验收

`test_patch_chat_enter_v139.py` 执行两个完整原生发布器及真实 DI 边沿谓词，
覆盖136个按键、8种修饰键组合、文字Return标志、主场景/其它调用者、guard可见性，
并检查按键已按住、输入禁用、单键抑制。共17432次原生执行，仅目标普通Enter回退决策变化。
栈、callee-saved寄存器、全局输入skip表、文件范围、独立ImageHlp checksum与重复应用保护通过。

通用更新、弱引用锁定和事件delegate为明确ABI fixture；自动化不代表完整窗口、IME或发送效果。
先部署预览供用户验证：普通Enter打开并保持、输入文字后Enter发送、装备链接发送、
Shift+Enter与Esc、城镇及副本，以及其它界面Enter。通过后再上传GitGud Releases。

## 发布验收记录

用户已对预览v139确认“已验收正常”，覆盖普通Enter打开并保持、纯文字与装备链接Enter发送、
Shift+Enter及Esc。GitGud Release `client-v139-chat-enter-20261019` 已发布同一EXE，
公开下载ZIP及内嵌EXE摘要均已读回核验。此记录不扩展为所有界面或输入法的实机通过声明。
专项测试支持 `DNF_CHAT_INPUT_EXE`、`DNF_CHAT_CANDIDATE_EXE` 和 `DNF_CHAT_TEST_RECEIPT`
指定隔离路径，在当前公开主线净补丁上重新完成同一17432次矩阵。
