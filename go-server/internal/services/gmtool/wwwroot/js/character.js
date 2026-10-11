async function setLevel() {
  if (!currentChar) return;
  const level = parseInt($('#level-input').value, 10);
  try {
    await post(`/api/characters/${currentChar.characterId}/level`, { level });
    toast('等级已设置为 ' + level);
    refreshHeader();
    loadCharacters();
    loadStats();
    loadSpTp();
  } catch (e) {
    toast(e.message, true);
  }
}

async function maxPersonalCargo() {
  if (!currentChar) return;
  try {
    const r = await post(`/api/characters/${currentChar.characterId}/personal-cargo/max`);
    toast(`当前角色仓库满级已设置: ${r.listParam16}`);
  } catch (e) {
    toast(e.message, true);
  }
}

// 特殊装备槽的三条入口共用同一个接口：slots = leftright / earring / all。
// 服务端只改 dnf_characters.ex_equip_slot_stat 的对应位（左右槽 0x01|0x02、
// 耳环 0x10）—— 面板不自己算位。
//
// 任务面（现场「gm工具开启耳环槽后 任务依然存在」）：默认档
// （DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN 未设）服务端会把 PVF 里授权该槽位的任务
// **连同它的 [pre required quest] 前置链**一起标 completed —— 耳环槽 = 2636
// 「穿戴耳环 (2/2)」+ 2635「穿戴耳环 (1/2)」，响应里的 questIds / completedQuestIds /
// createdQuestIds 就是这一档的实况；questChain=false 表示服端被显式回退成
// legacy（只写位）。位已经开过的角色**再点一次**也会走这段同步，所以两个按钮
// 在"已经开启"时不再禁用 —— 残留的外传任务就是靠再点一次收掉的。
async function unlockExtraEquipmentSlots(slotGroup) {
  if (!currentChar) return;
  updateExtraEquipmentSlotButton();
  const group = slotGroup || 'leftright';
  const isEarring = group === 'earring';
  const unlockBtn = $(isEarring ? '#btn-unlock-earring-slot' : '#btn-unlock-equipment-slots');
  if (unlockBtn && unlockBtn.disabled) return;
  const unlockOldText = unlockBtn ? unlockBtn.textContent : '';
  if (unlockBtn) {
    unlockBtn.disabled = true;
    unlockBtn.textContent = '正在开启...';
  }
  try {
    const r = await post(`/api/characters/${currentChar.characterId}/equipment-slots/unlock`, { slots: group });
    // 新行算 createdQuestIds、旧行翻完成算 completedQuestIds，两者都是"这次收掉的
    // 任务"；只数 completed 会让干净角色的提示永远显示 0（行是新建的）。
    const doneCount = (r.completedQuestIds?.length || 0) + (r.createdQuestIds?.length || 0);
    const alreadyDone = r.alreadyDoneQuestIds?.length || 0;
    const label = isEarring ? '耳环槽' : '左右槽';
    const already = (r.alreadyUnlocked || []).length;
    if (already && !(r.unlockedGroups || []).length) {
      toast(doneCount > 0
        ? `${label}已经是开启状态；已完成 ${doneCount} 个相关任务`
        : alreadyDone > 0
          ? `${label}已经是开启状态，相关任务早已完成`
          : `${label}已经是开启状态`);
    } else {
      toast(doneCount > 0 ? `${label}已开启，已完成 ${doneCount} 个相关任务` : `${label}已开启`);
    }
    applyExtraEquipmentState(r.extraEquipmentState);
    refreshHeader();
    loadAllVisibleQuests();
    loadAchieveQuests();
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (unlockBtn) {
      unlockBtn.textContent = unlockOldText;
      updateExtraEquipmentSlotButton();
    }
  }
  return;
}

// 服务端现在直接回逐槽状态（extraEquipmentState），前端不再自己拿旧口径
// （exEquipSlotStat & 3）算 —— 当前客户端的耳环位是 0x10，不是 bit2。
function extraEquipmentSlotState(character) {
  const state = character && character.extraEquipmentState;
  if (state && typeof state === 'object') return state;
  const mask = Number(character?.exEquipSlotStat || 0) & 0x13;
  return {
    mask,
    allMask: 0x13,
    all: mask === 0x13,
    support: (mask & 0x01) !== 0,
    magicStone: (mask & 0x02) !== 0,
    earring: (mask & 0x10) !== 0,
    leftRight: (mask & 0x03) === 0x03,
  };
}

