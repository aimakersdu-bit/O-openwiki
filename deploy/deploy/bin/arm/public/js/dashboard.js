document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  Auth.renderNavbarUser(currentUser);

  const repoList = document.getElementById('repoList');
  const refreshBtn = document.getElementById('refreshBtn');
  const repoSearchInput = document.getElementById('repoSearchInput');
  const clearSearchBtn = document.getElementById('clearSearchBtn');
  const statusFilter = document.getElementById('statusFilter');
  const pageSizeSelect = document.getElementById('pageSizeSelect');
  const repoCountStats = document.getElementById('repoCountStats');
  const paginationBar = document.getElementById('paginationBar');

  // Chat Drawer Elements
  const chatDrawer = document.getElementById('chatDrawer');
  const closeChatBtn = document.getElementById('closeChatBtn');
  const newSessionBtn = document.getElementById('newSessionBtn');
  const toggleHistoryBtn = document.getElementById('toggleHistoryBtn');
  const sessionHistoryPanel = document.getElementById('sessionHistoryPanel');
  const closeHistoryBtn = document.getElementById('closeHistoryBtn');
  const sessionList = document.getElementById('sessionList');
  const chatRepoTitle = document.getElementById('chatRepoTitle');
  const chatRepoBadge = document.getElementById('chatRepoBadge');
  const chatMessages = document.getElementById('chatMessages');
  const chatInput = document.getElementById('chatInput');
  const sendChatBtn = document.getElementById('sendChatBtn');

  let activeRepo = null;

  // Dashboard Repos State
  let allRepos = [];
  let filteredRepos = [];
  let currentPage = 1;
  let pageSize = 9;
  const buildStatusCache = new Map(); // repoId -> statusObject
  let searchQuery = '';
  let selectedStatus = 'all';
  let searchDebounceTimer = null;

  // Helper to parse build list into display status
  function parseBuildStatus(repo, builds = []) {
    const latestBuild = builds[0];
    const hasSuccess = builds.some(b => b.status === 'success' || b.status === 'skipped');
    const isBuilding = latestBuild && (latestBuild.status === 'pending' || latestBuild.status === 'running');

    let statusKey = 'unbuilt'; // 'ready', 'building', 'unbuilt'
    let statusHtml = '';

    if (isBuilding) {
      statusKey = 'building';
      statusHtml = `<span style="color: var(--accent-color);">⚡ 增量构建中 (Build #${latestBuild.id})...</span>`;
    } else if (latestBuild && latestBuild.status === 'success') {
      statusKey = 'ready';
      statusHtml = `<span style="color: var(--success-color);">✅ 已构建完成</span>`;
    } else if (latestBuild && latestBuild.status === 'skipped') {
      statusKey = 'ready';
      statusHtml = `<span style="color: var(--success-color);" title="定时检测无新提交，Wiki 为最新版本">✅ 已构建完成 (无代码更新)</span>`;
    } else if (latestBuild && latestBuild.status === 'failed') {
      if (hasSuccess) {
        statusKey = 'ready';
        statusHtml = `<span style="color: #f59e0b;">⚠️ 最新构建失败 (保留历史Wiki)</span>`;
      } else {
        statusKey = 'unbuilt';
        statusHtml = `<span style="color: var(--error-color, #ef4444);">❌ 构建失败</span>`;
      }
    } else if (hasSuccess) {
      statusKey = 'ready';
      statusHtml = `<span style="color: var(--success-color);">✅ 已构建完成</span>`;
    } else {
      statusKey = 'unbuilt';
      statusHtml = `<span style="color: var(--text-secondary);">⏳ 尚未构建</span>`;
    }

    const wikiActionHtml = hasSuccess
      ? `<a href="${repo.wiki_url}" target="_blank" class="btn btn-primary btn-sm">📖 查看 Wiki</a>`
      : `<button class="btn btn-primary btn-sm" disabled style="opacity: 0.5; cursor: not-allowed;" title="仓库尚未生成 Wiki 页面">📖 尚未构建</button>`;

    const chatActionHtml = hasSuccess
      ? `<button class="btn btn-secondary btn-sm chat-btn" data-id="${repo.id}" data-name="${API.escapeHTML(repo.name)}">💬 AI 问答</button>`
      : `<button class="btn btn-secondary btn-sm" disabled style="opacity: 0.5; cursor: not-allowed;" title="知识库尚未就绪，请先完成构建">💬 AI 问答</button>`;

    return {
      statusKey,
      hasSuccess,
      isBuilding,
      latestBuild,
      statusHtml,
      wikiActionHtml,
      chatActionHtml
    };
  }

  // Render pagination bar
  function renderPaginationBar(totalPages, page) {
    if (!paginationBar) return;
    if (totalPages <= 1 || pageSize === 'all') {
      paginationBar.style.display = 'none';
      paginationBar.innerHTML = '';
      return;
    }

    paginationBar.style.display = 'flex';
    let html = '';

    // Previous button
    html += `<button class="page-btn" ${page <= 1 ? 'disabled' : ''} data-page="${page - 1}">« 上一页</button>`;

    // Page numbers
    const pagesToShow = [];
    if (totalPages <= 7) {
      for (let i = 1; i <= totalPages; i++) pagesToShow.push(i);
    } else {
      pagesToShow.push(1);
      if (page > 3) pagesToShow.push('...');
      
      const start = Math.max(2, page - 1);
      const end = Math.min(totalPages - 1, page + 1);
      for (let i = start; i <= end; i++) {
        if (!pagesToShow.includes(i)) pagesToShow.push(i);
      }

      if (page < totalPages - 2) pagesToShow.push('...');
      if (!pagesToShow.includes(totalPages)) pagesToShow.push(totalPages);
    }

    pagesToShow.forEach(p => {
      if (p === '...') {
        html += `<span class="page-ellipsis">...</span>`;
      } else {
        html += `<button class="page-btn ${p === page ? 'active' : ''}" data-page="${p}">${p}</button>`;
      }
    });

    // Next button
    html += `<button class="page-btn" ${page >= totalPages ? 'disabled' : ''} data-page="${page + 1}">下一页 »</button>`;

    paginationBar.innerHTML = html;

    // Attach listeners
    paginationBar.querySelectorAll('.page-btn:not(:disabled):not(.active)').forEach(btn => {
      btn.addEventListener('click', () => {
        const targetPage = parseInt(btn.getAttribute('data-page'), 10);
        if (!isNaN(targetPage)) {
          currentPage = targetPage;
          renderCurrentPage();
          repoList.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }
      });
    });
  }

  // Render currently active page of repos
  async function renderCurrentPage() {
    const totalItems = filteredRepos.length;
    const size = (pageSize === 'all') ? (totalItems || 1) : parseInt(pageSize, 10);
    const totalPages = Math.ceil(totalItems / size) || 1;

    if (currentPage > totalPages) currentPage = totalPages;
    if (currentPage < 1) currentPage = 1;

    // Update stats label
    if (repoCountStats) {
      if (allRepos.length === 0) {
        repoCountStats.textContent = '共 0 个仓库';
      } else if (filteredRepos.length === allRepos.length) {
        repoCountStats.textContent = `共 ${allRepos.length} 个仓库`;
      } else {
        repoCountStats.textContent = `匹配 ${filteredRepos.length} / 共 ${allRepos.length} 个`;
      }
    }

    // Handle empty search / filter results
    if (totalItems === 0) {
      repoList.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">🔍</div>
          <div class="empty-state-text">未找到符合条件的仓库</div>
          <div class="empty-state-desc">请尝试调整搜索关键词或重置构建状态过滤器</div>
        </div>
      `;
      renderPaginationBar(0, 1);
      return;
    }

    // Slice items for current page
    const startIndex = (currentPage - 1) * size;
    const endIndex = (pageSize === 'all') ? totalItems : Math.min(startIndex + size, totalItems);
    const pageRepos = filteredRepos.slice(startIndex, endIndex);

    // Concurrently fetch build status ONLY for page items that are not in cache
    const uncachedPageRepos = pageRepos.filter(r => !buildStatusCache.has(r.id));
    if (uncachedPageRepos.length > 0) {
      const promises = uncachedPageRepos.map(r => 
        API.getBuildHistory(r.id)
          .then(builds => buildStatusCache.set(r.id, parseBuildStatus(r, builds)))
          .catch(() => buildStatusCache.set(r.id, parseBuildStatus(r, [])))
      );
      await Promise.all(promises);
    }

    // Render cards HTML
    repoList.innerHTML = pageRepos.map(repo => {
      const status = buildStatusCache.get(repo.id) || parseBuildStatus(repo, []);
      return `
        <div class="repo-card">
          <div>
            <div class="repo-title">${API.escapeHTML(repo.name)} <span class="badge">${API.escapeHTML(repo.branch)}</span></div>
            <div class="repo-meta">
              <div>仓库标识: <code>${API.escapeHTML(repo.id)}</code></div>
              <div>状态: ${status.statusHtml}</div>
            </div>
          </div>
          <div class="repo-actions">
            ${status.wikiActionHtml}
            ${status.chatActionHtml}
          </div>
        </div>
      `;
    }).join('');

    // Attach click listeners to chat buttons on current page
    repoList.querySelectorAll('.chat-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        const repoName = e.currentTarget.getAttribute('data-name');
        openChat(repoId, repoName);
      });
    });

    // Render pagination bar
    renderPaginationBar(totalPages, currentPage);
  }

  // Filter repos according to search and status selector
  async function applyFiltersAndRender(resetPage = false) {
    if (resetPage) {
      currentPage = 1;
    }

    // If user filtered by a specific build status, ensure all repos have their status cached
    if (selectedStatus !== 'all') {
      const uncachedRepos = allRepos.filter(r => !buildStatusCache.has(r.id));
      if (uncachedRepos.length > 0) {
        if (repoCountStats) repoCountStats.textContent = '正在检测构建状态...';
        const promises = uncachedRepos.map(r => 
          API.getBuildHistory(r.id)
            .then(builds => buildStatusCache.set(r.id, parseBuildStatus(r, builds)))
            .catch(() => buildStatusCache.set(r.id, parseBuildStatus(r, [])))
        );
        await Promise.all(promises);
      }
    }

    const query = searchQuery.trim().toLowerCase();
    filteredRepos = allRepos.filter(repo => {
      // 1. Keyword search (name or id)
      const matchesSearch = !query || 
        (repo.name && repo.name.toLowerCase().includes(query)) ||
        (repo.id && repo.id.toLowerCase().includes(query));
      if (!matchesSearch) return false;

      // 2. Status filter
      if (selectedStatus === 'all') return true;
      const status = buildStatusCache.get(repo.id);
      if (!status) return true;
      return status.statusKey === selectedStatus;
    });

    await renderCurrentPage();
  }

  // Background lazy-prefetch remaining repos' status when idle
  function prefetchRemainingStatus() {
    const uncachedRepos = allRepos.filter(r => !buildStatusCache.has(r.id));
    if (uncachedRepos.length === 0) return;

    // Low-concurrency background fetch (batch size 3)
    let index = 0;
    function fetchNextBatch() {
      if (index >= uncachedRepos.length) return;
      const batch = uncachedRepos.slice(index, index + 3);
      index += 3;
      Promise.all(batch.map(r => 
        API.getBuildHistory(r.id)
          .then(builds => buildStatusCache.set(r.id, parseBuildStatus(r, builds)))
          .catch(() => buildStatusCache.set(r.id, parseBuildStatus(r, [])))
      )).then(() => {
        if (window.requestIdleCallback) {
          window.requestIdleCallback(fetchNextBatch, { timeout: 2000 });
        } else {
          setTimeout(fetchNextBatch, 200);
        }
      });
    }

    if (window.requestIdleCallback) {
      window.requestIdleCallback(fetchNextBatch, { timeout: 1500 });
    } else {
      setTimeout(fetchNextBatch, 500);
    }
  }

  // Load Repos List
  async function loadRepos() {
    repoList.innerHTML = '<p style="color: var(--text-secondary);">加载仓库列表中...</p>';
    if (repoCountStats) repoCountStats.textContent = '加载中...';
    try {
      allRepos = (await API.getRepos()) || [];
      if (allRepos.length === 0) {
        repoList.innerHTML = `
          <div class="card" style="grid-column: 1 / -1; text-align: center; padding: 3rem;">
            <p style="color: var(--text-secondary); margin-bottom: 1rem;">暂无已注册的代码仓库。</p>
            <a href="admin.html" class="btn btn-primary">➕ 前往仓库运维管理注册新仓库</a>
          </div>
        `;
        if (repoCountStats) repoCountStats.textContent = '共 0 个仓库';
        if (paginationBar) paginationBar.style.display = 'none';
        return;
      }

      await applyFiltersAndRender(false);
      // Trigger background prefetch for remaining repos
      prefetchRemainingStatus();
    } catch (err) {
      repoList.innerHTML = `<p style="color: var(--error-color);">加载仓库列表失败: ${err.message}</p>`;
      if (repoCountStats) repoCountStats.textContent = '加载失败';
    }
  }

  // Toolbar Event Listeners
  if (repoSearchInput) {
    repoSearchInput.addEventListener('input', (e) => {
      const val = e.target.value;
      if (clearSearchBtn) {
        clearSearchBtn.style.display = val ? 'flex' : 'none';
      }
      clearTimeout(searchDebounceTimer);
      searchDebounceTimer = setTimeout(() => {
        searchQuery = val;
        applyFiltersAndRender(true);
      }, 200);
    });
  }

  if (clearSearchBtn) {
    clearSearchBtn.addEventListener('click', () => {
      if (repoSearchInput) {
        repoSearchInput.value = '';
        repoSearchInput.focus();
      }
      clearSearchBtn.style.display = 'none';
      searchQuery = '';
      applyFiltersAndRender(true);
    });
  }

  if (statusFilter) {
    statusFilter.addEventListener('change', (e) => {
      selectedStatus = e.target.value;
      applyFiltersAndRender(true);
    });
  }

  if (pageSizeSelect) {
    pageSizeSelect.addEventListener('change', (e) => {
      pageSize = e.target.value;
      applyFiltersAndRender(true);
    });
  }

  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => {
      buildStatusCache.clear();
      loadRepos();
    });
  }

  // Initial load
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
  function generateSessionId() {
    return 'sess_' + Date.now().toString(36) + '_' + Math.random().toString(36).substring(2, 7);
  }

  let currentSessionId = generateSessionId();

  function resetChatMessages() {
    chatMessages.innerHTML = `
      <div class="chat-welcome">
        <p>🤖 您正在针对该仓库进行 百信 RepoWiki 智能问答。</p>
        <p>示例问题：</p>
        <ul>
          <li>“这个项目的核心模块架构是怎样的？”</li>
          <li>“如何在本地方便地启动调试此服务？”</li>
        </ul>
      </div>
    `;
  }

  function startNewSession() {
    currentSessionId = generateSessionId();
    resetChatMessages();
    if (sessionHistoryPanel) sessionHistoryPanel.style.display = 'none';
    if (activeRepo) {
      loadSessionHistory();
    }
  }

  async function openChat(repoId, repoName) {
    const isNewRepo = (!activeRepo || activeRepo.id !== repoId);
    activeRepo = { id: repoId, name: repoName };
    chatRepoTitle.textContent = repoName;
    chatRepoBadge.textContent = repoId;
    chatDrawer.classList.add('open');

    if (isNewRepo) {
      startNewSession();
    }
  }

  closeChatBtn.addEventListener('click', () => {
    chatDrawer.classList.remove('open');
  });

  if (newSessionBtn) {
    newSessionBtn.addEventListener('click', () => {
      startNewSession();
    });
  }

  if (toggleHistoryBtn) {
    toggleHistoryBtn.addEventListener('click', () => {
      if (sessionHistoryPanel.style.display === 'none' || !sessionHistoryPanel.style.display) {
        sessionHistoryPanel.style.display = 'flex';
        loadSessionHistory();
      } else {
        sessionHistoryPanel.style.display = 'none';
      }
    });
  }

  if (closeHistoryBtn) {
    closeHistoryBtn.addEventListener('click', () => {
      sessionHistoryPanel.style.display = 'none';
    });
  }

  // Initialize mermaid if available
  if (typeof window.mermaid !== 'undefined' && typeof window.mermaid.initialize === 'function') {
    try {
      window.mermaid.initialize({
        startOnLoad: false,
        theme: 'dark',
        securityLevel: 'loose',
        suppressErrorRendering: true
      });
    } catch (e) {
      console.warn('mermaid init error:', e);
    }
  }

  let mermaidCounter = 0;
  async function renderMermaidInElement(container) {
    if (typeof window.mermaid === 'undefined' || !container) return;
    const blocks = container.querySelectorAll('code.language-mermaid');
    blocks.forEach((code) => {
      const pre = document.createElement('pre');
      pre.className = 'mermaid';
      pre.textContent = code.textContent;
      code.closest('pre')?.replaceWith(pre);
    });

    const nodes = container.querySelectorAll('.mermaid:not([data-processed="true"])');
    for (let i = 0; i < nodes.length; i++) {
      const node = nodes[i];
      node.setAttribute('data-processed', 'true');
      const rawText = (node.textContent || '').trim();
      if (!rawText) continue;

      // 预清洗常见格式瑕疵与安全占位符
      let cleaned = rawText
        .replace(/\/\/\s*\[安全策略.*\]/g, '')
        .replace(/\.\.\.\s*\[代码过长.*\]\s*\.\.\./g, '')
        .trim();

      const renderId = 'mermaid_' + Date.now() + '_' + (++mermaidCounter);
      try {
        if (typeof window.mermaid.render === 'function') {
          const res = await window.mermaid.render(renderId, cleaned);
          const svg = (typeof res === 'object' && res.svg) ? res.svg : res;
          node.innerHTML = svg;
          node.classList.add('mermaid-rendered');
        } else if (typeof window.mermaid.run === 'function') {
          node.textContent = cleaned;
          await window.mermaid.run({ nodes: [node] });
        } else if (typeof window.mermaid.init === 'function') {
          node.textContent = cleaned;
          window.mermaid.init(undefined, [node]);
        }
      } catch (err) {
        console.warn('Mermaid isolated render failed, falling back to styled code:', err);
        // 清理 mermaid 错误 DOM 产生在 body 下的残留节点
        const errorEl = document.getElementById('d' + renderId);
        if (errorEl) errorEl.remove();

        const fallback = document.createElement('div');
        fallback.className = 'mermaid-fallback-box';
        fallback.style.margin = '0.5rem 0';
        fallback.innerHTML = `
          <div style="font-size:0.75rem; color:var(--text-secondary); margin-bottom:0.25rem;">
            📊 <em>[Mermaid 流程图代码展示]</em>
          </div>
          <pre style="margin:0; background:rgba(0,0,0,0.3); padding:0.6rem; border-radius:6px; font-size:0.8rem; overflow-x:auto; border:1px solid rgba(255,255,255,0.1);"><code>${API.escapeHTML(cleaned)}</code></pre>
        `;
        node.replaceWith(fallback);
      }
    }
  }

  async function loadSessionHistory() {
    if (!activeRepo || !sessionList) return;
    sessionList.innerHTML = '<div style="color: var(--text-secondary); font-size: 0.8rem; padding: 0.5rem;">加载会话历史中...</div>';
    try {
      const sessions = await API.getUserQASessions(activeRepo.id);
      if (!sessions || sessions.length === 0) {
        sessionList.innerHTML = '<div style="color: var(--text-secondary); font-size: 0.8rem; padding: 0.5rem;">暂无历史会话记录</div>';
        return;
      }

      sessionList.innerHTML = '';
      sessions.forEach(sess => {
        const item = document.createElement('div');
        item.className = 'session-item' + (sess.session_id === currentSessionId ? ' active' : '');

        const info = document.createElement('div');
        info.className = 'session-info';
        const title = document.createElement('div');
        title.className = 'session-title';
        const rawTitle = sess.title || sess.question;
        title.textContent = rawTitle ? rawTitle : ('会话 ' + (sess.session_id ? sess.session_id.substring(0, 8) : ''));
        const meta = document.createElement('div');
        meta.className = 'session-meta';
        meta.textContent = `${sess.message_count}条对话 • ${API.formatDate(sess.updated_at)}`;

        info.appendChild(title);
        info.appendChild(meta);
        item.appendChild(info);

        const delBtn = document.createElement('button');
        delBtn.className = 'session-delete-btn';
        delBtn.title = '删除此会话';
        delBtn.innerHTML = '🗑️';
        delBtn.addEventListener('click', (e) => {
          e.stopPropagation();
          deleteSession(sess.session_id);
        });
        item.appendChild(delBtn);

        item.addEventListener('click', () => {
          selectSession(sess.session_id);
        });

        sessionList.appendChild(item);
      });
    } catch (err) {
      sessionList.innerHTML = `<div style="color: var(--error-color); font-size: 0.8rem; padding: 0.5rem;">获取历史失败: ${err.message}</div>`;
    }
  }

  async function selectSession(sessionId) {
    if (currentSessionId === sessionId && chatMessages.children.length > 1) {
      sessionHistoryPanel.style.display = 'none';
      return;
    }
    currentSessionId = sessionId;
    sessionHistoryPanel.style.display = 'none';
    chatMessages.innerHTML = '<div style="color: var(--text-secondary); font-size: 0.85rem;">正在加载历史对话消息...</div>';

    try {
      const messages = await API.getQASessionMessages(sessionId);
      chatMessages.innerHTML = '';
      if (!messages || messages.length === 0) {
        resetChatMessages();
        return;
      }
      messages.forEach(msg => {
        appendMessage(msg.question, 'user');
        appendBotMessage(msg.answer);
      });
    } catch (err) {
      chatMessages.innerHTML = `<div class="alert alert-error">加载会话消息失败: ${err.message}</div>`;
    }
  }

  async function deleteSession(sessionId) {
    if (!confirm('确定要删除该会话记录吗？')) return;
    try {
      await API.deleteQASession(sessionId);
      if (currentSessionId === sessionId) {
        startNewSession();
      } else {
        loadSessionHistory();
      }
    } catch (err) {
      alert('删除会话失败: ' + err.message);
    }
  }

  let activeAbortController = null;

  function setChatStreamingState(isStreaming) {
    if (isStreaming) {
      sendChatBtn.textContent = '⏹️ 停止';
      sendChatBtn.style.background = '#ef4444';
      sendChatBtn.style.borderColor = '#ef4444';
      chatInput.disabled = true;
    } else {
      sendChatBtn.textContent = '发送';
      sendChatBtn.style.background = '';
      sendChatBtn.style.borderColor = '';
      chatInput.disabled = false;
      activeAbortController = null;
    }
  }

  sendChatBtn.addEventListener('click', () => {
    if (activeAbortController) {
      // User clicked "Stop" button during active streaming
      activeAbortController.abort();
      activeAbortController = null;
      setChatStreamingState(false);
      return;
    }
    sendQuestion();
  });

  chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (!activeAbortController) {
        sendQuestion();
      }
    }
  });

  // 统一拦截问答界面中的超链接点击，避免 404
  if (chatMessages) {
    chatMessages.addEventListener('click', (e) => {
      const a = e.target.closest('a');
      if (!a) return;
      const href = a.getAttribute('href');
      if (!href) return;

      // 1. 如果是外链 (http/https 开头)
      if (/^https?:\/\//i.test(href)) {
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener noreferrer');
        return; // 允许正常打开新标签页
      }

      // 2. 如果是当前页面纯锚点 (#xxx)
      if (href.startsWith('#')) {
        return;
      }

      // 3. 内部 Wiki 相对文档或 .md 链接 (如 openwiki/quickstart.md, zk-repro-client.md)
      e.preventDefault();
      if (activeRepo && activeRepo.id) {
        // 提取干净的文档 ID (去除 openwiki/ 前缀与 .md 后缀)
        let cleanDoc = href.replace(/^\/?(openwiki\/)?/, '').replace(/\.md$/, '');
        const hashPart = cleanDoc.includes('#') ? cleanDoc.split('#')[1] : cleanDoc;
        const targetUrl = `/wiki/${encodeURIComponent(activeRepo.id)}/#${encodeURIComponent(hashPart)}`;
        window.open(targetUrl, '_blank');
      } else {
        console.warn('当前未选中仓库，无法映射 Wiki 文档链接:', href);
      }
    });
  }

  async function sendQuestion() {
    if (activeAbortController) {
      return; // Already streaming
    }

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

    activeAbortController = new AbortController();
    setChatStreamingState(true);

    let fullMarkdown = '';

    try {
      const response = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: activeAbortController.signal,
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
              fullMarkdown += textChunk;
              contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
              chatMessages.scrollTop = chatMessages.scrollHeight;
            } else if (currentEvent === 'done') {
              statusDiv.style.display = 'none';
              contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
              renderMermaidInElement(contentDiv);
              if (sessionHistoryPanel && sessionHistoryPanel.style.display !== 'none') {
                loadSessionHistory();
              }
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
      if (err.name === 'AbortError') {
        fullMarkdown += '\n\n**[已停止本次对话]**';
      } else {
        fullMarkdown += '\n\n**[连接出错: ' + err.message + ']**';
      }
      contentDiv.innerHTML = API.renderMarkdown(fullMarkdown);
    } finally {
      renderMermaidInElement(contentDiv);
      setChatStreamingState(false);
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

  function appendBotMessage(markdownText) {
    const div = document.createElement('div');
    div.className = 'msg msg-bot';
    const contentDiv = document.createElement('div');
    contentDiv.className = 'bot-content-text';
    contentDiv.innerHTML = API.renderMarkdown(markdownText);
    renderMermaidInElement(contentDiv);
    div.appendChild(contentDiv);
    chatMessages.appendChild(div);
    chatMessages.scrollTop = chatMessages.scrollHeight;
    return div;
  }
});
