(() => {
  'use strict';
  const labels = {new:'待处理',reviewing:'核对中',planned:'已采纳',working:'处理中',resolved:'已解决',declined:'暂不采纳'};
  const types = {idea:'功能建议',problem:'问题反馈'};
  window.InfoGapFeedback = {
    async render({ user, api, post, esc, dateText, toast, token, currentToken, id }) {
      const main = document.getElementById('main');
      const valid = () => token === currentToken() && main.isConnected;
      const status = value => `<span class="fb-status fb-status-${Object.hasOwn(labels,value)?value:'new'}">${esc(labels[value]||value)}</span>`;
      const errorHTML = error => `<p class="form-error" role="alert">${esc(error.message||'暂时无法读取，请重试。')}</p>`;
      const safeID = value => Number.isSafeInteger(Number(value)) && Number(value)>0 ? String(Number(value)) : '';
      const description = user?.role==='admin' ? '查看成员的建议与问题，记录处理进展，也可以奖励有帮助的反馈。' : '遇到问题或有新想法，都可以写在这里。内容仅你和站长可见。';
      if (!user) { main.innerHTML='<h1 class="page-title">意见栏</h1><p class="page-description">登录后提交建议或问题，并查看站长回复。</p><button class="btn primary" data-auth="login">登录后使用</button>';return; }
      const isAdmin=user.role==='admin';
      const canWrite=!user.test_mode;
      const action = async (form, callback) => {
        const button=form.querySelector('button[type="submit"]');
        const errors=form.querySelector('[data-fb-error]');
        if(button?.disabled)return;
        if(errors)errors.textContent='';
        if(button)button.disabled=true;
        try{await callback();}catch(error){if(valid()&&errors)errors.textContent=error.message;}
        finally{if(button?.isConnected)button.disabled=false;}
      };
      if (id) {
        const feedbackID=safeID(id);
        if(!feedbackID){main.innerHTML='<h1 class="page-title">找不到这条反馈</h1><a class="btn" href="#feedback">返回意见栏</a>';return;}
        const loadDetail=async()=>{
          main.innerHTML='<div class="loading-state"><p>正在读取反馈…</p></div>';
          try {
            const data=await api('/api/feedback/'+feedbackID);if(!valid())return;
            const f=data.feedback||data;
            const replies=Array.isArray(data.replies)?data.replies:[];
            const rewarded=Number(f.reward_credits)||0;
            main.innerHTML=`<div class="fb-view"><a class="back-link" href="#feedback">← 返回意见栏</a><div class="fb-detail-heading"><div class="fb-meta"><span>${esc(types[f.type]||'反馈')}</span>${status(f.status)}<span>#${esc(f.id)}</span></div><h1 class="page-title">${esc(f.title)}</h1><p class="tiny-note">${esc(f.username||'成员')} · ${dateText(f.created_at,true)} · 仅提交者和站长可见</p></div><section class="panel fb-original"><div class="fb-plain">${esc(f.body)}</div></section>${rewarded?`<div class="callout fb-reward"><strong>已奖励 ${rewarded.toLocaleString('zh-CN')} 永久积分</strong><p>这条反馈的奖励已发放，不会重复发放，也不增加贡献度。</p></div>`:''}${isAdmin?`<section class="panel fb-admin-panel"><h2>处理这条反馈</h2><form id="fb-status-form" class="fb-inline-form"><div class="field"><label for="fb-status">处理状态</label><select id="fb-status" name="status">${Object.entries(labels).map(([value,label])=>`<option value="${value}" ${f.status===value?'selected':''}>${label}</option>`).join('')}</select></div><button class="btn" type="submit" ${canWrite?'':'disabled'}>更新状态</button><p class="form-error" data-fb-error role="alert"></p></form>${rewarded?'':`<form id="fb-reward-form" class="fb-reward-form"><div class="field"><label for="fb-reward-credits">奖励永久积分</label><input id="fb-reward-credits" name="credits" type="number" min="1" max="10000" step="1" value="100" required><p class="tiny-note">奖励直接进入提交者的永久积分，每条反馈只能发放一次，不增加贡献度。</p></div><label class="fb-confirm"><input name="confirmed" type="checkbox" required>确认按填写数量发放，本条反馈不能重复领奖。</label><button class="btn primary" type="submit" ${canWrite?'':'disabled'}>发放永久积分</button><p class="form-error" data-fb-error role="alert"></p></form>`}</section>`:''}<section class="panel fb-conversation"><h2>沟通记录 <span class="count-note">${replies.length} 条回复</span></h2><div class="fb-replies">${replies.length?replies.map(reply=>`<article class="fb-reply ${reply.is_admin?'fb-reply-admin':'fb-reply-member'}"><div class="fb-reply-meta"><strong>${reply.is_admin?'站长':esc(reply.username||'提交者')}</strong><time>${dateText(reply.created_at,true)}</time></div><div class="fb-plain">${esc(reply.body)}</div></article>`).join(''):'<p class="empty-inline">暂时没有回复。你可以在下面补充情况。</p>'}</div><form id="fb-reply-form"><div class="field"><label for="fb-reply-body">${isAdmin?'回复成员':'补充说明 / 回复站长'}</label><textarea id="fb-reply-body" name="body" rows="4" maxlength="3000" required placeholder="请写下需要补充的细节。" ${canWrite?'':'disabled'}></textarea></div><p class="form-error" data-fb-error role="alert"></p><button class="btn primary" type="submit" ${canWrite?'':'disabled'}>发送回复</button></form></section>${user.test_mode?'<p class="callout">当前为测试模式，只查看页面，不提交正式反馈或奖励。</p>':''}</div>`;
            document.getElementById('fb-reply-form').addEventListener('submit',event=>{
              event.preventDefault();const form=event.currentTarget;
              action(form,async()=>{const body=form.elements.body.value.trim();if(!body)throw new Error('请先填写回复内容。');await post('/api/feedback/'+feedbackID+'/replies',{body});if(valid()){toast('回复已发送');await loadDetail();}});
            });
            if(isAdmin){
              document.getElementById('fb-status-form').addEventListener('submit',event=>{
                event.preventDefault();const form=event.currentTarget;
                action(form,async()=>{await post('/api/admin/feedback/'+feedbackID,{status:form.elements.status.value});if(valid()){toast('处理状态已更新');await loadDetail();}});
              });
              document.getElementById('fb-reward-form')?.addEventListener('submit',event=>{
                event.preventDefault();const form=event.currentTarget;
                action(form,async()=>{const credits=Number(form.elements.credits.value);if(!Number.isInteger(credits)||credits<1||credits>10000)throw new Error('奖励积分请填写1～10000的整数。');if(!form.elements.confirmed.checked)throw new Error('请先确认奖励数量。');const result=await post('/api/admin/feedback/'+feedbackID+'/reward',{credits});if(valid()){toast(result.already_awarded?'该奖励此前已发放，本次未重复发放':'永久积分奖励已到账');await loadDetail();}});
              });
            }
          }catch(error){if(valid())main.innerHTML=`<a class="back-link" href="#feedback">← 返回意见栏</a><h1 class="page-title">暂时无法打开反馈</h1>${errorHTML(error)}<p class="tiny-note">你只能查看自己的反馈；站长可以处理全部反馈。</p>`;}
        };
        await loadDetail();return;
      }
      main.innerHTML=`<div class="fb-view"><div class="eyebrow">反馈与建议</div><h1 class="page-title">意见栏</h1><p class="page-description">${description}</p>${user.test_mode?'<div class="callout">测试模式不能提交正式反馈，请返回管理员或真实成员账号后再提交。</div>':''}<details class="panel fb-compose" ${isAdmin?'':'open'}><summary>＋ 提交建议或问题</summary><form id="fb-create-form"><div class="form-grid"><div class="field"><label for="fb-type">反馈类型</label><select name="type" id="fb-type"><option value="idea">功能建议</option><option value="problem">问题反馈</option></select></div><div class="field wide"><label for="fb-title">标题</label><input id="fb-title" name="title" minlength="2" maxlength="120" required placeholder="用2～120个字说明你想反馈什么" ${canWrite?'':'disabled'}></div><div class="field wide"><label for="fb-body">详细说明</label><textarea id="fb-body" name="body" minlength="10" maxlength="3000" rows="5" required placeholder="建议可以说明使用场景；问题请写清出现步骤、页面和提示信息。请勿填写密码、验证码等隐私信息。" ${canWrite?'':'disabled'}></textarea><p class="tiny-note">10～3000字，内容仅提交者和站长可见。有帮助的反馈可获站长奖励的永久积分。</p></div></div><p class="form-error" data-fb-error role="alert"></p><button class="btn primary" type="submit" ${canWrite?'':'disabled'}>提交反馈</button></form></details><section class="panel fb-list-panel"><div class="fb-list-heading"><h2>${isAdmin?'成员反馈':'我的反馈'}</h2><label for="fb-filter">状态 <select id="fb-filter"><option value="">全部状态</option>${Object.entries(labels).map(([value,label])=>`<option value="${value}">${label}</option>`).join('')}</select></label></div><div id="fb-list-results" aria-live="polite">正在读取…</div></section></div>`;
      let listRequest=0;
      const loadList=async(page=1)=>{
        const request=++listRequest;const results=document.getElementById('fb-list-results');results.textContent='正在读取…';
        try{const data=await api('/api/feedback?status='+encodeURIComponent(document.getElementById('fb-filter').value)+'&page='+page);if(!valid()||request!==listRequest)return;
          const items=Array.isArray(data.items)?data.items:[];
          results.innerHTML=items.length?items.map(f=>`<article class="fb-list-row"><div class="fb-meta"><span>${esc(types[f.type]||'反馈')}</span>${status(f.status)}<span>#${esc(f.id)}</span>${Number(f.reward_credits)>0?`<span class="fb-reward-badge">奖励 ${Number(f.reward_credits).toLocaleString('zh-CN')} 积分</span>`:''}</div><h3><a href="#feedback/${safeID(f.id)}">${esc(f.title)}</a></h3><p class="fb-excerpt">${esc(f.body||'')}</p><div class="fb-list-footer"><span>${isAdmin?esc(f.username||'成员')+' · ':''}${dateText(f.created_at,true)}</span><span>${Number(f.reply_count)||0} 条回复 · <a href="#feedback/${safeID(f.id)}">查看详情 →</a></span></div></article>`).join(''):'<p class="empty-inline">这个状态下暂无反馈。你可以提交一条新建议，或选择其他状态查看。</p>';
          if(Number(data.pages)>1){results.insertAdjacentHTML('beforeend',`<div class="pagination"><button class="btn small" id="fb-prev" ${data.page<=1?'disabled':''}>上一页</button><span>第 ${Number(data.page)} / ${Number(data.pages)} 页</span><button class="btn small" id="fb-next" ${data.page>=data.pages?'disabled':''}>下一页</button></div>`);document.getElementById('fb-prev').onclick=()=>loadList(data.page-1);document.getElementById('fb-next').onclick=()=>loadList(data.page+1);}
        }catch(error){if(valid()&&request===listRequest)results.innerHTML=errorHTML(error);}
      };
      document.getElementById('fb-filter').addEventListener('change',()=>loadList());
      document.getElementById('fb-create-form').addEventListener('submit',event=>{
        event.preventDefault();const form=event.currentTarget;
        action(form,async()=>{const title=form.elements.title.value.trim(),body=form.elements.body.value.trim();if([...title].length<2||[...title].length>120)throw new Error('标题请填写2～120个字。');if([...body].length<10||[...body].length>3000)throw new Error('详细说明请填写10～3000个字。');const result=await post('/api/feedback',{title,body,type:form.elements.type.value});if(valid()){toast('反馈已提交');const newID=safeID(result.feedback?.id||result.id);if(newID)location.hash='feedback/'+newID;else{form.reset();await loadList();}}});
      });
      await loadList();
    }
  };
})();