function applyExtraEquipmentState(state) {
  if (!currentChar || !state || typeof state !== 'object') return;
  currentChar.extraEquipmentState = state;
  currentChar.extraEquipmentSlotsUnlocked = state.all === true;
  if (typeof state.mask === 'number') currentChar.exEquipSlotStat = state.mask;
}

async function unlockDungeonPermissions() {
  if (!currentChar) return;
  const btn = $('#btn-unlock-dungeon-permissions');
  const oldText = btn ? btn.textContent : '';
  if (btn) {
    btn.disabled = true;
    btn.textContent = '正在开启...';
  }
  try {
    const r = await post(`/api/characters/${currentChar.characterId}/dungeon-permissions/unlock`);
    toast(`已为当前角色开启 ${r.insertedCount || 0} 个副本的最高难度`);
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = oldText;
    }
  }
}

function updateExtraEquipmentSlotButton() {
  const state = currentChar ? extraEquipmentSlotState(currentChar) : null;
  const levelOk = Boolean(currentChar) && currentChar.level >= 70;
  // "已经开启"时**不禁用**：再点一次是合法的补同步动作（位已开、但旧工具没有把
  // 授权任务链标完成，任务手册里还挂着外传「穿戴耳环 (1/2)」）。
  const leftRightBtn = $('#btn-unlock-equipment-slots');
  if (leftRightBtn) {
    leftRightBtn.disabled = !levelOk;
    leftRightBtn.title = !currentChar
      ? ''
      : currentChar.level < 70
        ? '角色达到 70 级后可开启'
        : state?.leftRight
          ? '左右槽已经开启；再点一次会把授权任务链一并标完成（收掉残留的外传任务）'
          : '';
  }
  const earringBtn = $('#btn-unlock-earring-slot');
  if (earringBtn) {
    earringBtn.disabled = !levelOk;
    earringBtn.title = !currentChar
      ? ''
      : currentChar.level < 70
        ? '角色达到 70 级后可开启'
        : state?.earring
          ? '耳环槽已经开启；再点一次会把授权任务链一并标完成（收掉残留的外传任务）'
          : '';
  }
}

async function deleteCurrentCharacter() {
  if (!currentChar) return;
  const character = currentChar;
  if (!confirmCharacterSwitchedAway(character, '删除')) return;
  const message = `将彻底删除角色 ${character.name} (#${character.characterId})，并删除背包、任务等关联数据。仍在佣兵出战或奖励邮件尚未送达时会拒绝删除。此操作不可恢复。是否继续？`;
  if (!confirm(message)) return;

  const confirmText = prompt('请输入 删除角色 以确认彻底删除当前角色');
  if (confirmText !== '删除角色') {
    if (confirmText != null) toast('确认文本不正确，已取消删除', true);
    return;
  }

  const btn = $('#btn-delete-character');
  const oldText = btn ? btn.textContent : '';
  if (btn) {
    btn.disabled = true;
    btn.textContent = '正在删除...';
  }
  try {
    const r = await post(`/api/characters/${character.characterId}/delete`, { confirmText });
    toast(`已彻底删除角色 ${r.name || character.name} (#${r.characterId})`);
    closeCharacterClonePanel();
    currentChar = null;
    if (typeof updateCharacterMailboxButton === 'function') updateCharacterMailboxButton();
    selectEpoch++;
    $('#detail').classList.add('hidden');
    await loadAccounts();
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = oldText;
    }
  }
}

