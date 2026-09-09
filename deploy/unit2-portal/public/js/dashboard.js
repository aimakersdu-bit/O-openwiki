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

  let currentSessionId = 'sess_' + Date.now().toString(36) + '_' + Math.random().toString(36).substring(2, 7);

  async function sendQuestion() {
    const question = chatInput.value.trim();
    if (!question || !activeRepo) return;

    chatInput.value = '';
    appendMessage(question, 'user');

    const botContainer = appendMessage('', 'bot');
    const statusDiv = document.createElement('div');
    statusDiv.style.color = '#888';
    statusDiv.style.fontSize = '12px';
    statusDiv.style.marginBottom = '6px';
    statusDiv.textContent = '⏳ 正在思考中...';
    botContainer.appendChild(statusDiv);

    const contentDiv = document.createElement('div');
    contentDiv.className = 'bot-content-text';
    botContainer.appendChild(contentDiv);

    try {
      const response = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          repo_id: activeRepo.id,
          user_id: currentUser.user_id,
          session_id: currentSessionId,
          question: question
        })
      });

      if (!response.ok) {
        const errText = await response.text();
        statusDiv.style.display = 'none';
        contentDiv.textContent = '问答请求失败: ' + errText;
        return;
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder('utf-8');
      let buffer = '';
      let currentEvent = 'message';

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const lines = buffer.split('\n');
        buffer = lines.pop(); // retain partial line

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed) {
            currentEvent = 'message';
            continue;
          }

          if (trimmed.startsWith('event: ')) {
            currentEvent = trimmed.substring(7).trim();
            continue;
          }

          if (trimmed.startsWith('data: ')) {
            const rawData = trimmed.substring(6);
            let parsedData = null;
            try {
              parsedData = JSON.parse(rawData);
            } catch {
              parsedData = rawData;
            }

            if (currentEvent === 'status') {
              if (parsedData && typeof parsedData === 'object') {
                if (parsedData.stage === 'tool_start') {
                  statusDiv.textContent = `🔍 正在检索/调用: ${parsedData.name || '工具'}...`;
                  statusDiv.style.display = 'block';
                } else if (parsedData.stage === 'tool_end') {
                  statusDiv.textContent = `⚡ 检索完成，组织语言中...`;
                }
              }
            } else if (currentEvent === 'delta') {
              statusDiv.style.display = 'none';
              const textChunk = (parsedData && typeof parsedData === 'object' && parsedData.text !== undefined)
                ? parsedData.text
                : (typeof parsedData === 'string' ? parsedData : '');
              contentDiv.textContent += textChunk;
              chatMessages.scrollTop = chatMessages.scrollHeight;
            } else if (currentEvent === 'done') {
              statusDiv.style.display = 'none';
            } else if (currentEvent === 'error') {
              statusDiv.style.display = 'none';
              const errMsg = (parsedData && parsedData.error) || rawData;
              contentDiv.textContent += `\n[错误: ${errMsg}]`;
            } else {
              // Standard or legacy plaintext streaming fallback
              const textChunk = (parsedData && typeof parsedData === 'object' && parsedData.text !== undefined)
                ? parsedData.text
                : (typeof parsedData === 'string' ? parsedData : (rawData ? rawData + '\n' : ''));
              if (textChunk) {
                statusDiv.style.display = 'none';
                contentDiv.textContent += textChunk;
                chatMessages.scrollTop = chatMessages.scrollHeight;
              }
            }
          }
        }
      }
    } catch (err) {
      statusDiv.style.display = 'none';
      contentDiv.textContent += '\n[连接出错: ' + err.message + ']';
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
