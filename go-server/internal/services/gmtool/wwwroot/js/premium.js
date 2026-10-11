// Account contracts are granted through a selected character; no inventory writes.
document.querySelector('.tab[data-tab="premium"]').addEventListener('click', loadPremiumContracts);

async function loadPremiumContracts() {
  if (!currentChar) return;
  const characterId = currentChar.characterId;
  const epoch = selectEpoch;
  const box = $('#premium-contracts');
  box.textContent = '正在读取契约…';
  try {
    const result = await api(`/api/characters/${characterId}/premium`);
    if (epoch !== selectEpoch || !currentChar || currentChar.characterId !== characterId) return;
    box.innerHTML = '';
    for (const contract of result.contracts) {
      const row = document.createElement('div');
      row.className = 'row';
      const expiry = contract.active ? `有效至 ${formatServerTime(contract.expireAt)}` : '未开通或已到期';
      row.innerHTML = `<b style="width:120px">${escapeHtml(contract.name)}</b>
        <span style="min-width:220px">${escapeHtml(expiry)}</span>
        <label>天数 <input type="number" min="1" max="3650" value="30" style="width:85px"></label>
        <button type="button">开通 / 续期</button>`;
      const button = row.querySelector('button');
      let pendingRequest = null;
      button.onclick = async () => {
        if (!currentChar || currentChar.characterId !== characterId || selectEpoch !== epoch) return;
        const days = Number(row.querySelector('input').value);
        if (!Number.isInteger(days) || days < 1 || days > 3650) return toast('请输入 1 至 3650 的整数天数', true);
        if (!confirm(`为角色 ${currentChar.name} 所属账号开通 / 续期 ${contract.name} ${days} 天？账号内所有角色共享，重新登录游戏后生效。`)) return;
        if (!pendingRequest || pendingRequest.days !== days) {
          pendingRequest = {days, id: typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : 'premium-' + Date.now() + '-' + Math.random().toString(36).slice(2)};
        }
        button.disabled = true;
        try {
          const grant = await post(`/api/characters/${characterId}/premium`, {type:contract.type, days, requestId:pendingRequest.id});
          pendingRequest = null;
          toast(`${grant.name}已开通，请重新登录游戏`);
          if (selectEpoch === epoch) await loadPremiumContracts();
        } catch (e) {
          toast(e.message, true);
          button.disabled = false;
        }
      };
      box.appendChild(row);
    }
    if (!result.contracts.length) box.textContent = '当前资源没有可开通的契约。';
  } catch (e) {
    if (selectEpoch === epoch) box.textContent = e.message;
  }
}

