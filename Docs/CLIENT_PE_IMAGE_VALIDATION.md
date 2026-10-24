# 客户端 PE 地址空洞与 Windows 错误 193

登录器在资源、频道和账号验证成功后，创建游戏进程返回 `ERROR_BAD_EXE_FORMAT (193)`。V158 的 Windows 映像映射通过；V159 和 V162 的正式 EXE 在同一机器返回 193。

V159 新增 `.xscene` 的 RVA 为 `0x6ac0000`，前一段 `.imeup` 对齐后的末尾仅为 `0x6ab3000`，中间缺少 `0xd000` 字节的虚拟覆盖。后续 V160–V162 继承了该布局。文件下载摘要正确并不能证明 Windows 可以加载它。

## 最小修复

`scripts/client/pe-image-gap/build_candidate.py` 只接受冻结 V162：

`04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad`

将 `.imeup.VirtualSize` 从 `0x6b` 扩展为 `0xe000`，使用 PE 的虚拟零填充覆盖到 `.xscene` 起点，并重算 CheckSum。原始文件大小、全部44段的原始内容、入口、RVA、导入、权限和已有补丁均保持；不移动任何代码或文件内容。仅两个头字段允许改变。

```text
python scripts/client/pe-image-gap/build_candidate.py --input <v162-DNF.exe> --output-dir <new-directory> --verify-windows
```

Windows 校验使用 `CreateFileMappingW(PAGE_READONLY | SEC_IMAGE_NO_EXECUTE)`，验证原件193、候选成功；不启动游戏或执行入口。构建回执保存输入、输出、精确字段差异及逐段摘要。候选的正常游戏登录另行实测。

测试使用环境变量 `DNF_PE_GAP_V162` 指向私有真实 EXE，再执行：

```text
python -m unittest discover -s scripts/client/pe-image-gap -p "test_*.py" -v
```

## 后续构建门槛

追加 PE 段时，新 RVA 应等于前段 `align(RVA + max(VirtualSize, SizeOfRawData), SectionAlignment)`，不能任意向更大的地址取整而留下未覆盖区域。需要保留固定 RVA 时，应明确安排虚拟零填充，并验证重叠、原始段边界和 SizeOfImage。

新候选发布前同时检查摘要、全部既有段保持、布局连续及 Windows 不执行映像映射；归档可读和原生模拟测试不能替代操作系统加载校验。发布仍遵循 Release、玩家签名清单及实际下载读回顺序。
