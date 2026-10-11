// ---- 发放物品 ----

// 类型标签中文名(鼠标悬浮可见原始标签); 含义未经实物确认的不硬翻, 显示原始标签
const TAG_LABELS = {
  // 装备部位
  'weapon': '武器', 'coat': '上衣', 'shoulder': '头肩', 'pants': '下装', 'shoes': '鞋',
  'waist': '腰带', 'amulet': '项链', 'wrist': '手镯', 'ring': '戒指', 'support': '辅助装备',
  'magic stone': '魔法石', 'support weapon': '副武器',
  'title name': '称号', 'name tag': '名称装饰卡',
  'creature': '宠物', 'artifact red': '宠物装备·红', 'artifact blue': '宠物装备·蓝',
  'artifact green': '宠物装备·绿',
  // 装扮部位
  'hat avatar': '帽子装扮', 'hair avatar': '头发装扮', 'face avatar': '脸部装扮',
  'coat avatar': '上衣装扮', 'breast avatar': '胸部装扮', 'waist avatar': '腰部装扮',
  'pants avatar': '下装装扮', 'shoes avatar': '鞋装扮', 'skin avatar': '皮肤装扮',
  'aurora avatar': '光环装扮', 'weapon avatar': '武器装扮',
  // 堆叠物类型(仅列实物确认过的: 附魔宝珠/福包/名称装饰卡等均抽样核对)
  'material': '材料', 'quest': '任务品', 'material expert job': '副职业材料',
  'avatar emblem': '徽章', 'recipe': '设计图', 'dye': '染色剂', 'throw': '投掷物',
  'enchant waste': '附魔宝珠', 'cera package': '点券礼包', 'usable cera package': '点券礼包',
  'cera booster': '福包', 'booster': '礼盒', 'booster selection': '自选礼盒',
  'town and dungeon': '城镇副本道具', 'teleport potion': '传送药剂', 'etc': '其他',
};
// 品级体系依客户端串表(dstr 35103-35105): 勇者=红色仅出自异界(狂龙套=5),
// 镇魂/释魂/杰诺灵魂剑=6=传说。5不是传说。
const RARITY_LABELS = ['普通', '高级', '稀有', '神器', '史诗', '勇者', '传说'];
// 品质细分(数据标记均经实物验证): 传承=[item category] legacy,
// 领主神器=[item category] boss drop, 魔法封印=[random option]
const SPECIAL_LABELS = { sealed: '魔法封印', legacy: '传承', boss: '领主神器' };

