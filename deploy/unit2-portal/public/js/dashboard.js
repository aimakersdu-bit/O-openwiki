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

      // Fetch latest build status for each repo to filter only successfully built repos
      const buildPromises = repos.map(r => API.getBuildHistory(r.id).catch(() => []));
      const buildsList = await Promise.all(buildPromises);

      const successfulRepos = repos.filter((repo, i) => {
        const builds = buildsList[i] || [];
        return builds.length > 0 && builds[0].status === 'success';
      });

      if (successfulRepos.length === 0) {
        repoList.innerHTML = `
          <div class="card" style="grid-column: 1 / -1; text-align: center; padding: 3rem;">
            <p style="color: var(--text-secondary); margin-bottom: 1rem;">暂无已完成构建的 Wiki 仓库。</p>
            <a href="admin.html" class="btn btn-primary">⚡ 前往【仓库运维与构建】发起构建</a>
          </div>
        `;
        return;
      }

      repoList.innerHTML = successfulRepos.map(repo => `
        <div class="repo-card">
          <div>
            <div class="repo-title">${API.escapeHTML(repo.name)} <span class="badge">${API.escapeHTML(repo.branch)}</span></div>
            <div class="repo-meta">
              <div>仓库标识: <code>${API.escapeHTML(repo.id)}</code></div>
              <div>状态: <span style="color: var(--success-color);">✅ 已构建完成</span></div>
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

  // Chat Drawer Resizer Drag Logic
  const chatResizer = document.getElementById('chatResizer');
  let isResizing = false;

  // Restore saved width from localStorage
  const savedWidth = localStorage.getItem('chatDrawerWidth');
  if (savedWidth) {
    const parsedW = parseInt(savedWidth, 10);
    if (!isNaN(parsedW)) {
      const clampedW = Math.min(Math.max(parsedW, 360), Math.floor(window.innerWidth * 0.85));
      chatDrawer.style.width = clampedW + 'px';
    }
  }

  if (chatResizer) {
    chatResizer.addEventListener('mousedown', (e) => {
      e.preventDefault();
      isResizing = true;
      chatResizer.classList.add('dragging');
      document.body.style.cursor = 'ew-resize';
      document.body.style.userSelect = 'none';

      const startX = e.clientX;
      const startWidth = chatDrawer.offsetWidth;

      function onMouseMove(moveEvent) {
        if (!isResizing) return;
        const deltaX = startX - moveEvent.clientX; // Dragging left increases drawer width
        let newWidth = startWidth + deltaX;
        const minW = 360;
        const maxW = Math.floor(window.innerWidth * 0.85);

        if (newWidth < minW) newWidth = minW;
        if (newWidth > maxW) newWidth = maxW;

        chatDrawer.style.width = newWidth + 'px';
      }

      function onMouseUp() {
        if (isResizing) {
          isResizing = false;
          chatResizer.classList.remove('dragging');
          document.body.style.cursor = '';
          document.body.style.userSelect = '';
          localStorage.setItem('chatDrawerWidth', chatDrawer.offsetWidth);
          window.removeEventListener('mousemove', onMouseMove);
          window.removeEventListener('mouseup', onMouseUp);
        }
      }

      window.addEventListener('mousemove', onMouseMove);
      window.addEventListener('mouseup', onMouseUp);
    });
  }

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

      let fullMarkdown = '';
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
              fullMarkdown += textChunk;
              contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
              chatMessages.scrollTop = chatMessages.scrollHeight;
            } else if (currentEvent === 'done') {
              statusDiv.style.display = 'none';
              contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
            } else if (currentEvent === 'error') {
              statusDiv.style.display = 'none';
              const errMsg = (parsedData && parsedData.error) || rawData;
              fullMarkdown += `\n\n**[错误: ${errMsg}]**`;
              contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
            } else {
              // Standard or legacy plaintext streaming fallback
              const textChunk = (parsedData && typeof parsedData === 'object' && parsedData.text !== undefined)
                ? parsedData.text
                : (typeof parsedData === 'string' ? parsedData : (rawData ? rawData + '\n' : ''));
              if (textChunk) {
                statusDiv.style.display = 'none';
                fullMarkdown += textChunk;
                contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
                chatMessages.scrollTop = chatMessages.scrollHeight;
              }
            }
          }
        }
      }
    } catch (err) {
      statusDiv.style.display = 'none';
      fullMarkdown += '\n\n**[连接出错: ' + err.message + ']**';
      contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
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