// 角色改名：服务端 POST /api/characters/{id}/rename（api_character_rename.go，
// 移植上游 A21 GM 工具的 GmService.CharacterRename.cs）。
//
// 这条是"客户端点【修改】改名不生效"的运维兜底：游戏信道 class1/op135 在本服务端
// 与厂商参考实现里都没有 handler，S2C 回包形态未取证（_dist/rename-delete-diagnosis.md
// §2.4/§2.7），所以先把面板这条**不需要任何新协议**的改名交给操作者用。
//
// 契约（逐字段）：newName + expectedName（乐观并发，逐字给出当前名，服务端不做 Trim）
// + characterOffline（上游要求的操作者确认；服务端另外还会探一次真实对局会话）。
// 失败恒 HTTP 200 + success:false，post() 会把它变成异常 → 原样 toast 服务端文案。
async function renameCurrentCharacter() {
  if (!currentChar) return;
  const character = currentChar;
  const newName = ($('#rename-character-name').value || '').trim();
  const state = $('#rename-character-state');
  if (!newName) {
    toast('请先填写新角色名', true);
    return;
  }
  if (newName === character.name) {
    toast('新角色名与当前角色名相同，无需修改');
    return;
  }
  if (!confirmCharacterSwitchedAway(character, '改名')) return;
  if (!confirm(`将把角色 ${character.name} (#${character.characterId}) 改名为 ${newName}。\n\n角色名需为 2–12 字节（最多 6 个中文），且该角色必须不在线。是否继续？`)) return;

  const btn = $('#btn-rename-character');
  const oldText = btn ? btn.textContent : '';
  if (btn) {
    btn.disabled = true;
    btn.textContent = '正在改名...';
  }
  if (state) state.textContent = '';
  try {
    const r = await post(`/api/characters/${character.characterId}/rename`, {
      newName,
      expectedName: character.name,
      characterOffline: true,
    });
    if (r.idempotent) {
      toast(`角色名已经是 ${r.name}（未重复写入）`);
    } else {
      toast(`已改名：${r.previousName} → ${r.name}`);
    }
    // verified=false 只有一种来源：写入已提交但提交后重读没确认到新名。
    // 如实显示，不谎报成功（服务端 api_character_rename.go 的 finishRename）。
    if (state && r.verified === false) {
      state.textContent = '服务端提示：写入已提交，但重读未确认到新名，请刷新列表核对';
    }
    $('#rename-character-name').value = '';
    await loadCharacters();
    selectCharacter(character.characterId);
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = oldText;
    }
  }
}

let characterClonePlan = null;
let cloneNameAvailable = false;
let cloneRequestRunning = false;
const CLONE_OPTIONS_DEFAULT_OFF = new Set([
  'quests',
  'quest',
  'clearedQuests',
  'clearedQuest',
  'titleBook',
  'titlebook',
  'dailyWeekly',
  'dailyWeeklyState',
  'characterState',
  'characterStates',
  'extraCharacterState',
  'otherCharacterState',
  'audit',
  'itemAudit',
  'itemAuditLog',
]);

function confirmCharacterSwitchedAway(character, actionName) {
  return confirm(
    `${actionName}角色前，请先在客户端进入选择角色界面，并切换到其他角色。\n\n`
    + `当前操作角色：${character.name} (#${character.characterId})\n\n`
    + '确认已经切换到其他角色后，点击“确定”继续。'
  );
}

async function openCharacterClonePanel() {
  if (!currentChar) return;
  if (!confirmCharacterSwitchedAway(currentChar, '复制')) return;
  $('#character-clone-panel').classList.remove('hidden');
  $('#clone-character-state').textContent = '正在加载复制设置...';
  $('#clone-character-name').value = `${currentChar.name}_copy`;
  cloneNameAvailable = false;
  $('#clone-name-state').textContent = '';
  try {
    characterClonePlan = await api(`/api/characters/${currentChar.characterId}/clone-plan`);
    renderCharacterClonePlan();
  } catch (e) {
    $('#clone-character-state').textContent = e.message;
    toast(e.message, true);
  }
}

function closeCharacterClonePanel() {
  $('#character-clone-panel').classList.add('hidden');
}

function renderCharacterClonePlan() {
  const select = $('#clone-target-account');
  select.innerHTML = '';
  for (const account of characterClonePlan.accounts || []) {
    const option = document.createElement('option');
    option.value = account.accountId;
    option.textContent = `${account.name} (#${account.accountId}) ${account.characterCount}/${account.slotLimit}`
      + (account.isCurrent ? ' 当前账号' : '')
      + (!account.canAcceptCharacter ? ' 已满' : '');
    option.disabled = !account.canAcceptCharacter;
    select.appendChild(option);
  }
  select.value = String(characterClonePlan.sourceAccountId);
  updateCloneAccountLimit();

  const box = $('#clone-option-list');
  box.innerHTML = '';
  for (const option of characterClonePlan.options || []) {
    const label = document.createElement('label');
    const defaultChecked = option.defaultChecked && !isCloneOptionDefaultOff(option);
    label.innerHTML = `<input type="checkbox" value="${escapeHtml(option.key)}" ${defaultChecked ? 'checked' : ''}> ${escapeHtml(option.label)}`;
    const input = label.querySelector('input');
    if (option.key === 'basic') {
      input.checked = true;
      input.disabled = true;
    }
    box.appendChild(label);
  }
  updateCloneButtonState();
  if (!cloneRequestRunning)
    $('#clone-character-state').textContent = '';
}