function normalizeEquipmentTag(tag) {
  return String(tag || '')
    .replace(/[\[\]`]/g, '')
    .replace(/_/g, ' ')
    .trim()
    .toLowerCase()
    .replace(/\s+\d+$/, '');
}

const tagLabel = (tag) => TAG_LABELS[normalizeEquipmentTag(tag)] || tag || '(无标签)';
const equipmentTypeLabel = (tag) => TAG_LABELS[normalizeEquipmentTag(tag)] || normalizeEquipmentTag(tag) || '(无标签)';

const CONFIG_OPTION_LABELS = {
  'EQUIPMENT PHYSICAL DEFENSE': '物理防御',
  'EQUIPMENT MAGICAL DEFENSE': '魔法防御',
  'PHYSICAL DEFENSE': '物理防御',
  'MAGICAL DEFENSE': '魔法防御',
  'INTELLIGENCE': '智力',
  'SPIRIT': '精神',
  'STRENGTH': '力量',
  'VITALITY': '体力',
  'CAST SPEED': '施放速度',
  'ATTACK SPEED': '攻击速度',
  'MOVE SPEED': '移动速度',
  'HIT RECOVERY': '硬直',
  'ABNORMAL STATUS RESISTANCE': '异常状态抗性',
  'FIRE ELEMENTAL RESISTANCE': '火属性抗性',
  'WATER ELEMENTAL RESISTANCE': '冰属性抗性',
  'ICE ELEMENTAL RESISTANCE': '冰属性抗性',
  'DARK ELEMENTAL RESISTANCE': '暗属性抗性',
  'LIGHT ELEMENTAL RESISTANCE': '光属性抗性',
  'EVASION': '回避率',
  'EQUIPMENT WEIGHT': '负重上限',
  'INVENTORY LIMIT': '负重上限',
  'JUMP': '跳跃力',
  'PHYSICAL DAMAGE REDUCE': '物理伤害减免',
  'MAGICAL DAMAGE REDUCE': '魔法伤害减免',
  'ALL DAMAGE REDUCE': '所有伤害减免',
  'ALL STAT': '所有基础属性',
  'ALL STATS': '所有基础属性',
};

function localizeConfigOptionLabel(label) {
  const raw = String(label || '');
  if (!raw) return raw;
  if (/^(HP|MP)\b/i.test(raw.trim())) return raw;
  const match = raw.match(/^(.+?)(\s+[+\-]\s*\d.*)?$/);
  const body = (match ? match[1] : raw)
    .trim()
    .replace(/^[`\[]+|[`]+$/g, '')
    .replace(/\]$/g, '')
    .replace(/_/g, ' ');
  const suffix = match && match[2] ? match[2].replace(/\s+/g, '') : '';
  const key = body.toUpperCase();
  return CONFIG_OPTION_LABELS[key] ? CONFIG_OPTION_LABELS[key] + suffix : raw;
}

// 装备侧栏分组: 固定顺序, 未列出的标签落入"其他"
const EQUIP_GROUPS = [
  { title: '装备', tags: ['weapon', 'coat', 'shoulder', 'pants', 'shoes', 'waist',
    'amulet', 'wrist', 'ring', 'support', 'magic stone', 'support weapon',
    'title name', 'name tag'] },
  { title: '宠物', tags: ['creature', 'artifact red', 'artifact blue', 'artifact green'] },
  { title: '装扮', tags: ['hat avatar', 'hair avatar', 'face avatar', 'coat avatar',
    'breast avatar', 'waist avatar', 'pants avatar', 'shoes avatar',
    'skin avatar', 'aurora avatar', 'weapon avatar'] },
];
// 堆叠物侧栏 = 背包同款六段(与服务端入格语义一致), 固定顺序
const STACK_SEGMENTS = ['消耗品', '材料', '任务品', '副职业材料', '徽章', '特殊材料'];

let giveCategory = null; // {kind:'equipment', tag/tags} 或 {kind:'stackable', segment/segments}

function giveCategoryMatches(left, right) {
  return JSON.stringify(left || null) === JSON.stringify(right || null);
}

function pipeValues(values) {
  return (values || []).filter(Boolean).join('|');
}

function usableJobChipsHtml(item) {
  const labels = usableJobDisplayLabels(item);
  const cleanLabels = labels.map((label) => String(label || '').trim()).filter(Boolean);
  const allLabels = cleanLabels.length ? cleanLabels : ['无限制'];
  const visible = allLabels.length > 4 ? allLabels.slice(0, 3) : allLabels.slice(0, 4);
  const hiddenCount = allLabels.length - visible.length;
  const chips = visible.map((label) =>
    `<span class="usable-job-chip" title="${escapeHtml(label)}">${escapeHtml(label)}</span>`);
  if (hiddenCount > 0)
    chips.push(`<span class="usable-job-chip more" title="${escapeHtml(allLabels.join(' / '))}">+${hiddenCount}</span>`);
  return `<div class="usable-job-chips" title="${escapeHtml(allLabels.join(' / '))}">${chips.join('')}</div>`;
}

function usableJobDisplayLabels(item) {
  if (Array.isArray(item?.usableJobLabels) && item.usableJobLabels.length) return item.usableJobLabels;
  return [item?.usableJobLabel || '无限制'];
}

function giveCatEl(label, count, isActive, rawTitle, onClick) {
  const el = document.createElement('div');
  el.className = 'cat' + (isActive ? ' active' : '');
  if (rawTitle) el.title = rawTitle;
  el.innerHTML = `<span>${escapeHtml(label)}</span>` +
    (count != null ? `<span class="cnt">${count}</span>` : '');
  el.onclick = onClick;
  return el;
}

// 展开状态跨重渲染保留; 默认全收起, 只显示组头
const giveNavExpanded = new Set();

async function loadGiveCategories(expectedRuntimeEpoch) {
  try {
    const data = await api('/api/items/categories');
    if (expectedRuntimeEpoch != null && expectedRuntimeEpoch !== runtimeSourceEpoch) return;
    const nav = $('#give-category-nav');
    nav.innerHTML = '';
    if (!data.ready) {
      nav.innerHTML = '<div class="group-title">索引构建中…</div>';
      return;
    }

    const pick = (cat) => { giveCategory = cat; loadGiveCategories(); searchItems(); };
    nav.appendChild(giveCatEl('全部', null, giveCategory === null, null, () => pick(null)));

    const equipCounts = new Map(data.equipment.map((c) => [c.tag, c.count]));
    const segCounts = new Map(data.stackable.map((c) => [c.segment, c.count]));
    const listed = new Set();

    syncGiveUsableJobOptions(data.jobs || []);

    // entries: [{label, rawTitle, count, active, cat}]
    const addGroup = (title, entries, groupCat) => {
      const present = entries.filter((e) => e.count != null);
      if (present.length === 0) return;
      const total = present.reduce((sum, e) => sum + e.count, 0);
      const expanded = giveNavExpanded.has(title);
      const head = document.createElement('div');
      head.className = 'group-title group-toggle' + (giveCategoryMatches(giveCategory, groupCat) ? ' active' : '');
      head.innerHTML = `<span><span class="toggle" role="button" title="展开/收起">${expanded ? '▾' : '▸'}</span><span class="group-label">${escapeHtml(title)}</span></span><span class="cnt">${total}</span>`;
      head.onclick = (event) => {
        if (event.target.classList.contains('toggle')) {
          if (giveNavExpanded.has(title)) giveNavExpanded.delete(title);
          else giveNavExpanded.add(title);
          loadGiveCategories();
          return;
        }
        pick(groupCat);
      };
      nav.appendChild(head);
      if (!expanded) return;
      for (const e of present)
        nav.appendChild(giveCatEl(e.label, e.count, e.active, e.rawTitle, () => pick(e.cat)));
    };

    const equipEntry = (tag) => {
      listed.add(tag);
      return {
        label: tagLabel(tag),
        rawTitle: tag,
        count: equipCounts.get(tag),
        active: !!(giveCategory && giveCategory.kind === 'equipment' && giveCategory.tag === tag),
        cat: { kind: 'equipment', tag },
      };
    };

    for (const group of EQUIP_GROUPS)
      addGroup(group.title, group.tags.map(equipEntry), { kind: 'equipment', tags: group.tags.slice() });

    addGroup('消耗品 / 材料', STACK_SEGMENTS.map((seg) => ({
      label: seg,
      rawTitle: '与背包入格分类同语义',
      count: segCounts.get(seg),
      active: !!(giveCategory && giveCategory.kind === 'stackable' && giveCategory.segment === seg),
      cat: { kind: 'stackable', segment: seg },
    })), { kind: 'stackable', segments: STACK_SEGMENTS.slice() });

    const leftovers = data.equipment.filter((c) => !listed.has(c.tag))
      .sort((a, b) => b.count - a.count);
    addGroup('其他', leftovers.map((c) => equipEntry(c.tag)), { kind: 'equipment', tags: leftovers.map((c) => c.tag) });
  } catch (e) {
    toast(e.message, true);
  }
}

function syncGiveUsableJobOptions(jobs) {
  const select = $('#give-usable-job');
  if (!select) return;
  const previous = select.value;
  const defaultValue = currentChar ? String(currentChar.job) : '-1';
  select.innerHTML = '<option value="-1">全部职业</option><option value="-2">无限制</option>';
  for (const job of jobs || []) {
    const option = document.createElement('option');
    option.value = job.value;
    option.textContent = giveJobLabelWithGender(job.value, job.label || `job ${job.value}`);
    select.appendChild(option);
  }
  select.value = previous && [...select.options].some((option) => option.value === previous)
    ? previous
    : defaultValue;
}

function giveJobLabelWithGender(job, label) {
  if (typeof jobLabelWithGender === 'function')
    return jobLabelWithGender(job, label);
  return label;
}

function giveJobGenderSuffix(job) {
  return '';
}

function resetGiveUsableJobToCurrentCharacter() {
  const select = $('#give-usable-job');
  if (!select || !currentChar) return;
  if ([...select.options].some((option) => option.value === String(currentChar.job)))
    select.value = String(currentChar.job);
}

let givePageSize = ItemPageSize.get();
let givePage = 0; // 从 0 计; 换筛选条件时归零
let giveConfiguration = null;
let giveConfigurationEpoch = 0;
let giveSearchSignature = '';
let clearingCharacterMailbox = false;
const GIVE_DELIVERY_MODE_STORAGE_KEY = 'dfogm-give-delivery-mode';
let giveDeliveryMode = 'mail';
let giveRequestInFlight = false;

function readGiveDeliveryMode() {
  try {
    const value = String(window.localStorage.getItem(GIVE_DELIVERY_MODE_STORAGE_KEY) || '');
    return value === 'inventory' || value === 'mail' ? value : 'mail';
  } catch (_) {
    return 'mail';
  }
}

function rememberGiveDeliveryMode() {
  try {
    window.localStorage.setItem(GIVE_DELIVERY_MODE_STORAGE_KEY, giveDeliveryMode);
    return true;
  } catch (_) {
    // Storage can be unavailable in private or restricted browsing modes.
    return false;
  }
}

function giveDeliveryLabel(mode = giveDeliveryMode) {
  return mode === 'inventory' ? '背包发放' : '邮件发放';
}

function updateGiveDeliveryPresentation() {
  const control = $('#give-delivery-mode');
  if (control) {
    control.dataset.mode = giveDeliveryMode;
    control.setAttribute('aria-checked', giveDeliveryMode === 'inventory' ? 'true' : 'false');
    control.setAttribute('aria-label', `发放方式：${giveDeliveryLabel()}`);
    control.disabled = giveRequestInFlight;
    control.title = giveRequestInFlight ? '发放进行中，暂不能切换发放方式' : '';
  }

  document.querySelectorAll('#search-results .give-mode-hint').forEach((cell) => {
    cell.textContent = `配置后${giveDeliveryLabel()}`;
  });
  document.querySelectorAll('#search-results button[data-give-action="grant"]').forEach((button) => {
    button.textContent = giveDeliveryLabel();
    button.disabled = giveRequestInFlight;
  });
  const submit = $('#give-config-submit');
  if (submit) {
    submit.textContent = giveDeliveryLabel();
    submit.disabled = giveRequestInFlight || submit.dataset.baseDisabled === 'true';
  }
}

function setGiveRequestBusy(busy) {
  giveRequestInFlight = !!busy;
  updateGiveDeliveryPresentation();
  document.querySelectorAll('#search-results button[data-give-action="configure"]').forEach((button) => {
    button.disabled = giveRequestInFlight;
  });
}

function bindGiveDeliveryMode() {
  const control = $('#give-delivery-mode');
  if (!control || control._giveDeliveryModeBound) return;
  control._giveDeliveryModeBound = true;
  // 预热发放对话框模式的开关广告（下面那个缓存）：点击时就不必再等一次请求。
  giveDeliveryDialogMode();
  giveDeliveryMode = readGiveDeliveryMode();
  control.onclick = () => {
    const next = giveDeliveryMode === 'inventory' ? 'mail' : 'inventory';
    if (giveRequestInFlight) return;
    if (next === 'inventory'
        && !confirm('背包发放会直接写入新版背包，确认切换到背包发放吗？')) return;
    giveDeliveryMode = next;
    if (!rememberGiveDeliveryMode())
      toast('当前发放方式已切换，但无法跨刷新记忆。', true);
    updateGiveDeliveryPresentation();
    if (giveConfiguration)
      renderGrantConfiguration();
  };
  updateGiveDeliveryPresentation();
}

// ---- 发放对话框（DNFBRIDGE_GM_MAIL_GRANT_DIALOG）-------------------------------
// 现场报障（操作者原话 + 截图）：「物品列表每一行的『邮件发放』按钮，点下去直接就把
// 邮件发了；应该像 A21 GM 工具那样先弹一个对话框，里面有能填的 强化 / 红字 / 锻造」。
// 改动前只有"需要配置"的行（装备/称号/限期物）走 :712 的「配置」按钮弹浮层卡片，
// 其余行走 :713 的直发按钮 —— 同一张列表两套行为，操作者点的恰好是后者。
//
// 现在：**每一行的按钮都先打开同一张对话框**（#give-config-card，A21 工具就是这么做的：
// Temp\gm-upstream-v260910\wwwroot\js\give.js:516-598），数量带入行内的值，
// 强化 / 红字 / 锻造 三个框由服务端能力块（GET …/grant-options）决定渲不渲染，
// 按对话框里的按钮才真正发邮件。
//
// 开关真源 = 服务端 GET /api/environment 的 switches[]（api_items_grant_dialog.go：
// DNFBRIDGE_GM_MAIL_GRANT_DIALOG，默认 on；off/direct 等显式字面量 = 回退成今天的直发）。
// 本文件只读那一个真源，不在前端重复一套判断；**取不到就按默认档（对话框）走** ——
// 读不到开关绝不等于"悄悄回退成直发"。
const giveDialogModeCache = { dialog: true, optionsOn: false, known: false };
let giveDialogModePromise = null;

function applyGiveDialogSwitches(switches) {
  for (const entry of switches || []) {
    if (!entry || typeof entry.name !== 'string') continue;
    if (entry.name === 'DNFBRIDGE_GM_MAIL_GRANT_DIALOG') {
      giveDialogModeCache.dialog = entry.enabled !== false;
      giveDialogModeCache.known = true;
    } else if (entry.name === 'DNFBRIDGE_GM_MAIL_OPTIONS') {
      // 二次诊断用：能力块恒为 off 档历史常量时，对话框里那句"暂不可填"靠它解释。
      giveDialogModeCache.optionsOn = entry.enabled === true;
    }
  }
}

async function giveDeliveryDialogMode() {
  if (!giveDialogModePromise) {
    giveDialogModePromise = (async () => {
      try {
        const report = await api('/api/environment');
        applyGiveDialogSwitches(report && report.switches);
      } catch (_) {
        // 探测失败保持默认档（对话框），不改变任何行为。
      }
      return giveDialogModeCache;
    })();
  }
  return giveDialogModePromise;
}

// openGrantDialogForRow 是发放列表行内按钮的唯一入口。
//   对话框档：拉能力块 → 打开对话框（数量带入行内输入框的值）。
//   回退档（DNFBRIDGE_GM_MAIL_GRANT_DIALOG=off/direct）：与加这条开关之前**逐字节相同**
//   —— 一个字段都不问，直接用行内数量发那一封邮件（giveItem 的请求体也不带 options）。
async function openGrantDialogForRow(item, row) {
  if (giveRequestInFlight) return;
  if (!currentChar) { toast('请先选择角色', true); return; }
  const input = row ? row.querySelector('input') : null;
  const seedCount = Math.max(1, parseInt(input ? input.value : '', 10) || 1);
  const mode = await giveDeliveryDialogMode();
  if (!mode.dialog) {
    await giveItem(item.itemId, seedCount);
    return;
  }
  await configureGrantItem(item, row, seedCount);
}

function updateCharacterMailboxButton() {
  const button = $('#btn-clear-character-mailbox');
  if (!button) return;
  button.disabled = !currentChar || clearingCharacterMailbox;
  button.textContent = clearingCharacterMailbox ? '清空中…' : '清空邮箱';
}

function bindClearCharacterMailbox() {
  const button = $('#btn-clear-character-mailbox');
  if (!button || button._mailboxClearBound) {
    updateCharacterMailboxButton();
    return;
  }
  button._mailboxClearBound = true;
  button.onclick = clearCurrentCharacterMailbox;
  updateCharacterMailboxButton();
}

async function clearCurrentCharacterMailbox() {
  if (clearingCharacterMailbox || !currentChar) return;
  const snapshot = {
    characterId: Number(currentChar.characterId),
    name: String(currentChar.name ?? ''),
  };
  if (!Number.isSafeInteger(snapshot.characterId) || snapshot.characterId <= 0) {
    toast('当前角色无有效 ID', true);
    return;
  }
  if (!confirm(`是否确认清空当前角色 ${snapshot.name} 的邮箱？`)) return;

  clearingCharacterMailbox = true;
  updateCharacterMailboxButton();
  try {
    const result = await post(`/api/characters/${encodeURIComponent(String(snapshot.characterId))}/mailbox/clear`);
    const recipientCount = Number(result.recipientCount || 0);
    const messageCount = Number(result.messageCount || 0);
    const attachmentCount = Number(result.attachmentCount || 0);
    const auditCount = Number(result.auditCount || 0);
    toast(`已清空角色 ${snapshot.name} 的邮箱：收件人 ${recipientCount}、邮件 ${messageCount}、附件 ${attachmentCount}、审计 ${auditCount}`);
  } catch (e) {
    toast(`清空角色 ${snapshot.name} 的邮箱失败：${e.message}`, true);
  } finally {
    clearingCharacterMailbox = false;
    updateCharacterMailboxButton();
  }
}

// 发放 option 的跨刷新记忆。qualityMode 用服务端词表里的字符串（default/random/top，
// 见 gmtool/api_items_grant_options.go），其余是整数。
const giveConfigMemory = {
  qualityMode: 'default',
  upgradeLevel: 0,
  amplifyType: 0,
  forgingLevel: 0,
  manualGrantType: '',
  expirationMode: 'default',
  expirationDays: 30,
  avatarOptionByPart: Object.create(null),
  avatarDurationByPart: Object.create(null),
};

function legalRememberedValue(options, value, fallback) {
  return (options || []).some((option) => String(option.value) === String(value)) ? value : fallback;
}

function rememberGiveConfiguration() {
  if (!giveConfiguration) return;
  const part = String(giveConfiguration.capability?.avatar?.part || '');
  const readInt = (selector, fallback) => {
    const element = $(selector);
    const value = element ? parseInt(element.value, 10) : NaN;
    return Number.isFinite(value) ? value : fallback;
  };
  // 品级是字符串词表，不能走 readInt（parseInt('top') 是 NaN，会把选择静默丢掉）。
  const quality = $('#give-config-quality');
  if (quality) giveConfigMemory.qualityMode = quality.value;
  giveConfigMemory.upgradeLevel = readInt('#give-config-upgrade', giveConfigMemory.upgradeLevel);
  giveConfigMemory.amplifyType = readInt('#give-config-amplify', giveConfigMemory.amplifyType);
  giveConfigMemory.forgingLevel = readInt('#give-config-forging', giveConfigMemory.forgingLevel);
  giveConfigMemory.expirationDays = readInt('#give-config-expiration-days', giveConfigMemory.expirationDays);
  const manual = $('#give-config-manual-type');
  if (manual) giveConfigMemory.manualGrantType = manual.value;
  const expirationMode = $('#give-config-expiration-mode');
  if (expirationMode) giveConfigMemory.expirationMode = expirationMode.value;
  const avatarOption = $('#give-config-avatar-option');
  if (avatarOption && part) giveConfigMemory.avatarOptionByPart[part] = parseInt(avatarOption.value, 10);
  const avatarDuration = $('#give-config-avatar-duration');
  if (avatarDuration && part) giveConfigMemory.avatarDurationByPart[part] = parseInt(avatarDuration.value, 10);
}

function clearGiveConfiguration() {
  rememberGiveConfiguration();
  giveConfigurationEpoch++;
  giveConfiguration = null;
  const card = $('#give-config-card');
  if (card) {
    card.innerHTML = '';
    FloatingConfigPanel.hide(card);
  }
  document.querySelectorAll('#search-results tr.config-selected')
    .forEach((row) => row.classList.remove('config-selected'));
}

function isLimitedTemplate(item) {
  const expiry = item && item.templateExpiration;
  return !!(expiry && (expiry.absoluteExpireTime > 0 || expiry.usablePeriodDays > 0));
}

function needsGrantConfiguration(item) {
  if (!item) return false;
  // Titles have one PVF grade (middle) but their stat roll still varies.
  // Keep them on the configuration path so the grant-options endpoint can
  // expose Top (full stats) and Random choices.
  if (item.kind === 'equipment' && String(item.tag || '').toLowerCase() === 'title name')
    return true;
  if (typeof item.requiresConfiguration === 'boolean') return item.requiresConfiguration;
  return item.kind === 'equipment' || item.requiresManualGrantType || isLimitedTemplate(item);
}

function optionHtml(options, selectedValue) {
  return (options || []).map((option) => {
    const selected = String(option.value) === String(selectedValue) ? ' selected' : '';
    return `<option value="${escapeHtml(String(option.value))}"${selected}>${escapeHtml(localizeConfigOptionLabel(option.label))}</option>`;
  }).join('');
}

function avatarOptionControlHtml(id, options, selectedValue) {
  const list = options || [];
  const select = `<select id="${id}"${list.some((option) => option.isSkill) ? ' class="hidden"' : ''}>${optionHtml(list, selectedValue)}</select>`;
  if (!list.some((option) => option.isSkill))
    return select;

  const selected = list.find((option) => String(option.value) === String(selectedValue)) || list[0];
  const selectedLabel = selected ? localizeConfigOptionLabel(selected.label) : '';
  const datalistId = `${id}-list`;
  const datalist = list.map((option) =>
    `<option value="${escapeHtml(localizeConfigOptionLabel(option.label))}"></option>`).join('');
  return `<input id="${id}-search" type="text" list="${datalistId}" value="${escapeHtml(selectedLabel)}" autocomplete="off" spellcheck="false" placeholder="输入技能名快速定位">` +
    `<datalist id="${datalistId}">${datalist}</datalist>${select}`;
}

function bindAvatarOptionSearch(id, onValidChange) {
  const input = $(`#${id}-search`);
  const select = $(`#${id}`);
  if (!input || !select) return;
  const sync = () => {
    const match = Array.from(select.options).find((option) => option.textContent === input.value);
    select.value = match ? match.value : '';
    if (match && onValidChange) onValidChange();
  };
  input.addEventListener('input', sync);
  input.addEventListener('change', sync);
}

function readAvatarOptionValue(id) {
  const select = $(`#${id}`);
  if (!select) return { ok: false, error: '没有可保存的时装属性' };
  const input = $(`#${id}-search`);
  if (input) {
    const match = Array.from(select.options).find((option) => option.textContent === input.value);
    if (!match)
      return { ok: false, error: '请选择列表中的合法技能名，不支持自定义输入' };
    return { ok: true, value: parseInt(match.value, 10) };
  }
  return { ok: true, value: parseInt(select.value, 10) };
}

async function configureGrantItem(item, row, seedCount) {
  if (giveRequestInFlight) return;
  if (!currentChar) { toast('请先选择角色', true); return; }
  rememberGiveConfiguration();
  const epoch = ++giveConfigurationEpoch;
  // 数量由调用方带入（列表行内输入框的值）；对话框自己也是这个值的权威 ——
  // 提交时读的是 #give-config-count。
  const count = Number.isFinite(Number(seedCount)) && Number(seedCount) > 0
    ? Math.trunc(Number(seedCount))
    : 1;
  try {
    const capability = await api(`/api/characters/${currentChar.characterId}/items/${item.itemId}/grant-options`);
    if (epoch !== giveConfigurationEpoch) return;
    giveConfiguration = { item, capability, count };
    document.querySelectorAll('#search-results tr.config-selected')
      .forEach((candidate) => candidate.classList.remove('config-selected'));
    if (row) row.classList.add('config-selected');
    renderGrantConfiguration();
  } catch (e) {
    toast(e.message, true);
  }
}

function renderGrantConfiguration() {
  const card = $('#give-config-card');
  if (!giveConfiguration || !card) {
    clearGiveConfiguration();
    return;
  }

  const { item, capability } = giveConfiguration;
  const fields = [];
  const seedCount = Number.isFinite(Number(giveConfiguration.count)) && Number(giveConfiguration.count) > 0
    ? Math.trunc(Number(giveConfiguration.count))
    : 1;
  fields.push(`<label class="give-config-field"><span>数量</span><input id="give-config-count" type="number" min="1" value="${seedCount}"></label>`);

  if (capability.equipment) {
    const equipment = capability.equipment;
    // 下面几个布尔位由服务端发放能力块给出：DNFBRIDGE_GM_MAIL_OPTIONS 未开时它们全是
    // false（gmtool/api_items.go 的原响应），渲染结果与改动前完全一样。
    if (equipment.supportsQuality) {
      const quality = legalRememberedValue(equipment.qualityOptions, giveConfigMemory.qualityMode, 'default');
      fields.push(`<label class="give-config-field"><span>装备品级</span><select id="give-config-quality">${optionHtml(equipment.qualityOptions, quality)}</select></label>`);
    }
    if (equipment.canUpgrade)
      fields.push(`<label class="give-config-field"><span>强化等级</span><input id="give-config-upgrade" type="number" min="0" max="${equipment.maxUpgradeLevel}" value="${giveConfigMemory.upgradeLevel}"></label>`);
    if (equipment.canAmplify) {
      const amplify = legalRememberedValue(equipment.amplifyTypes, giveConfigMemory.amplifyType, 0);
      fields.push(`<label class="give-config-field"><span>红字属性</span><select id="give-config-amplify">${optionHtml(equipment.amplifyTypes, amplify)}</select></label>`);
    }
    if (equipment.canForge)
      fields.push(`<label class="give-config-field"><span>锻造</span><input id="give-config-forging" type="number" min="0" max="${equipment.maxForgingLevel}" value="${giveConfigMemory.forgingLevel}"></label>`);
    // 三个框一个都没出来时，说清楚"为什么"：只有服务端没开 DNFBRIDGE_GM_MAIL_OPTIONS
    // 才会让整块能力位恒为 off 档的历史常量（四个布尔位全 false）。物品类型**自己**
    // 不支持时不解释 —— 那是物品的性质，写一句话只会误导。
    if (item.kind === 'equipment' && !equipment.supportsQuality && !equipment.canUpgrade
        && !equipment.canAmplify && !equipment.canForge
        && giveDialogModeCache.known && !giveDialogModeCache.optionsOn)
      fields.push(`<div class="give-config-field"><span>强化 / 红字 / 锻造</span><div class="give-config-value">服务端未开启 DNFBRIDGE_GM_MAIL_OPTIONS，本次不可填</div></div>`);
  }

  if (capability.manual && capability.manual.required) {
    const selected = legalRememberedValue(capability.manual.choices, giveConfigMemory.manualGrantType, capability.manual.choices[0]?.value || '');
    fields.push(`<label class="give-config-field"><span>手动分类</span><select id="give-config-manual-type">${optionHtml(capability.manual.choices, selected)}</select></label>`);
  }

  let grantDisabled = false;
  if (capability.avatar) {
    const avatar = capability.avatar;
    fields.push(`<div class="give-config-field"><span>装扮部位</span><div class="give-config-value">${escapeHtml(equipmentTypeLabel(avatar.part))}</div></div>`);
    if (!avatar.compatible || !avatar.options || avatar.options.length === 0) {
      fields.push('<div class="give-config-field"><span>可选属性</span><div class="give-config-value">当前职业不可用</div></div>');
      grantDisabled = true;
    } else {
      const selected = legalRememberedValue(avatar.options, giveConfigMemory.avatarOptionByPart[avatar.part], avatar.options[0].value);
      fields.push(`<label class="give-config-field"><span>可选属性</span>${avatarOptionControlHtml('give-config-avatar-option', avatar.options, selected)}</label>`);
    }
    if (avatar.durations && avatar.durations.length > 0) {
      const permanent = avatar.durations.find((value) => value.days === 0);
      const defaultDays = permanent ? 0 : avatar.durations[0].days;
      const durationOptions = avatar.durations.map((value) => ({ value: value.days, label: value.label }));
      const selectedDays = legalRememberedValue(durationOptions, giveConfigMemory.avatarDurationByPart[avatar.part], defaultDays);
      fields.push(`<label class="give-config-field"><span>使用期限</span><select id="give-config-avatar-duration">${optionHtml(durationOptions, selectedDays)}</select></label>`);
    }
  } else if (capability.expiration && capability.expiration.canOverride) {
      const expired = capability.expiration.expired === true;
      const mode = expired ? 'custom' : giveConfigMemory.expirationMode;
      const modeOptions = expired
        ? '<option value="custom" selected>自定义有效期</option>'
        : `<option value="default"${mode === 'default' ? ' selected' : ''}>PVF 默认期限</option><option value="custom"${mode === 'custom' ? ' selected' : ''}>自定义天数</option>`;
      fields.push(`<label class="give-config-field"><span>期限方式</span><select id="give-config-expiration-mode">${modeOptions}</select></label>`);
      fields.push(`<label id="give-config-expiration-days-field" class="give-config-field${mode === 'custom' ? '' : ' hidden'}"><span>期限天数</span><input id="give-config-expiration-days" type="number" min="1" max="${capability.expiration.maxDays}" value="${giveConfigMemory.expirationDays || 30}"></label>`);
  }

  card.innerHTML = `<div class="give-config-head"><div class="give-config-title rarity-${item.rarity >= 0 && item.rarity <= 6 ? item.rarity : 0}">${escapeHtml(item.name)}</div><div class="give-config-meta">ID ${item.itemId} · ${escapeHtml(tagLabel(item.tag))} · 当前${escapeHtml(giveDeliveryLabel())}</div></div>` +
    `<div class="give-config-grid">${fields.join('')}</div>` +
    `<div class="give-config-actions"><button id="give-config-cancel" type="button">取消</button><button id="give-config-submit" type="button" data-base-disabled="${grantDisabled ? 'true' : 'false'}" ${grantDisabled ? 'disabled' : ''}>${escapeHtml(giveDeliveryLabel())}</button></div>`;
  FloatingConfigPanel.show(card, {
    avoidSelector: '#search-results thead th:nth-last-child(2)',
  });

  $('#give-config-cancel').onclick = clearGiveConfiguration;
  bindAvatarOptionSearch('give-config-avatar-option', rememberGiveConfiguration);
  const expirationMode = $('#give-config-expiration-mode');
  if (expirationMode) {
    expirationMode.onchange = () => {
      $('#give-config-expiration-days-field').classList.toggle('hidden', expirationMode.value !== 'custom');
      rememberGiveConfiguration();
      FloatingConfigPanel.refresh(card);
    };
  }
  card.querySelectorAll('input:not(#give-config-count), select').forEach((element) => {
    if (element !== expirationMode)
      element.addEventListener(element.tagName === 'INPUT' ? 'input' : 'change', rememberGiveConfiguration);
  });
  $('#give-config-submit').onclick = submitConfiguredGrant;
  updateGiveDeliveryPresentation();
}

// readConfigInt 读一个整数控件。控件不存在返回 undefined（= 没有这个 option），
// 存在但读不出整数返回 null（= 明确失败，调用方必须中止并说明原因）。
function readConfigInt(selector) {
  const element = $(selector);
  if (!element) return undefined;
  const value = parseInt(String(element.value).trim(), 10);
  return Number.isFinite(value) ? value : null;
}

async function submitConfiguredGrant() {
  if (!giveConfiguration || giveRequestInFlight) return;
  const { item } = giveConfiguration;
  rememberGiveConfiguration();
  const count = Math.max(1, parseInt($('#give-config-count').value, 10) || 1);
  // 只发送服务端真正消费的 5 个字段（服务端词表见 gmtool/api_items_grant_options.go
  // 的 gmMailGrantOptionKeys：expire_days / reinforce / amplify_type /
  // equipment_quality / forging_level）。服务端在 DNFBRIDGE_GM_MAIL_OPTIONS=on 时对
  // 未知字段是**明确拒绝**，所以这里不再发旧的驼峰字段名 qualityMode/upgradeLevel ——
  // 服务端不认识它们，发过去只会被拒。
  // 锻造在 90CN **是**有这个属性的（90CN 落点是 0x77 物品行的 0x57 字节，上限取 PVF
  // etc/upgrade_separate.etc 的 [separate upgrade max]），所以它必须真的发出去：
  // 过去这个输入框的值读进 giveConfigMemory 之后就被丢掉了，操作者填了等于没填。
  // 读不出整数时本地就明确失败，绝不悄悄按 0 发出去。
  const options = {};
  const quality = $('#give-config-quality');
  if (quality) options.equipment_quality = String(quality.value);
  const reinforce = readConfigInt('#give-config-upgrade');
  if (reinforce === null) return toast('强化等级要填 0..31 的整数；本次未发放任何物品。', true);
  if (reinforce !== undefined) options.reinforce = reinforce;
  const amplify = readConfigInt('#give-config-amplify');
  if (amplify === null) return toast('红字属性要选 0..4 的整数；本次未发放任何物品。', true);
  if (amplify !== undefined) options.amplify_type = amplify;
  const forging = readConfigInt('#give-config-forging');
  if (forging === null)
    return toast(`锻造等级要填 0..${giveConfiguration.capability?.equipment?.maxForgingLevel ?? 0} 的整数；本次未发放任何物品。`, true);
  if (forging !== undefined) options.forging_level = forging;
  const avatarDuration = $('#give-config-avatar-duration');
  if (avatarDuration) options.expire_days = parseInt(avatarDuration.value, 10);
  const expirationMode = $('#give-config-expiration-mode');
  if (expirationMode && expirationMode.value === 'custom') {
    const days = readConfigInt('#give-config-expiration-days');
    if (days === null) return toast('期限天数要填 1..3650 的整数；本次未发放任何物品。', true);
    options.expire_days = days;
  }
  // 装扮属性 / 手动分类的控件在 90CN 永不出现（发放能力块里 avatar 与 manual 恒为 null），
  // 而服务端在 on 档对未知字段是明确拒绝，所以这里**不发**这两个键：
  // 与其发一个服务端不认识、会被整单拒掉的字段，不如一个字段都不发。
  await giveItem(item.itemId, count, options);
}

async function searchItems(page) {
  givePage = page || 0;
  const q = $('#search-input').value.trim();
  const minLv = parseInt($('#give-minlv').value, 10) || 0;
  const maxLv = parseInt($('#give-maxlv').value, 10) || 0;
  const raritySel = $('#give-rarity').value;
  const expiration = $('#give-expiration').value;
  const usableJob = parseInt($('#give-usable-job')?.value || '-1', 10);
  const special = SPECIAL_LABELS[raritySel] ? raritySel : '';
  const rarity = special ? -1 : parseInt(raritySel, 10);
  const signature = JSON.stringify({ q, minLv, maxLv, raritySel, expiration, usableJob, giveCategory, characterId: currentChar?.characterId });
  if (signature !== giveSearchSignature) {
    giveSearchSignature = signature;
    clearGiveConfiguration();
  }
  if (!q && !giveCategory && minLv === 0 && maxLv === 0 && rarity < 0 && !special && expiration === 'all') {
    $('#search-results tbody').innerHTML =
      '<tr><td colspan="9" class="hint">选择左侧分类或输入关键词开始浏览</td></tr>';
    $('#give-total').textContent = '';
    $('#give-pager').innerHTML = '';
    return;
  }
  try {
    let url = `/api/items/browse?limit=${givePageSize}&offset=${givePage * givePageSize}` +
      `&q=${encodeURIComponent(q)}&minLevel=${minLv}&maxLevel=${maxLv}&rarity=${rarity}` +
      `&expiration=${encodeURIComponent(expiration)}&usableJob=${usableJob}`;
    if (special) url += `&special=${special}`;
    if (giveCategory) {
      url += `&kind=${encodeURIComponent(giveCategory.kind)}`;
      if (giveCategory.tag) url += `&tag=${encodeURIComponent(giveCategory.tag)}`;
      if (giveCategory.tags) url += `&tag=${encodeURIComponent(pipeValues(giveCategory.tags))}`;
      if (giveCategory.segment) url += `&segment=${encodeURIComponent(giveCategory.segment)}`;
      if (giveCategory.segments) url += `&segment=${encodeURIComponent(pipeValues(giveCategory.segments))}`;
    }
    const data = await api(url);
    const pageCount = Math.max(1, Math.ceil(data.total / givePageSize));
    // 条件变化后可能停留在越界页, 自动回退到末页
    if (givePage >= pageCount && data.total > 0) {
      searchItems(pageCount - 1);
      return;
    }
    $('#give-total').textContent = `共 ${data.total} 个匹配`;
    const tbody = $('#search-results tbody');
    tbody.innerHTML = '';
    for (const r of data.results) {
      const tr = document.createElement('tr');
      const configurable = needsGrantConfiguration(r);
      tr.innerHTML = `<td>${r.itemId}</td>
        <td class="rarity-${r.rarity >= 0 && r.rarity <= 6 ? r.rarity : 0}">${escapeHtml(r.name)}</td>
        <td>${r.minLevel || ''}</td>
        <td>${r.special ? (SPECIAL_LABELS[r.special] || escapeHtml(r.special)) : (RARITY_LABELS[r.rarity] || r.rarity)}</td>
        <td title="${escapeHtml(r.tag || '')}">${escapeHtml(tagLabel(r.tag))}</td>
        <td>${escapeHtml(r.usableJobLabel || '无限制')}</td>
        <td>${templateExpirationLabel(r)}</td>` +
        (configurable
          ? `<td class="hint give-mode-hint">配置后${escapeHtml(giveDeliveryLabel())}</td><td><button class="mini" data-give-action="configure">配置</button></td>`
          : `<td><input type="number" value="1" min="1"></td><td><button class="mini" data-give-action="grant">${escapeHtml(giveDeliveryLabel())}</button></td>`);
      const usableJobCell = tr.children[5];
      if (usableJobCell) {
        usableJobCell.className = 'usable-job-cell';
        usableJobCell.innerHTML = usableJobChipsHtml(r);
      }
      const button = tr.querySelector('button');
      button.onclick = (event) => {
        event.stopPropagation();
        if (configurable)
          configureGrantItem(r, tr);
        else
          // 直发行（堆叠物/消耗品…）也先弹对话框：数量带入行内输入框的值。
          // 回退档（DNFBRIDGE_GM_MAIL_GRANT_DIALOG=off/direct）里这个函数直接发。
          openGrantDialogForRow(r, tr);
      };
      tr.onclick = () => configurable ? configureGrantItem(r, tr) : clearGiveConfiguration();
      tbody.appendChild(tr);
    }
    if (data.results.length === 0)
      tbody.innerHTML = '<tr><td colspan="9" class="hint">没有匹配的物品</td></tr>';
    updateGiveDeliveryPresentation();

    const pager = $('#give-pager');
    pager.innerHTML = '';
    if (data.total > givePageSize) {
      const prev = document.createElement('button');
      prev.className = 'mini';
      prev.textContent = '上一页';
      prev.disabled = givePage === 0;
      prev.onclick = () => searchItems(givePage - 1);
      const next = document.createElement('button');
      next.className = 'mini';
      next.textContent = '下一页';
      next.disabled = givePage >= pageCount - 1;
      next.onclick = () => searchItems(givePage + 1);
      const info = document.createElement('span');
      info.className = 'hint';
      info.textContent = `第 ${givePage + 1} / ${pageCount} 页`;
      pager.append(prev, info, next);
    }
  } catch (e) {
    toast(e.message, true);
  }
}

function bindGivePageSize() {
  const select = $('#give-page-size');
  if (!select) return;
  select.value = String(givePageSize);
  select.onchange = () => {
    ItemPageSize.set(select.value);
  };
  ItemPageSize.subscribe((value) => {
    if (value === givePageSize && select.value === String(value)) return;
    givePageSize = value;
    select.value = String(value);
    searchItems(0);
  });
}

async function giveItem(templateId, count, options) {
  if (giveRequestInFlight) return;
  if (!currentChar) { toast('请先选择角色', true); return; }
  const characterSnapshot = {
    characterId: Number(currentChar.characterId),
    name: String(currentChar.name || ''),
  };
  if (!Number.isSafeInteger(characterSnapshot.characterId) || characterSnapshot.characterId <= 0) {
    toast('当前角色无有效 ID', true);
    return;
  }
  const deliveryModeSnapshot = giveDeliveryMode === 'inventory' ? 'inventory' : 'mail';
  setGiveRequestBusy(true);
  try {
    const requestId = typeof globalThis.crypto?.randomUUID === 'function'
      ? globalThis.crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(16).slice(2)}-${Math.random().toString(16).slice(2)}`;
    const body = { templateId, count, requestId, deliveryMode: deliveryModeSnapshot };
    // 一个 option 都没选（或本来就没有配置面板）时不带 options：请求体与改动前逐字节相同，
    // 服务端 off 档与 on 档都会走"不介入"的路径。
    if (options && Object.keys(options).length > 0) body.options = options;
    const r = await post(`/api/characters/${encodeURIComponent(String(characterSnapshot.characterId))}/items`, body);
    const itemLabel = `${r.name || r.itemTemplateId || templateId} x${r.count ?? count}`;
    const deliveryHint = String(r.deliveryHint || '').trim();
    let summary;
    let fallbackHint = '';
    switch (r.delivery) {
      case 'mail': {
        const attachmentCount = Number(r.attachmentCount || 0);
        const messageCount = Number(r.messageCount || (r.messageId ? 1 : 0));
        summary = messageCount > 0
          ? `邮件发放成功（${messageCount} 封邮件、${attachmentCount} 个附件）`
          : '邮件发放成功';
        fallbackHint = '重新打开邮箱即可，无需重新选择角色。';
        break;
      }
      case 'inventory': {
        const grantedCount = Number(r.grantedCount || r.count || count || 0);
        const slots = Array.isArray(r.slots) ? r.slots.filter((slot) => Number.isFinite(Number(slot))) : [];
        const slotText = slots.length ? `，槽位 ${slots.join('、')}` : '';
        summary = `背包发放成功（${grantedCount} 件${slotText}）`;
        fallbackHint = '请返回角色选择界面后重新进入以刷新显示。';
        break;
      }
      case 'direct_cube':
        summary = '晶块发放成功';
        fallbackHint = '请返回角色选择界面后重新进入以刷新显示。';
        break;
      case 'direct_revive':
        summary = '复活币发放成功';
        fallbackHint = '请返回角色选择界面后重新进入以刷新显示。';
        break;
      case 'direct_name_tag':
        summary = '名称装饰卡发放成功';
        fallbackHint = '请返回角色选择界面后重新进入以刷新显示。';
        break;
      case 'direct_premium':
        summary = '账号契约发放成功';
        fallbackHint = '请返回角色选择界面后重新进入以刷新显示。';
        break;
      default:
        summary = '发放成功';
        break;
    }
    toast(`${itemLabel}：${summary}${deliveryHint || fallbackHint ? `。${deliveryHint || fallbackHint}` : ''}`);
    if (typeof loadItems === 'function'
        && currentChar
        && Number(currentChar.characterId) === characterSnapshot.characterId)
      loadItems();
    if (options
        && currentChar
        && Number(currentChar.characterId) === characterSnapshot.characterId
        && giveDeliveryMode === deliveryModeSnapshot)
      clearGiveConfiguration();
  } catch (e) {
    toast(e.message, true);
  } finally {
    setGiveRequestBusy(false);
  }
}
