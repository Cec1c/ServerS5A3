'use strict';
const $ = id => document.getElementById(id);
let csrf = '', sources = [], candidate = null, news = [];
function message(text) { $('message').textContent = text; }
async function api(path, method = 'GET', body) {
  const response = await fetch('/admin/api/' + path, {method, credentials:'same-origin', headers:{'Content-Type':'application/json','X-CSRF-Token':csrf}, body:body === undefined ? undefined : JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) { if(response.status === 401) showLogin(); throw new Error(data.error || '操作失败'); }
  return data;
}
function showLogin() { $('login').hidden=false; $('workspace').hidden=true; $('logout').hidden=true; $('gm').hidden=true; csrf=''; candidate=null; }
async function load() {
  const state=await api('state'); csrf=state.csrf; sources=state.sources||[];
  $('login').hidden=true; $('workspace').hidden=false; $('logout').hidden=false; $('gm').hidden=!state.gmConfigured;
  $('version').textContent=state.published.content?.version||'尚未发布游戏更新';
  $('publication').textContent='配置版本 '+state.published.sequence+' · 账号入口与兼容指纹由服务器配置提供';
  const selected=$('source-list').value; $('source-list').replaceChildren(new Option('新增来源',''));
  sources.forEach(s=>$('source-list').add(new Option(s.name+(s.enabled?'':'（停用）'),s.id))); $('source-list').value=selected;
  news=state.published.news||[];const newsSelected=$('news-list').value;$('news-list').replaceChildren(new Option('新增公告','-1'));news.forEach((n,i)=>$('news-list').add(new Option(n.title,String(i))));$('news-list').value=newsSelected;
}
function guarded(fn) { return async event => { event?.preventDefault(); const button=event?.submitter || (event?.currentTarget?.tagName==='BUTTON'?event.currentTarget:null); if(button)button.disabled=true; try{message('');await fn();}catch(error){message(error.message);}finally{if(button)button.disabled=button.id==='publish'&&(!candidate?.compatible||!$('confirmed').checked);} }; }
$('login-form').addEventListener('submit',guarded(async()=>{ const token=$('token').value; try{const s=await api('login','POST',{token,remember:$('remember').checked});csrf=s.csrf;await load();}finally{$('token').value='';} }));
$('logout').addEventListener('click',guarded(async()=>{await api('logout','POST',{});showLogin();message('已退出，此会话已撤销。');}));
$('source-list').addEventListener('change',()=>{const s=sources.find(s=>s.id===$('source-list').value);['id','name'].forEach(k=>$('source-'+k).value=s?.[k]||'');$('source-url').value=s?.endpoint||'';$('source-pin').value=s?.certificateSha256||'';$('source-key').value=s?.publicKey||'';$('source-enabled').checked=s?.enabled??true;candidate=null;$('publish').disabled=true;$('files').replaceChildren();});
$('source-form').addEventListener('submit',guarded(async()=>{const id=$('source-id').value;await api('source','PUT',{id,name:$('source-name').value,endpoint:$('source-url').value,publicKey:$('source-key').value,certificateSha256:$('source-pin').value,enabled:$('source-enabled').checked});await load();$('source-list').value=id;candidate=null;$('publish').disabled=true;message('来源已保存；尚未发布或安装更新。');}));
$('inspect').addEventListener('click',guarded(async()=>{candidate=null;$('publish').disabled=true;$('confirmed').checked=false;const id=$('source-list').value;if(!id)throw new Error('请先选择已保存的来源');$('candidate-status').textContent='正在连接来源并验证签名…';candidate=await api('check','POST',{id,channel:$('channel').value});$('candidate-status').textContent=candidate.version+' · '+candidate.totalFiles+' 个清单文件 · '+(candidate.compatible?'符合本服 EXE/PVF 指纹':'兼容检查未通过：'+candidate.compatibilityError);$('files').replaceChildren();candidate.downloadable.forEach(f=>{const row=document.createElement('div');row.className='file';row.textContent=f.Path;const sub=document.createElement('span');sub.textContent=(f.Length/1048576).toFixed(2)+' MiB · '+(/^(client\/(dnf\.exe|script\.pvf|dstr\.dat)|client\/imagepacks2\/.+\.npk)$/i.test(f.Path)?'游戏内容候选':'排除：不属于游戏补丁');row.append(sub);$('files').append(row);});}));
$('confirmed').addEventListener('change',()=>{$('publish').disabled=!candidate?.compatible||!$('confirmed').checked;});
$('publish').addEventListener('click',guarded(async()=>{if(!candidate?.compatible||!$('confirmed').checked)throw new Error('请先完成兼容确认');await api('publish','POST',{candidateId:candidate.candidateId,confirmCompatibility:true});candidate=null;$('confirmed').checked=false;await load();message('已发布本服签名内容版本。玩家检查更新时才能看到该版本。');}));
$('news-list').addEventListener('change',()=>{const n=news[Number($('news-list').value)];$('news-title').value=n?.title||'';$('news-body').value=n?.body||'';$('news-category').value=n?.category||'综合';$('news-date').value=n?.date||'';});
$('save-news').addEventListener('click',guarded(async()=>{const n={title:$('news-title').value.trim(),body:$('news-body').value,category:$('news-category').value,date:$('news-date').value};if(!n.title)throw new Error('请填写公告标题');const next=[...news];const index=Number($('news-list').value);if(index>=0)next[index]=n;else next.push(n);await api('news','PUT',next);await load();message('公告已签名发布。');}));

load().catch(error=>{showLogin();if(error.message!=='请先验证 Admin Token')message(error.message);});

$('bundle-form').addEventListener('submit',guarded(async()=>{const file=$('bundle-file').files[0];if(!file)throw new Error('请选择完整 ZIP');message('正在上传并校验完整包…');const response=await fetch('/admin/api/bundle/'+encodeURIComponent($('bundle-version').value),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/zip','X-CSRF-Token':csrf},body:file});const result=await response.json();if(!response.ok)throw new Error(result.error||'发布失败');await load();message('登录器 '+result.version+' 已签名发布。');}));