function updateCloneButtonState() {
  const btn = $('#btn-run-character-clone');
  if (!btn) return;
  btn.disabled = cloneRequestRunning;
}

function isCloneOptionDefaultOff(option) {
  const key = String(option?.key || '').replace(/[-_\s]/g, '').toLowerCase();
  const label = String(option?.label || '').toLowerCase();
  if ([...CLONE_OPTIONS_DEFAULT_OFF].some((item) => key === item.replace(/[-_\s]/g, '').toLowerCase()))
    return true;
  return label.includes('任务')
    || label.includes('完成记录')
    || label.includes('称号簿')
    || label.includes('每日')
    || label.includes('周常')
    || label.includes('角色状态')
    || label.includes('审计');
}

function activateMainTab(tabName) {
  const tab = document.querySelector(`.tab[data-tab="${tabName}"]`);
  if (!tab) return;
  document.querySelectorAll('.tab[data-tab]').forEach((t) => t.classList.remove('active'));
  document.querySelectorAll('.tab-page').forEach((p) => p.classList.add('hidden'));
  tab.classList.add('active');
  $('#tab-' + tabName).classList.remove('hidden');
}

function activateAccountTab(tabName) {
  const tab = document.querySelector(`.acc-tab[data-acc-tab="${tabName}"]`);
  if (!tab) return;
  document.querySelectorAll('.acc-tab').forEach((t) => t.classList.remove('active'));
  document.querySelectorAll('.acc-tab-page').forEach((p) => p.classList.add('hidden'));
  tab.classList.add('active');
  $('#acc-tab-' + tabName).classList.remove('hidden');
}

async function jumpToCharacterCurrency() {
  if (!currentChar) return toast('请先选择角色', true);
  activateMainTab('inventory');
  activeCategory = '货币';
  invPage = 0;
  clearInventoryConfiguration();
  if (inventoryItems.length === 0)
    await loadItems();
  else {
    renderCategoryNav();
    renderItemTable();
  }
}

async function jumpToAccountCurrency() {
  const accountId = parseInt($('#account-select').value, 10);
  if (!accountId) return toast('请先选择账号', true);
  await showAccountPanel();
  activateAccountTab('currency');
}

function updateCloneAccountLimit() {
  const accountId = parseInt($('#clone-target-account').value, 10);
  const account = (characterClonePlan?.accounts || []).find((a) => a.accountId === accountId);
  $('#clone-account-limit').textContent = account
    ? `角色 ${account.characterCount}/${account.slotLimit}${account.isCurrent ? '，当前账号' : ''}`
    : '';
}

async function checkCloneCharacterName() {
  cloneNameAvailable = false;
  const name = $('#clone-character-name').value.trim();
  $('#clone-name-state').textContent = '正在检查...';
  try {
    const result = await api(`/api/characters/name-available?name=${encodeURIComponent(name)}`);
    cloneNameAvailable = result.available === true;
    $('#clone-name-state').textContent = cloneNameAvailable ? '角色名可用' : (result.reason || '角色名不可用');
    if (!cloneNameAvailable) toast($('#clone-name-state').textContent, true);
  } catch (e) {
    $('#clone-name-state').textContent = e.message;
    toast(e.message, true);
  }
  updateCloneButtonState();
}

async function runCharacterClone() {
  if (!currentChar || !characterClonePlan) return;
  if (!cloneNameAvailable) return toast('请先检查角色名，并确认角色名可用', true);

  const targetAccountId = parseInt($('#clone-target-account').value, 10);
  const newName = $('#clone-character-name').value.trim();
  const options = [...document.querySelectorAll('#clone-option-list input[type="checkbox"]:checked')]
    .map((input) => input.value);
  if (!confirm(`复制角色 ${currentChar.name} 到账号 #${targetAccountId}，新角色名 ${newName}？`))
    return;

  const btn = $('#btn-run-character-clone');
  cloneRequestRunning = true;
  updateCloneButtonState();
  $('#clone-character-state').textContent = '正在复制...';
  try {
    const result = await post(`/api/characters/${currentChar.characterId}/clone`, { targetAccountId, newName, options });
    $('#clone-character-state').textContent = `复制完成，新角色 #${result.characterId}；请在客户端正常重新选择角色或重新登录后进入新角色`;
    toast(`角色已复制为 ${result.name} (#${result.characterId})，请在客户端正常刷新角色列表`);
    cloneNameAvailable = false;
    await loadAccounts();
    $('#account-select').value = String(result.targetAccountId);
    await loadCharacters(result.targetAccountId);
  } catch (e) {
    $('#clone-character-state').textContent = e.message;
    toast(e.message, true);
  } finally {
    cloneRequestRunning = false;
    updateCloneButtonState();
  }
}

