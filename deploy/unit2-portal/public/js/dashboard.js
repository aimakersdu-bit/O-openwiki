document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  Auth.renderNavbarUser(currentUser);

  const repoList = document.getElementById('repoList');
  const refreshBtn = document.getElementById('refreshBtn');

  // Chat Drawer Elements
  const chatDrawer = document.getElementById('chatDrawer');
  const closeChatBtn = document.getElementById('closeChatBtn');
  const chatRepoTitle = document.getElementById('chatRepoTitle');
  const chatRepoBadge = document.getElementById('chatRepoBadge');
  const chatMessages = document.getElementById('chatMessages');
  const chatInput = document.getElementById('chatInput');
  const sendChatBtn = document.getElementById('sendChatBtn');

  let activeRepo = null;

  // Load Repos List
  async function loadRepos() {
    repoList.innerHTML = '<p style="color: var(--text-secondary);">加载仓库列表中...</p>';
    try {
      const repos = await API.getRepos();
      if (!repos || repos.length === 0) {
        repoList.innerHTML = `
          <div class="card" style="grid-column: 1 / -1; text-align: center; padding: 3rem;">
            <p style="color: var(--text-secondary); margin-bottom: 1rem;">暂无已注册的代码仓库。</p>
            <a href="admin.html" class="btn btn-primary">➕ 前往仓库运维管理注册新仓库</a>
          </div>
        `;
        return;
      }

      repoList.innerHTML = repos.map(repo => `
        <div class="repo-card">
          <div>
            <div class="repo-title">${API.escapeHTML(repo.name)} <span class="badge">${API.escapeHTML(repo.branch)}</span></div>
            <div class="repo-meta">
              <div>Git: <code>${API.escapeHTML(repo.git_url)}</code></div>
              <div>状态: <span style="color: ${repo.status === 'active' ? 'var(--success-color)' : 'var(--error-color)'}">${API.escapeHTML(repo.status)}</span></div>
            </div>
          </div>
          <div class="repo-actions">
            <!-- Nginx Static Wiki Link -->
            <a href="${repo.wiki_url}" target="_blank" class="btn btn-primary btn-sm">
              📖 查看 Wiki
            </a>
            <!-- QA Chat Drawer -->
            <button class="btn btn-secondary btn-sm chat-btn" data-id="${repo.id}" data-name="${API.escapeHTML(repo.name)}">
              💬 AI 问答
            </button>
          </div>
        </div>
      `).join('');

      // Attach click listeners to chat buttons
      document.querySelectorAll('.chat-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const repoId = e.currentTarget.getAttribute('data-id');
          const repoName = e.currentTarget.getAttribute('data-name');
          openChat(repoId, repoName);
        });
      });
    } catch (err) {
      repoList.innerHTML = `<p style="color: var(--error-color);">加载仓库列表失败: ${err.message}</p>`;
    }
  }

  refreshBtn.addEventListener('click', loadRepos);
  loadRepos();

  // Chat Drawer Logic
  function openChat(repoId, repoName) {
    activeRepo = { id: repoId, name: repoName };
    chatRepoTitle.textContent = repoName;
    chatRepoBadge.textContent = repoId;
    chatDrawer.classList.add('open');
  }

  closeChatBtn.addEventListener('click', () => {
    chatDrawer.classList.remove('open');
  });

  sendChatBtn.addEventListener('click', sendQuestion);
  chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.ctrlKey) {
      e.preventDefault();
      sendQuestion();
    }
  });

  async function sendQuestion() {
    const question = chatInput.value.trim();
    if (!question || !activeRepo) return;

    chatInput.value = '';
    appendMessage(question, 'user');

    const botMsgDiv = appendMessage('正在思考中...', 'bot');

    try {
      const response = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          repo_id: activeRepo.id,
          user_id: currentUser.user_id,
          question: question
        })
      });

      if (!response.ok) {
        const errText = await response.text();
        botMsgDiv.textContent = '问答请求失败: ' + errText;
        return;
      }

      botMsgDiv.textContent = '';
      const reader = response.body.getReader();
      const decoder = new TextDecoder('utf-8');

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const chunk = decoder.decode(value, { stream: true });

        const lines = chunk.split('\n');
        for (const line of lines) {
          if (line.startsWith('data: ')) {
            const content = line.substring(6);
            botMsgDiv.textContent += content + '\n';
            chatMessages.scrollTop = chatMessages.scrollHeight;
          }
        }
      }
    } catch (err) {
      botMsgDiv.textContent += '\n[连接出错: ' + err.message + ']';
    }
  }

  function appendMessage(text, type) {
    const div = document.createElement('div');
    div.className = `msg msg-${type}`;
    div.textContent = text;
    chatMessages.appendChild(div);
    chatMessages.scrollTop = chatMessages.scrollHeight;
    return div;
  }
});
