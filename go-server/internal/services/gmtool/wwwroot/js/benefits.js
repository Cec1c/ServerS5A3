// Campaign contents and recipients are frozen by the server at preview time.
(() => {
  let revision = null, busy = false, jobs = [], campaigns = [], epoch = 0;
  const panel = $('#benefits-panel');
  function state(text) { $('#benefits-state').textContent = text; }
  function lock(value) {
    busy = value;
    panel.querySelectorAll('button, input, textarea').forEach(el => { if (el.id !== 'btn-close-benefits') el.disabled = value; });
    $('#benefits-save').disabled = value || revision === null;
  }
  function policy(p) {
    revision = p.revision;
    $('#benefits-cera').value = p.accountCera || '';
    $('#benefits-gold').value = p.characterGold || '';
  }
  function line(tag, text) { const el = document.createElement(tag); el.textContent = text; return el; }
  function reward(job) {
    const items = (job.attachments || []).map(a => `物品 ${a.item_id} × ${a.count}`).join('、');
    return [items, job.gold ? `${job.gold.toLocaleString()} 金币` : ''].filter(Boolean).join('，');
  }
  function renderCampaigns() {
    const list = $('#benefits-campaigns'); list.replaceChildren();
    if (!campaigns.length) list.append(line('p', '暂无物品规则。'));
    for (const c of campaigns) {
      const card = document.createElement('div'); card.className = 'card';
      card.append(line('h4', `${c.reward.title} · ${c.enabled ? '已启用' : '已停用'}`),
        line('p', `${c.itemName} · ${reward(c.reward)}`),
        line('p', c.autoNewCharacters ? '新角色自动发放；老角色手动补发' : '仅手动预览并补发'),
        line('p', `规则 ${c.id}`));
      const toggle = line('button', c.enabled ? '停用规则' : '启用规则');
      toggle.type = 'button'; toggle.disabled = busy;
      toggle.addEventListener('click', async () => {
        if (busy || !confirm(`${c.enabled ? '停用' : '启用'}「${c.reward.title}」？\n${c.itemName} · ${reward(c.reward)}\n${c.autoNewCharacters ? '启用后，后台会定时向符合创建时间的新角色发送邮件，无需保持本页面打开。' : '启用后可以手动补发。'}\n重复启停不会清空已发记录。`)) return;
        lock(true);
        try {
          const data = await post(`/api/benefits/campaigns/${c.id}/state`, { revision: c.revision, enabled: !c.enabled, confirm: true });
          campaigns = campaigns.map(old => old.id === c.id ? data.campaign : old); state('规则状态已保存。');
        } catch (e) { state(e.message + '；请刷新规则后重试。'); }
        finally { lock(false); renderCampaigns(); }
      });
      const preview = line('button', '预览现有角色／补发');
      preview.type = 'button'; preview.disabled = busy;
      preview.addEventListener('click', async () => {
        if (busy) return; lock(true);
        try {
          const data = await post(`/api/benefits/campaigns/${c.id}/preview`, {});
          jobs = [data.job, ...jobs].slice(0,20); render();
          const n = counts(data.job); state(`预览完成：已发 ${n.sent}，待补发 ${n.pending}。尚未发送；启用规则后可在下方确认补发。`);
        } catch (e) { state(e.message); } finally { lock(false); }
      });
      card.append(toggle, preview); list.append(card);
    }
  }
  function counts(job) {
    const sent = job.recipients.filter(r => r.mailId).length;
    const failed = job.recipients.filter(r => !r.mailId && r.error).length;
    return { sent, failed, pending: job.recipients.length - sent - failed };
  }
  function render() {
    const list = $('#benefits-jobs'); list.replaceChildren();
    if (!jobs.length) { list.append(line('p', '暂无发放记录。预览不会发送邮件。')); return; }
    for (const job of jobs) {
      const card = document.createElement('div'); card.className = 'card';
      const c = counts(job);
      card.append(line('h4', job.title + ' · ' + (job.started ? '发放记录' : '未发放预览')),
        line('p', `每封 ${reward(job)} · 名单共 ${job.recipients.length} 个角色${job.campaignId ? ' · 复用规则 ' + job.campaignId : ''}`),
        line('p', `已发送 ${c.sent} · 待发送 ${c.pending} · 失败 ${c.failed}`),
        line('p', `批次 ${job.id} · ${new Date(job.createdAt).toLocaleString()}`), line('p', job.body));
      const details = document.createElement('details'); details.append(line('summary', '查看冻结的收件名单与结果'));
      const recipients = document.createElement('ul');
      for (const r of job.recipients) recipients.append(line('li', `${r.name}（角色 ${r.characterId}）— ${r.mailId ? '已发送，邮件 ' + r.mailId : r.error ? '失败：' + r.error : '待发送'}`));
      details.append(recipients); card.append(details);
      if (c.pending || c.failed) {
        const button = line('button', !job.started ? '确认向这些角色发放' : c.pending ? '继续未完成发放' : '重试失败角色');
        button.type = 'button'; button.disabled = busy;
        button.addEventListener('click', () => run(job, c.pending === 0 && c.failed > 0)); card.append(button);
      }
      list.append(card);
    }
  }
  async function load() {
    if (busy) return;
    const current = ++epoch; revision = null; lock(true); state('正在读取…');
    try {
      const data = await api('/api/benefits');
      if (current !== epoch) return;
      policy(data.policy); jobs = data.jobs; campaigns = data.campaigns || []; render(); renderCampaigns(); state('已读取。新建物品规则默认停用，预览不会发送邮件。');
    } catch (e) { state(e.message); } finally { lock(false); }
  }
  async function run(job, retry) {
    if (busy) return;
    if (!confirm(`确认${retry ? '重试失败角色' : '发放未发送邮件'}？\n${job.title}\n每封 ${reward(job)}，冻结名单共 ${job.recipients.length} 个角色。\n已成功发送的角色不会重复发放。`)) return;
    lock(true);
    try {
      let current = job;
      do {
        state('正在发放，请等待。若请求中断，可从本批记录继续。');
        const data = await post(`/api/benefits/jobs/${encodeURIComponent(job.id)}/run`, { confirm: true, retry });
        retry = false; current = data.job;
        jobs = jobs.map(j => j.id === current.id ? current : j); render();
      } while (counts(current).pending > 0);
      const c = counts(current); state(`本批已发送 ${c.sent} 封，失败 ${c.failed} 封。奖励由玩家在游戏邮箱领取。`);
    } catch (e) { state(e.message + '；可刷新记录后继续同一批次，请勿为此重新建批。'); }
    finally { lock(false); render(); }
  }
  $('#btn-benefits').addEventListener('click', () => { panel.classList.remove('hidden'); load(); });
  $('#btn-close-benefits').addEventListener('click', () => panel.classList.add('hidden'));
  $('#benefits-refresh').addEventListener('click', load);
  $('#benefits-item-form').addEventListener('submit', async e => {
    e.preventDefault(); if (busy) return;
    if (!confirm('保存一条新的独立福利规则？\n新规则默认停用，不会发送邮件。后续补发应复用现有规则，不要重复新建。')) return;
    lock(true);
    try {
      const data = await post('/api/benefits/campaigns', { itemId: Number($('#benefits-item-id').value), count: Number($('#benefits-item-count').value), title: $('#benefits-item-title').value, body: $('#benefits-item-body').value, autoNewCharacters: $('#benefits-item-auto').checked });
      campaigns.unshift(data.campaign); state('已保存停用规则。请核对物品名称，再启用并预览补发。');
    } catch (e) { state(e.message + '；若请求中断，请先刷新确认是否已保存，避免重复建规则。'); }
    finally { lock(false); renderCampaigns(); }
  });
  $('#benefits-policy-form').addEventListener('submit', async e => {
    e.preventDefault(); if (busy || revision === null) return;
    const payload = { revision, accountCera: Number($('#benefits-cera').value), characterGold: Number($('#benefits-gold').value) };
    if (!confirm(`保存新手额度？\n新账号点券：${payload.accountCera.toLocaleString()}\n新角色金币：${payload.characterGold.toLocaleString()}\n0 表示关闭，仅影响后续注册／创建。`)) return;
    lock(true);
    try { const data = await post('/api/benefits/policy', payload); policy(data.policy); state('新手额度已保存，立即生效。'); }
    catch (e) { revision = null; state(e.message); } finally { lock(false); }
  });
  $('#benefits-mail-form').addEventListener('submit', async e => {
    e.preventDefault(); if (busy) return; lock(true);
    try {
      const data = await post('/api/benefits/preview', { gold: Number($('#benefits-mail-gold').value), title: $('#benefits-mail-title').value, body: $('#benefits-mail-body').value });
      jobs = [data.job, ...jobs].slice(0, 20); render(); state('预览已生成，尚未发放。请核对下方金额及收件名单，再点击确认发放。');
    } catch (e) { state(e.message); } finally { lock(false); }
  });
})();