function openCloneAccountPanel() {
  $('#clone-account-panel').classList.remove('hidden');
  $('#clone-account-state').textContent = '';
}

function closeCloneAccountPanel() {
  $('#clone-account-panel').classList.add('hidden');
}

function togglePasswordInput(inputSelector, buttonSelector) {
  const input = $(inputSelector);
  const button = $(buttonSelector);
  const visible = input.type === 'text';
  input.type = visible ? 'password' : 'text';
  button.textContent = visible ? '显示' : '隐藏';
}

async function createCloneAccount(e) {
  e.preventDefault();
  $('#clone-account-state').textContent = '正在创建...';
  try {
    const result = await post('/api/accounts/create-for-clone', {
      accountName: $('#clone-account-name').value.trim(),
      password: $('#clone-account-password').value,
      confirmPassword: $('#clone-account-password-confirm').value,
    });
    toast(`账号 ${result.name} 已创建`);
    closeCloneAccountPanel();
    await loadAccounts();
    if (currentChar) {
      characterClonePlan = await api(`/api/characters/${currentChar.characterId}/clone-plan`);
      renderCharacterClonePlan();
      $('#clone-target-account').value = String(result.accountId);
      updateCloneAccountLimit();
    }
  } catch (err) {
    $('#clone-account-state').textContent = err.message;
    toast(err.message, true);
  }
}

let growOptions = null;

// Default loading and job changes can overlap for the same character.
// Only the newest request may change the selection or report an error.
let growOptionsRequestSeq = 0;

function growOptionsResponseIsCurrent(epoch, characterId, seq) {
  return seq === growOptionsRequestSeq && epoch === selectEpoch &&
    !!currentChar && currentChar.characterId === characterId;
}

async function loadGrowOptions() {
  if (!currentChar) return;
  const epoch = selectEpoch;
  const characterId = currentChar.characterId;
  const seq = ++growOptionsRequestSeq;
  try {
    const fetched = await api(`/api/characters/${characterId}/growoptions`);
    if (!growOptionsResponseIsCurrent(epoch, characterId, seq)) return;
    renderGrowOptions(fetched);
  } catch (e) {
    if (!growOptionsResponseIsCurrent(epoch, characterId, seq)) return;
    toast(e.message, true);
  }
}

async function loadGrowOptionsForJob() {
  if (!currentChar) return;
  const job = parseInt($('#grow-job').value, 10);
  const epoch = selectEpoch;
  const characterId = currentChar.characterId;
  const seq = ++growOptionsRequestSeq;
  try {
    const fetched = await api(`/api/characters/${characterId}/growoptions?job=${encodeURIComponent(job)}`);
    if (!growOptionsResponseIsCurrent(epoch, characterId, seq)) return;
    renderGrowOptions(fetched);
  } catch (e) {
    if (!growOptionsResponseIsCurrent(epoch, characterId, seq)) return;
    toast(e.message, true);
  }
}

function renderGrowOptions(fetched) {
  growOptions = fetched;

  const jobSel = $('#grow-job');
  if (jobSel) {
    jobSel.innerHTML = '';
    const jobs = Array.isArray(growOptions.jobs) && growOptions.jobs.length
      ? growOptions.jobs
      : [{ value: growOptions.job, label: growOptions.options.baseName || `job ${growOptions.job}` }];
    for (const job of jobs) {
      const option = document.createElement('option');
      option.value = job.value;
      option.textContent = jobLabelWithGender(job.value, job.label || `job ${job.value}`);
      jobSel.appendChild(option);
    }
    jobSel.value = String(growOptions.job);
  }

  const firstSel = $('#grow-first');
  firstSel.innerHTML = '';
  const growTypes = growOptions.options.growTypes || [];
  const hasBaseOption = growTypes.some((g) => Number(g.value) === 0);
  if (!hasBaseOption) {
    const baseOption = document.createElement('option');
    baseOption.value = '0';
    baseOption.textContent = growOptions.options.baseName || '未转职';
    firstSel.appendChild(baseOption);
  }
  for (const g of growTypes) {
    const option = document.createElement('option');
    option.value = g.value;
    option.textContent = g.label;
    option.disabled = Number(g.value) > 0 && currentChar && currentChar.level < 15;
    firstSel.appendChild(option);
  }
  firstSel.value = String(growOptions.first);
  renderSecondOptions();
  $('#grow-second').value = String(growOptions.second);
}

