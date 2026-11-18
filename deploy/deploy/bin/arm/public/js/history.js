document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  Auth.renderNavbarUser(currentUser);

  if (window.mermaid) {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
      securityLevel: 'loose'
    });
  }

  const viewReposBtn = document.getElementById('viewReposBtn');
  const viewProjectsBtn = document.getElementById('viewProjectsBtn');
  const targetSelect = document.getElementById('targetSelect');
  const queryBtn = document.getElementById('queryBtn');
  const historyContainer = document.getElementById('historyContainer');
  const auditSubtitle = document.getElementById('auditSubtitle');

  let currentMode = 'repo'; // 'repo' or 'project'
  let cachedRepos = [];
  let cachedProjects = [];

  async function loadData() {
    try {
      const [repos, projects] = await Promise.all([
        API.getRepos().catch(() => []),
        API.getProjects().catch(() => [])
      ]);
      cachedRepos = repos || [];
      cachedProjects = projects || [];
      renderSelectOptions();
    } catch (e) {
      console.warn('Failed to load repositories or projects for history:', e);
    }
  }

  function renderSelectOptions() {
    if (currentMode === 'repo') {
      if (auditSubtitle) auditSubtitle.textContent = '查看单代码仓库的历史提问记录与 AI 问答审计日志';
      if (cachedRepos.length > 0) {
        targetSelect.innerHTML = cachedRepos.map(r => `<option value="${r.id}">${API.escapeHTML(r.name)} (${r.id})</option>`).join('');
        queryHistory(cachedRepos[0].id, false);
      } else {
        targetSelect.innerHTML = '<option value="">暂无可用仓库</option>';
        historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">暂无可用仓库</p>';
      }
    } else {
      if (auditSubtitle) auditSubtitle.textContent = '查看跨多微服务协同项目的历史提问记录与 AI 问答审计日志';
      if (cachedProjects.length > 0) {
        targetSelect.innerHTML = cachedProjects.map(p => `<option value="${p.id}">${API.escapeHTML(p.name)} (${p.id})</option>`).join('');
        queryHistory(cachedProjects[0].id, true);
      } else {
        targetSelect.innerHTML = '<option value="">暂无微服务项目</option>';
        historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">暂无微服务项目，可前往【仓库运维与构建】编排创建。</p>';
      }
    }
  }

  function renderMermaidBlocks(container) {
    if (!window.mermaid) return;
    const mermaidNodes = container.querySelectorAll('.mermaid');
    if (mermaidNodes.length > 0) {
      try {
        mermaid.run({ nodes: mermaidNodes });
      } catch (err) {
        console.warn('Mermaid rendering failed:', err);
      }
    }
  }

  async function queryHistory(targetId, isProject) {
    if (!targetId) {
      historyContainer.innerHTML = '<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">请选择代码仓库或微服务项目</p>';
      return;
    }

    const typeLabel = isProject ? '微服务项目' : '代码仓库';
    historyContainer.innerHTML = `<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">加载 ${typeLabel} 问答审计日志中...</p>`;

    try {
      let sessions = await API.getQASessions(targetId, isProject);
      if (!Array.isArray(sessions)) {
        if (sessions && Array.isArray(sessions.sessions)) {
          sessions = sessions.sessions;
        } else {
          sessions = [];
        }
      }
      if (sessions.length === 0) {
        historyContainer.innerHTML = `<p style="color: var(--text-secondary); text-align: center; padding: 2rem;">该${typeLabel}暂无历史问答记录。</p>`;
        return;
      }

      historyContainer.innerHTML = sessions.map(sess => {
        const modeBadge = isProject
          ? `<span class="badge" style="background: rgba(139, 92, 246, 0.2); color: #c084fc; border: 1px solid rgba(139, 92, 246, 0.4); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem;">🏢 微服务项目: ${API.escapeHTML(sess.repo_id || targetId)}</span>`
          : `<span class="badge" style="background: rgba(59, 130, 246, 0.2); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.4); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem;">📦 仓库: ${API.escapeHTML(sess.repo_id || targetId)}</span>`;

        return `
          <div style="background:#0f172a; border:1px solid var(--border-color); border-radius:10px; padding:1.25rem; margin-bottom:1rem; box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.2);">
            <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:0.75rem; font-size:0.85rem; color:var(--text-secondary); flex-wrap:wrap; gap:0.5rem;">
              <div style="display:flex; align-items:center; gap:0.5rem;">
                ${modeBadge}
                <span>提问用户: <strong style="color:var(--text-primary);">${API.escapeHTML(sess.user_id)}</strong></span>
              </div>
              <span>提问时间: ${API.formatDate(sess.created_at)}</span>
            </div>
            <div style="font-weight:600; color:var(--accent-color); margin-bottom:0.75rem; font-size: 1rem;">
              ❓ 问题: ${API.escapeHTML(sess.question)}
            </div>
            <div class="bot-content-text" style="background:#1e293b; border-radius:8px; padding:1rem; font-size:0.95rem; color:var(--text-primary); max-height:480px; overflow-y:auto; line-height:1.6;">
              ${API.renderMarkdown(sess.answer || '无回答内容')}
            </div>
          </div>
        `;
      }).join('');

      renderMermaidBlocks(historyContainer);
    } catch (err) {
      historyContainer.innerHTML = `<p style="color: var(--error-color); text-align: center; padding: 2rem;">查询日志失败: ${err.message}</p>`;
    }
  }

  viewReposBtn.addEventListener('click', () => {
    if (currentMode === 'repo') return;
    currentMode = 'repo';
    viewReposBtn.classList.add('active');
    viewProjectsBtn.classList.remove('active');
    renderSelectOptions();
  });

  viewProjectsBtn.addEventListener('click', () => {
    if (currentMode === 'project') return;
    currentMode = 'project';
    viewProjectsBtn.classList.add('active');
    viewReposBtn.classList.remove('active');
    renderSelectOptions();
  });

  queryBtn.addEventListener('click', () => queryHistory(targetSelect.value, currentMode === 'project'));
  targetSelect.addEventListener('change', () => queryHistory(targetSelect.value, currentMode === 'project'));

  loadData();
});
