# 永久灵跃武器相对动作目录修复

永久副本放在 equipment/private_seria 后，装备动作表使用的相对 ANI 路径不再指向原模板动作目录，导致模型缺失和依赖武器动作攻击框的普通攻击失效。光剑模板401040005所在 beamsword 目录包含 beamswd/Stay.ani、Attack1/2/3.ani、DashAttack.ani、JumpAttack.ani。

本次资源修复计划见 seria-weapon-relocations-20261029.json。对每项把 old_path 的原始定义复制到 new_path，保持原文件，再将 equipment/equipment.lst 中相同 ID 的引用改为 new_path（去除 equipment/ 前缀）。必须原样复制 raw，不从格式化文本重建定义。37个ID保持，装备实例、强化、品质、成长属性和永久规则均不变。不同武器使用各自模板目录，不统一移动到光剑目录。

写入时固定输入摘要，拒绝目标已存在或旧引用不同；追加阶段验证全部既有条目未变，引用更新阶段仅允许37个引用变化。完成后验证37个新定义与旧定义逐字节相同、现有列表编号集合保持，以及光剑6动作文件与攻击框。源资源及累计PVF由运营方独立分发，不包含于源码仓库。

回归环境变量 DNFBRIDGE_SERIA_WEAPON_PVF 指向已修复的独立PVF；运行 TestRealPVFPermanentSeriaWeaponAnimationRoots。主线修复允许已达相同觉醒分支的角色归档匹配的史诗觉醒节点，不重复转职或发奖。真实任务回归覆盖西海岸3427、根特3515、克洛诺斯3779，以及玩家已选择3868阵营后魔界营地3944和中央公园4840。未回答选择保持等待玩家选择。

离线资源、领域及完整累计服务验证已通过；原生模型、普通攻击命中、通行操作由玩家游戏复测单独记录。