function renderSecondOptions() {
  const first = parseInt($('#grow-first').value, 10);
  const secondSel = $('#grow-second');
  secondSel.innerHTML = '';
  const baseOption = document.createElement('option');
  baseOption.value = '0';
  baseOption.textContent = '未觉醒';
  secondSel.appendChild(baseOption);
  const grow = growOptions?.options.growTypes.find((g) => g.value === first);
  if (grow) {
    // 优先用带"第几次觉醒"标签的 awakeningOptions（后端同序提供）；
    // 老响应只有 awakenings（纯名字）时退回按序号推断 stage。
    const labelled = Array.isArray(grow.awakeningOptions) ? grow.awakeningOptions : null;
    const names = Array.isArray(grow.awakenings) ? grow.awakenings : [];
    const entries = labelled && labelled.length
      ? labelled.map((o) => ({ stage: Number(o.stage) || 0, label: String(o.label || o.name || '') }))
      : names.map((name, i) => ({ stage: i + 1, label: `${i === 0 ? '一次觉醒' : (i === 1 ? '二次觉醒' : '三次觉醒')} · ${name}` }));
    entries.forEach((entry, i) => {
      const value = entry.stage > 0 ? entry.stage : i + 1;
      const option = document.createElement('option');
      option.value = value;
      option.textContent = entry.label;
      option.disabled = currentChar && ((value === 1 && currentChar.level < 50) || (value >= 2 && currentChar.level < 75));
      secondSel.appendChild(option);
    });
  }
}

function jobLabelWithGender(job, label) {
  const gender = jobGenderSuffix(job);
  return gender ? `${label}（${gender}）` : label;
}

function jobGenderSuffix(job) {
  // The shipped PVF uses the same base name for both gunner jobs.
  if (Number(job) === 2) return '男';
  if (Number(job) === 5) return '女';
  return '';
}

async function setGrowType() {
  if (!currentChar) return;
  const job = parseInt($('#grow-job').value, 10);
  const first = parseInt($('#grow-first').value, 10);
  const second = parseInt($('#grow-second').value, 10);
  try {
    await post(`/api/characters/${currentChar.characterId}/growtype`, { job, first, second });
    toast('职业/转职/觉醒已覆写，战斗属性和技能点已重算');
    refreshHeader();
    loadCharacters();
    loadStats();
    loadSpTp();
  } catch (e) {
    toast(e.message, true);
  }
}

let inventoryLimitIs999 = false;
let goldLimitStatus = null;

function updateInventoryLimitButtons(is999, isSaving = false) {
  const setMaxButton = $('#btn-inventory-limit-999');
  const restoreButton = $('#btn-inventory-limit-restore');
  setMaxButton.disabled = isSaving || is999;
  restoreButton.disabled = isSaving || !is999;
}

async function loadStats() {
  if (!currentChar) return;
  updateInventoryLimitButtons(inventoryLimitIs999, true);
  updateGoldLimitButton(null, true);
  const epoch = selectEpoch;
  try {
    const data = await api(`/api/characters/${currentChar.characterId}/stats`);
    if (epoch !== selectEpoch) return;
    $('#stats-meta').textContent = `Lv.${data.level} job=${data.job} growType=${data.growType}`;
    inventoryLimitIs999 = data.inventoryLimitOverridden;
    updateInventoryLimitButtons(inventoryLimitIs999);
    $('#inventory-limit-state').textContent = data.inventoryLimitOverridden
      ? '当前已设为 999。服务端重算属性后会恢复正常值。'
      : '当前使用职业/等级的正常负重。';
    const tbody = $('#stats-table tbody');
    tbody.innerHTML = '';
    const cell = (s) => s
      ? `<td${s.zeroBlock ? ' class="dim"' : ''}>${s.label}</td><td${s.zeroBlock ? ' class="dim"' : ''}>${Number(s.value).toLocaleString()}</td>`
      : '<td></td><td></td>';
    for (let i = 0; i < data.stats.length; i += 2) {
      const tr = document.createElement('tr');
      tr.innerHTML = cell(data.stats[i]) + cell(data.stats[i + 1]);
      tbody.appendChild(tr);
    }
  } catch (e) {
    toast(e.message, true);
  }
}

