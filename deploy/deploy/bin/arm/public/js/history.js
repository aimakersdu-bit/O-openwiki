document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  Auth.renderNavbarUser(currentUser);

  const repoSelect = document.getElementById('repoSelect');
  const queryBtn = document.getElementById('queryBtn');
  const historyContainer = document.getElementById('historyContainer');

  // Load repos into dropdown select
  async function initRepoSelect() {
    try {
      const repos = await API.getRepos();
      if (repos && repos.length > 0) {
        repoSelect.innerHTML = repos.map(r => `<option value="${r.id}">${API.escapeHTML(r.name)} (${r.id})</option>`).join('');
        // Automatically query first repo
        queryHistory(repos[0].id);
      } else {
        repoSelect.innerHTML = '<option value="">暂无可用仓库</option>';
      }
    } catch (e) {
      console.warn('Failed to load repo list for history:', e);
    }
  }

  async function queryHistory(repoId) {
    if (!repoId) {
      historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">请选择代码仓库</p>';
      return;
    }

    historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">加载问答审计日志中...</p>';
    try {
      const sessions = await API.getQASessions(repoId);
      if (!sessions || sessions.length === 0) {
        historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">该仓库暂无历史问答记录。</p>';
        return;
      }

      historyContainer.innerHTML = sessions.map(sess => `
        <div style="background:#0f172a; border:1px solid var(--border-color); border-radius:10px; padding:1.25rem; margin-bottom:1rem;">
          <div style="display:flex; justify-content:space-between; margin-bottom:0.75rem; font-size:0.85rem; color:var(--text-secondary);">
            <span>提问用户: <strong style="color:var(--text-primary);">${API.escapeHTML(sess.user_id)}</strong></span>
            <span>提问时间: ${API.formatDate(sess.created_at)}</span>
          </div>
          <div style="font-weight:600; color:var(--accent-color); margin-bottom:0.75rem;">
            ❓ 问题: ${API.escapeHTML(sess.question)}
          </div>
          <div class="bot-content-text" style="background:#1e293b; border-radius:8px; padding:1rem; font-size:0.95rem; color:var(--text-primary); max-height:360px; overflow-y:auto;">
            ${API.renderMarkdown(sess.answer || '无回答内容')}
          </div>
        </div>
      `).join('');
    } catch (err) {
      historyContainer.innerHTML = `<p style="color: var(--error-color); text-align: center; padding: 2rem;">查询日志失败: ${err.message}</p>`;
    }
  }

  queryBtn.addEventListener('click', () => queryHistory(repoSelect.value));
  repoSelect.addEventListener('change', () => queryHistory(repoSelect.value));

  initRepoSelect();
});
