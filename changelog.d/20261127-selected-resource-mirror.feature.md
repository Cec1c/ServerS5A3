---
category: 功能修复
---

# 固定资源 Release 的 GitGud 镜像入口

## 变更

增加仅手动触发的 2026-11-27 选定资源镜像工作流。目标固定为公开 GitGud 项目 51684、标签 `selected-resources-20261127`、公开源提交 `94026b51`，以及 GitHub Release 中的 ZIP、manifest 和 SHA256SUMS 三个附件。

辅助程序先验证固定项目、GitHub Release ID 459806188、源提交、本地附件大小与 SHA256，再幂等创建缺失的 GitGud 标签、generic package 和 Release。每次写操作前先持久化安全的状态回执；网络中断后以 `unknown` 和最后操作标识保留现场，由人工检查后再幂等重跑，不自动重试写操作。已有标签、包或 Release 内容不一致时停止写入；上传后匿名完整下载并复核摘要。流程不写 GitGud master，不调用源码镜像，也不包含 EXE、PVF、玩家通道、服务端部署或公告操作。

## 验证

离线单元测试覆盖计划模式的零写入、固定 payload、标签与三项包创建、Release 创建、重复执行幂等、标签/包/Release 冲突拒绝，以及工作流只读权限和现有项目变量/密钥引用。凭据不写入输出、回执或仓库。