function updateGoldLimitButton(status, isSaving = false) {
  const button = $('#btn-gold-limit-max');
  button.disabled = isSaving || !status || !status.canSetMaximum;
}

async function loadGoldLimit() {
  if (!currentChar) return;
  updateGoldLimitButton(null, true);
  const epoch = selectEpoch;
  try {
    const data = await api(`/api/characters/${currentChar.characterId}/gold-limit`);
    if (epoch !== selectEpoch) return;
    goldLimitStatus = data;
    updateGoldLimitButton(data);
    const suffix = data.isMaximum
      ? '（已满级）'
      : data.characterLevel < data.minimumUpgradeCharacterLevel
        ? `（需 ${data.minimumUpgradeCharacterLevel} 级）`
        : '';
    $('#gold-limit-state').textContent = `当前金币上限 ${Number(data.goldCarryLimit).toLocaleString()}${suffix}`;
  } catch (e) {
    goldLimitStatus = null;
    $('#gold-limit-state').textContent = '金币上限读取失败';
    toast(e.message, true);
  }
}

async function setMaximumGoldLimit() {
  if (!currentChar || !goldLimitStatus?.canSetMaximum) return;
  updateGoldLimitButton(goldLimitStatus, true);
  try {
    const data = await post(`/api/characters/${currentChar.characterId}/gold-limit/max`);
    toast(`金币上限已升至 ${Number(data.goldCarryLimit).toLocaleString()}`);
    await loadGoldLimit();
  } catch (e) {
    toast(e.message, true);
    updateGoldLimitButton(goldLimitStatus);
  }
}

async function setInventoryLimitTo999() {
  if (!currentChar) return;
  updateInventoryLimitButtons(inventoryLimitIs999, true);
  try {
    await post(`/api/characters/${currentChar.characterId}/inventory-limit/max`);
    toast('负重上限已设为 999');
    await loadStats();
  } catch (e) {
    toast(e.message, true);
    updateInventoryLimitButtons(inventoryLimitIs999);
  }
}

async function restoreNormalInventoryLimit() {
  if (!currentChar) return;
  updateInventoryLimitButtons(inventoryLimitIs999, true);
  try {
    await post(`/api/characters/${currentChar.characterId}/inventory-limit/restore`);
    toast('已恢复职业/等级对应的正常负重');
    await loadStats();
  } catch (e) {
    toast(e.message, true);
    updateInventoryLimitButtons(inventoryLimitIs999);
  }
}

async function loadSpTp() {
  if (!currentChar) return;
  const epoch = selectEpoch;
  try {
    const d = await api(`/api/characters/${currentChar.characterId}/sptp`);
    if (epoch !== selectEpoch) return;
    $('#sptp-view').innerHTML =
      `<b>剩余 SP ${d.remainingSp.toLocaleString()}</b>&nbsp;/ 总 SP ${d.totalSp.toLocaleString()}` +
      `&nbsp;&nbsp;|&nbsp;&nbsp;<b>剩余 TP ${d.remainingTp}</b>&nbsp;/ 总 TP ${d.totalTp}` +
      `&nbsp;&nbsp;(附加 SP ${d.bonusSp} / TP ${d.bonusTp})`;
    $('#sp-now').textContent = `当前附加 SP ${d.bonusSp} / TP ${d.bonusTp}`;
  } catch (e) {
    $('#sptp-view').textContent = e.message;
  }
}

async function adjustSp() {
  if (!currentChar) return;
  const sp = parseInt($('#sp-input').value, 10) || 0;
  const tp = parseInt($('#tp-input').value, 10) || 0;
  if (!sp && !tp) return toast('SP/TP 至少填写一个非零值', true);
  try {
    await post(`/api/characters/${currentChar.characterId}/sp`, { sp, tp });
    toast('附加点已调整');
    loadSpTp();
  } catch (e) {
    toast(e.message, true);
  }
}
