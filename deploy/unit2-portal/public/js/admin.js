document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  const isSuperAdmin = currentUser && (currentUser.role === 'admin' || currentUser.role === 'super_admin');
  const isGroupLeader = currentUser && currentUser.role === 'group_leader';

  if (!isSuperAdmin && !isGroupLeader) {
    alert('权限不足：仓库配置与微服务项目管理界面仅允许组长或管理员访问！');
    window.location.href = '/portal/index.html';
    return;
  }
  Auth.renderNavbarUser(currentUser);

  if (!isSuperAdmin) {
    document.querySelectorAll('.super-admin-only').forEach(el => el.style.display = 'none');
    const batchSec = document.getElementById('batchSection');
    if (batchSec) batchSec.style.display = 'none';
    const regSec = document.getElementById('registerRepoForm')?.closest('section');
    if (regSec) regSec.style.display = 'none';
    const repoManageSec = document.getElementById('adminRepoTable')?.closest('section');
    if (repoManageSec) repoManageSec.style.display = 'none';
    const projTitle = document.getElementById('projectSectionTitle');
    if (projTitle) projTitle.textContent = '🏢 我的业务域 - 虚拟项目编排与软链运维';
    const projSubtitle = document.getElementById('projectSectionSubtitle');
    if (projSubtitle) projSubtitle.textContent = `您当前为开发组长，已授权管理业务域：${(currentUser.authorized_domains || []).join('、') || '暂无授权'}`;
  }


  // ==========================================
  // Tab Navigation Setup
  // ==========================================
  const TAB_DEFS = [
    { id: 'batch',      icon: '🚀', label: '批量调度',   sel: '#batchSection' },
    { id: 'tokens',     icon: '📊', label: 'Token报表',  sel: '#tokenSection' },
    { id: 'regrepo',    icon: '➕', label: '仓库注册',   sel: '#registerRepoForm' },
    { id: 'repos',      icon: '📦', label: '仓库管理',   sel: '#adminRepoTable' },
    { id: 'projects',   icon: '🏢', label: '微服务项目', sel: '#projectSection' },
    { id: 'quota',      icon: '⚡', label: 'LLM限流',    sel: '#quotaSection' },
    { id: 'gitauth',    icon: '🔑', label: 'Git认证',    sel: '#gitCredsSection' },
    { id: 'storage',    icon: '💽', label: '存储资源池', sel: '#storageSection' },
    { id: 'domains',    icon: '🗂️', label: '业务域字典', sel: '#domainDictSection' },
    { id: 'rbac',       icon: '👥', label: '用户权限',   sel: '#rbacSection' },
    { id: 'logs',       icon: '🧹', label: '日志清理',   sel: '#logDiskSection' },
  ];

  const tabNav = document.getElementById('owAdminTabs');
  const panelsHost = document.getElementById('owAdminPanels');
  const visibleTabs = [];

  TAB_DEFS.forEach(def => {
    const inner = document.querySelector(def.sel);
    if (!inner) return;
    // Always resolve the enclosing <section> so the whole card (title + body) moves together.
    const el = inner.tagName === 'SECTION' ? inner : inner.closest('section');
    if (!el) return;
    if (el.style.display === 'none') return; // skip hidden (group_leader gate)

    const panel = document.createElement('div');
    panel.className = 'ow-tab-panel';
    panel.dataset.tab = def.id;
    el.style.display = '';
    el.style.marginTop = '0';
    el.parentNode.insertBefore(panel, el);
    panel.appendChild(el);
    visibleTabs.push(def);
  });

  if (tabNav && visibleTabs.length > 0) {
    tabNav.innerHTML = visibleTabs.map((def, i) =>
      `<button class="ow-tab${i === 0 ? ' active' : ''}" data-tab="${def.id}" role="tab">${def.icon} ${def.label}</button>`
    ).join('');
    const firstPanel = document.querySelector('.ow-tab-panel[data-tab="' + visibleTabs[0].id + '"]');
    if (firstPanel) firstPanel.classList.add('active');
    tabNav.addEventListener('click', (e) => {
      const btn = e.target.closest('.ow-tab');
      if (!btn) return;
      const tabId = btn.dataset.tab;
      tabNav.querySelectorAll('.ow-tab').forEach(b => b.classList.toggle('active', b === btn));
      document.querySelectorAll('.ow-tab-panel').forEach(p => p.classList.toggle('active', p.dataset.tab === tabId));
    });
  }

  // ==========================================
  // Project Modal: Architecture MD file upload
  // ==========================================
  const projArchMDFile = document.getElementById('projArchMDFile');
  const projArchMDUploadBtn = document.getElementById('projArchMDUploadBtn');
  const projArchMDClearBtn = document.getElementById('projArchMDClearBtn');
  const projArchMDTextarea = document.getElementById('projArchitectureMD');
  const projArchMDFileHint = document.getElementById('projArchMDFileHint');

  if (projArchMDUploadBtn && projArchMDFile) {
    projArchMDUploadBtn.addEventListener('click', () => projArchMDFile.click());
    projArchMDFile.addEventListener('change', (e) => {
      const file = e.target.files && e.target.files[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = (ev) => {
        if (projArchMDTextarea) projArchMDTextarea.value = ev.target.result;
        if (projArchMDFileHint) projArchMDFileHint.innerHTML = `已载入文件 <strong>${API.escapeHTML(file.name)}</strong> (${(file.size/1024).toFixed(1)} KB)，内容已填入下方编辑框。`;
      };
      reader.onerror = () => { alert('读取文件失败'); };
      reader.readAsText(file, 'UTF-8');
      projArchMDFile.value = '';
    });
  }
  if (projArchMDClearBtn && projArchMDTextarea) {
    projArchMDClearBtn.addEventListener('click', () => {
      projArchMDTextarea.value = '';
      if (projArchMDFileHint) projArchMDFileHint.textContent = '由域管理员维护，描述各底层仓库的作用与调用关系。构建时写入 _project_hub/openwiki/architecture.md 供聚合 Wiki 引用。可上传 .md 文件自动填充。';
    });
  }

  const registerForm = document.getElementById('registerRepoForm');
  const adminAlert = document.getElementById('adminAlert');
  const adminTableBody = document.querySelector('#adminRepoTable tbody');
  const refreshAdminBtn = document.getElementById('refreshAdminBtn');

  const repoIdInput = document.getElementById('repoId');
  const localPathInput = document.getElementById('localPath');

  // Search & Pagination Toolbar elements
  const adminSearchInput = document.getElementById('adminSearchInput');
  const adminClearSearchBtn = document.getElementById('adminClearSearchBtn');
  const adminPageSizeSelect = document.getElementById('adminPageSizeSelect');
  const adminPaginationBar = document.getElementById('adminPaginationBar');

  // Log Modal elements
  const logModal = document.getElementById('logModal');
  const closeLogModalBtn = document.getElementById('closeLogModalBtn');
  const logModalTitle = document.getElementById('logModalTitle');
  const logModalMeta = document.getElementById('logModalMeta');
  const logModalContent = document.getElementById('logModalContent');

  // Edit Modal elements
  const editModal = document.getElementById('editModal');
  const closeEditModalBtn = document.getElementById('closeEditModalBtn');
  const cancelEditBtn = document.getElementById('cancelEditBtn');
  const editRepoForm = document.getElementById('editRepoForm');
  const editAlert = document.getElementById('editAlert');

  closeLogModalBtn.addEventListener('click', () => {
    logModal.style.display = 'none';
  });

  closeEditModalBtn.addEventListener('click', () => {
    editModal.style.display = 'none';
  });
  cancelEditBtn.addEventListener('click', () => {
    editModal.style.display = 'none';
  });

  // Dynamic placeholder sync
  repoIdInput.addEventListener('input', () => {
    const val = repoIdInput.value.trim();
    localPathInput.placeholder = `/var/openwiki/repos/${val || 'order-service'}`;
  });

  // State
  let cachedRepos = [];
  let cachedBuildsMap = new Map();
  let configuredDomainsCache = null;
  let domainCodeByNameMap = new Map();
  let projectPollTimer = null;

  // Business Domain Selection Elements
  const repoDomainSelect = document.getElementById('repoBusinessDomain');
  const repoCustomDomainInput = document.getElementById('repoCustomBusinessDomain');
  const editRepoDomainSelect = document.getElementById('editRepoBusinessDomain');
  const editCustomDomainInput = document.getElementById('editCustomBusinessDomain');

  function setupCustomDomainToggle(selectEl, customInputEl) {
    if (!selectEl || !customInputEl) return;
    selectEl.addEventListener('change', () => {
      if (selectEl.value === '__custom__') {
        customInputEl.style.display = 'block';
        customInputEl.focus();
      } else {
        customInputEl.style.display = 'none';
      }
    });
  }

  setupCustomDomainToggle(repoDomainSelect, repoCustomDomainInput);
  setupCustomDomainToggle(editRepoDomainSelect, editCustomDomainInput);

  async function getAvailableDomains(selectedDomain = '') {
    // group_leader: restricted to authorized domains only
    if (isGroupLeader && currentUser && currentUser.authorized_domains) {
      let list = (currentUser.authorized_domains || []).slice();
      if (selectedDomain && !list.includes(selectedDomain)) list.push(selectedDomain);
      return list;
    }

    if (configuredDomainsCache === null) {
      try {
        const list = await API.getDomains();
        configuredDomainsCache = (list || []).map(d => d.name);
      } catch (e) {
        configuredDomainsCache = [];
      }
    }
    let domains = (configuredDomainsCache || []).slice();

    // Backward-compat fallback: if no configured domains yet, dedup from cached repos
    if (domains.length === 0) {
      const set = new Set();
      (cachedRepos || []).forEach(r => { if (r.business_domain) set.add(r.business_domain); });
      domains = Array.from(set);
    }
    if (selectedDomain && !domains.includes(selectedDomain)) domains.push(selectedDomain);
    return domains;
  }

  async function populateDomainDropdown(selectEl, customInputEl, selectedDomain = '', allowCustom = true) {
    if (!selectEl) return;
    const domains = await getAvailableDomains(selectedDomain);

    let html = '<option value="" disabled ' + (!selectedDomain ? 'selected' : '') + '>-- 请选择业务域 --</option>';
    let matched = false;
    domains.forEach(d => {
      const isSel = (d === selectedDomain);
      if (isSel) matched = true;
      html += `<option value="${API.escapeHTML(d)}" ${isSel ? 'selected' : ''}>${API.escapeHTML(d)}</option>`;
    });

    if (allowCustom && !isGroupLeader) {
      html += `<option value="__custom__">➕ 手动输入新业务域...</option>`;
    }
    selectEl.innerHTML = html;

    if (customInputEl) {
      if (selectedDomain && !matched && allowCustom && !isGroupLeader) {
        selectEl.value = '__custom__';
        customInputEl.style.display = 'block';
        customInputEl.value = selectedDomain;
      } else if (domains.length === 0 && allowCustom && !isGroupLeader) {
        selectEl.value = '__custom__';
        customInputEl.style.display = 'block';
        customInputEl.value = selectedDomain || '';
      } else {
        customInputEl.style.display = 'none';
        customInputEl.value = '';
      }
    }
  }

  // Register Repo Form Handler
  registerForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    adminAlert.style.display = 'none';

    let businessDomain = repoDomainSelect ? repoDomainSelect.value : '';
    if (businessDomain === '__custom__' && repoCustomDomainInput) {
      businessDomain = repoCustomDomainInput.value.trim();
    }
    if (!businessDomain) {
      showAlert('请选择或输入所属业务域！', 'error');
      return;
    }

    const tier = (document.getElementById('repoTier') && document.getElementById('repoTier').value) || 'B';

    const repoData = {
      id: repoIdInput.value.trim(),
      name: document.getElementById('repoName').value.trim(),
      git_url: document.getElementById('gitUrl').value.trim(),
      branch: document.getElementById('branch').value.trim() || 'master',
      business_domain: businessDomain,
      tier: tier,
      local_path: localPathInput.value.trim(),
      schedule: document.getElementById('schedule').value.trim()
    };

    try {
      await API.registerRepo(repoData);
      showAlert('仓库注册成功，已成功加入 Cron 调度队列！', 'success');
      registerForm.reset();
      // 保持添加仓库默认分支为 master，默认 Tier 为 B
      document.getElementById('branch').value = 'master';
      if (document.getElementById('repoTier')) document.getElementById('repoTier').value = 'B';
      populateDomainDropdown(repoDomainSelect, repoCustomDomainInput, '', true);
      loadAdminRepos();
    } catch (err) {
      showAlert(err.message, 'error');
    }
  });

  // Edit Repo Form Handler
  editRepoForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    editAlert.style.display = 'none';

    let editBusinessDomain = editRepoDomainSelect ? editRepoDomainSelect.value : '';
    if (editBusinessDomain === '__custom__' && editCustomDomainInput) {
      editBusinessDomain = editCustomDomainInput.value.trim();
    }
    if (!editBusinessDomain) {
      editAlert.textContent = '请选择或输入所属业务域！';
      editAlert.className = 'alert alert-error';
      editAlert.style.display = 'block';
      return;
    }

    const editTier = (document.getElementById('editRepoTier') && document.getElementById('editRepoTier').value) || 'B';

    const repoData = {
      id: document.getElementById('editRepoId').value,
      name: document.getElementById('editRepoName').value.trim(),
      git_url: document.getElementById('editGitUrl').value.trim(),
      branch: document.getElementById('editBranch').value.trim(),
      business_domain: editBusinessDomain,
      tier: editTier,
      local_path: document.getElementById('editLocalPath').value.trim(),
      schedule: document.getElementById('editSchedule').value.trim()
    };

    try {
      await API.updateRepo(repoData);
      editModal.style.display = 'none';
      showAlert(`仓库 [${repoData.id}] 配置已更新成功！`, 'success');
      loadAdminRepos();
    } catch (err) {
      editAlert.textContent = err.message;
      editAlert.className = 'alert alert-error';
      editAlert.style.display = 'block';
    }
  });

  // Search & Pagination State
  let searchKeyword = '';
  let currentPage = 1;
  let pageSize = '10';
  let searchDebounceTimer = null;

  // Search input listeners
  if (adminSearchInput) {
    adminSearchInput.addEventListener('input', (e) => {
      const val = e.target.value.trim();
      if (adminClearSearchBtn) {
        adminClearSearchBtn.style.display = val ? 'block' : 'none';
      }
      clearTimeout(searchDebounceTimer);
      searchDebounceTimer = setTimeout(() => {
        searchKeyword = val;
        currentPage = 1;
        renderAdminTable();
      }, 200);
    });
  }

  if (adminClearSearchBtn) {
    adminClearSearchBtn.addEventListener('click', () => {
      if (adminSearchInput) {
        adminSearchInput.value = '';
        adminSearchInput.focus();
      }
      adminClearSearchBtn.style.display = 'none';
      searchKeyword = '';
      currentPage = 1;
      renderAdminTable();
    });
  }

  if (adminPageSizeSelect) {
    adminPageSizeSelect.addEventListener('change', (e) => {
      pageSize = e.target.value;
      currentPage = 1;
      renderAdminTable();
    });
  }

  // Filter repos based on searchKeyword
  function getFilteredRepos() {
    if (!searchKeyword) return cachedRepos;
    const q = searchKeyword.toLowerCase();
    return cachedRepos.filter(r => {
      return (r.id && r.id.toLowerCase().includes(q)) ||
             (r.name && r.name.toLowerCase().includes(q)) ||
             (r.business_domain && r.business_domain.toLowerCase().includes(q)) ||
             (r.git_url && r.git_url.toLowerCase().includes(q)) ||
             (r.branch && r.branch.toLowerCase().includes(q)) ||
             (r.local_path && r.local_path.toLowerCase().includes(q));
    });
  }

  // Render Admin Repos Table with Pagination
  function renderAdminTable() {
    const filtered = getFilteredRepos();
    const totalItems = filtered.length;
    const size = (pageSize === 'all') ? (totalItems || 1) : parseInt(pageSize, 10);
    const totalPages = Math.max(1, Math.ceil(totalItems / size));

    if (currentPage > totalPages) currentPage = totalPages;
    if (currentPage < 1) currentPage = 1;

    if (totalItems === 0) {
      adminTableBody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-secondary); padding: 2rem;">没有找到匹配的代码仓库。</td></tr>';
      renderAdminPagination(0, 1, 1);
      return;
    }

    const startIndex = (pageSize === 'all') ? 0 : (currentPage - 1) * size;
    const endIndex = (pageSize === 'all') ? totalItems : Math.min(startIndex + size, totalItems);
    const pageRepos = filtered.slice(startIndex, endIndex);

    adminTableBody.innerHTML = pageRepos.map((repo) => {
      const builds = cachedBuildsMap.get(repo.id) || [];
      const latestBuild = builds.length > 0 ? builds[0] : null;

      let buildBadge = '<span style="color:var(--text-secondary); font-size:0.8rem;">尚未构建</span>';
      if (latestBuild) {
        switch (latestBuild.status) {
          case 'running':
            buildBadge = '<span class="badge" style="background:rgba(56,189,248,0.2); color:var(--accent-color);">🔄 构建中...</span>';
            break;
         case 'success':
           buildBadge = '<span class="badge" style="background:rgba(74,222,128,0.2); color:var(--success-color);">✅ 构建成功</span>';
           break;
          case 'completed':
            buildBadge = '<span class="badge" style="background:rgba(74,222,128,0.2); color:var(--success-color);">✅ 构建成功</span>';
            break;
          case 'interrupted':
            buildBadge = '<span class="badge" style="background:rgba(251,191,36,0.2); color:#f59e0b;">⚠️ 已中断</span>';
            break;
         case 'failed':
            buildBadge = '<span class="badge" style="background:rgba(248,113,113,0.2); color:var(--error-color);">❌ 构建失败</span>';
            break;
          case 'skipped':
            buildBadge = '<span class="badge" style="background:rgba(148,163,184,0.2); color:var(--text-secondary);">⏸️ 无代码更新</span>';
            break;
          default:
            buildBadge = `<span class="badge">${API.escapeHTML(latestBuild.status)}</span>`;
        }
      }

      return `
        <tr>
          <td class="col-nowrap">
            <strong>${API.escapeHTML(repo.name)}</strong>
            <div style="font-size:0.75rem; color:var(--text-secondary); margin-top:2px;">
              <code>${API.escapeHTML(repo.id)}</code>
              ${repo.business_domain ? `<span class="badge" style="background:rgba(20,184,166,0.15); color:#14b8a6; font-size:0.7rem; padding:1px 5px; margin-left:4px;">${API.escapeHTML(repo.business_domain)}</span>` : ''}
              ${repo.tier ? `<span class="badge" style="background:rgba(99,102,241,0.15); color:#818cf8; font-size:0.7rem; padding:1px 4px; margin-left:4px;">Tier ${API.escapeHTML(repo.tier)}</span>` : ''}
            </div>
          </td>
          <td>
            <code style="display:block; max-width:280px; word-break:break-all; font-size:0.8rem;">${API.escapeHTML(repo.git_url)}</code>
            <div style="margin-top:0.25rem;"><span class="badge" style="background:rgba(56,189,248,0.15); color:var(--accent-color);">${API.escapeHTML(repo.branch)}</span></div>
          </td>
          <td>
            <code style="display:block; max-width:260px; word-break:break-all; font-size:0.8rem;">${API.escapeHTML(repo.local_path)}</code>
          </td>
          <td class="col-center col-nowrap">
            <code style="font-size:0.85rem;">${API.escapeHTML(repo.schedule)}</code>
          </td>
          <td class="col-center col-nowrap">
            <span class="badge" style="background:${repo.status === 'active' ? 'rgba(74, 222, 128, 0.15)' : 'rgba(248, 113, 113, 0.15)'}; color:${repo.status === 'active' ? 'var(--success-color)' : 'var(--error-color)'}">
              ${API.escapeHTML(repo.status)}
            </span>
          </td>
          <td class="col-center col-nowrap">${buildBadge}</td>
          <td class="col-center col-nowrap">
            <div style="display:inline-flex; gap:0.35rem; align-items:center;">
              <button class="btn btn-secondary btn-sm trigger-build-btn" data-id="${repo.id}" style="white-space:nowrap;">
                ⚡ 立即构建
              </button>
              <button class="btn btn-outline btn-sm view-log-btn" data-id="${repo.id}" style="white-space:nowrap;">
                📋 日志
              </button>
              <button class="btn btn-secondary btn-sm edit-repo-btn" data-id="${repo.id}" style="white-space:nowrap;">
                ✏️ 编辑
              </button>
              <button class="btn btn-secondary btn-sm delete-repo-btn" data-id="${repo.id}" style="color:var(--error-color); border-color:rgba(248,113,113,0.4); white-space:nowrap;">
                🗑️ 删除
              </button>
            </div>
          </td>
        </tr>
      `;
    }).join('');

    renderAdminPagination(totalItems, currentPage, totalPages);
    bindActionButtons();
  }

  // Render Pagination Bar
  function renderAdminPagination(totalItems, page, totalPages) {
    if (!adminPaginationBar) return;
    if (totalItems === 0 || pageSize === 'all' || totalPages <= 1) {
      adminPaginationBar.innerHTML = `<span style="font-size:0.85rem; color:var(--text-secondary);">共 ${totalItems} 个已配置仓库</span>`;
      return;
    }

    let html = `
      <div style="font-size:0.85rem; color:var(--text-secondary);">
        共 ${totalItems} 个仓库 · 第 ${page} / ${totalPages} 页
      </div>
      <div class="pagination-controls" style="display:inline-flex; gap:0.25rem; align-items:center;">
        <button class="btn btn-secondary btn-sm page-btn" ${page <= 1 ? 'disabled' : ''} data-page="${page - 1}">« 上一页</button>
    `;

    for (let p = 1; p <= totalPages; p++) {
      if (totalPages > 7) {
        if (p > 1 && p < page - 1 && p === 2) { html += '<span style="color:var(--text-secondary); padding:0 0.2rem;">...</span>'; continue; }
        if (p > page + 1 && p < totalPages && p === totalPages - 1) { html += '<span style="color:var(--text-secondary); padding:0 0.2rem;">...</span>'; continue; }
        if (p !== 1 && p !== totalPages && Math.abs(p - page) > 1) continue;
      }
      html += `<button class="btn ${p === page ? 'btn-primary' : 'btn-secondary'} btn-sm page-btn" data-page="${p}">${p}</button>`;
    }

    html += `
        <button class="btn btn-secondary btn-sm page-btn" ${page >= totalPages ? 'disabled' : ''} data-page="${page + 1}">下一页 »</button>
      </div>
    `;

    adminPaginationBar.innerHTML = html;

    adminPaginationBar.querySelectorAll('.page-btn:not(:disabled)').forEach(btn => {
      btn.addEventListener('click', () => {
        const targetPage = parseInt(btn.getAttribute('data-page'), 10);
        if (!isNaN(targetPage) && targetPage !== currentPage) {
          currentPage = targetPage;
          renderAdminTable();
          document.querySelector('#adminRepoTable').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        }
      });
    });
  }

  // Open shared build log modal for either a repository ID or a microservice project ID
  async function openBuildLogModal(targetId, displayLabel) {
    logModalTitle.textContent = `📋 构建日志详情: ${displayLabel || targetId}`;
    logModalMeta.textContent = '加载日志中...';
    logModalContent.textContent = '';
    logModal.style.display = 'flex';

    try {
      const builds = await API.getBuildHistory(targetId);
      if (!builds || builds.length === 0) {
        logModalMeta.textContent = '暂无历史构建记录';
        logModalContent.textContent = '无构建日志信息';
        return;
      }

      const latest = builds[0];
      logModalMeta.innerHTML = `
        <div><strong>构建状态:</strong> ${API.escapeHTML(latest.status || '-')}</div>
        <div><strong>Git Commit HEAD:</strong> ${API.escapeHTML(latest.git_head || '无')}</div>
        <div><strong>开始时间:</strong> ${API.formatDate(latest.started_at)}</div>
        <div><strong>完成时间:</strong> ${API.formatDate(latest.finished_at)}</div>
      `;

      let fullLog = '';
      if (latest.log) fullLog += `=== 构建标准日志 ===\n${latest.log}\n\n`;
      if (latest.error) fullLog += `=== 异常与错误信息 ===\n${latest.error}\n\n`;
      if (!fullLog) fullLog = '暂无详细文本日志输出。';

      logModalContent.textContent = fullLog;
    } catch (err) {
      logModalMeta.textContent = '获取日志失败';
      logModalContent.textContent = err.message;
    }
  }

  // Bind Action Buttons
  function bindActionButtons() {
    // Attach trigger build listeners
    document.querySelectorAll('.trigger-build-btn').forEach(btn => {
      btn.addEventListener('click', async (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        btn.disabled = true;
        btn.textContent = '⏳ 发起构建...';

        try {
          await API.triggerBuild(repoId);
          showAlert(`仓库 [${repoId}] 已成功发起构建任务！后台正在实时编译中...`, 'success');

          let pollCount = 0;
          const pollInterval = setInterval(() => {
            pollCount++;
            loadAdminRepos();
            if (pollCount >= 8) clearInterval(pollInterval);
          }, 1500);

        } catch (err) {
          showAlert(`触发构建失败: ${err.message}`, 'error');
          btn.disabled = false;
          btn.textContent = '⚡ 立即构建';
        }
      });
    });

    // Attach view log listeners
    document.querySelectorAll('.view-log-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        openBuildLogModal(repoId, repoId);
      });
    });

    // Attach edit repo listeners
    document.querySelectorAll('.edit-repo-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        const targetRepo = cachedRepos.find(r => r.id === repoId);
        if (!targetRepo) return;

        document.getElementById('editRepoId').value = targetRepo.id;
        document.getElementById('editRepoName').value = targetRepo.name || targetRepo.id;
        document.getElementById('editGitUrl').value = targetRepo.git_url || '';
        document.getElementById('editBranch').value = targetRepo.branch || 'master';
        if (document.getElementById('editRepoTier')) {
          document.getElementById('editRepoTier').value = targetRepo.tier || 'B';
        }
        populateDomainDropdown(editRepoDomainSelect, editCustomDomainInput, targetRepo.business_domain || '', true);
        document.getElementById('editLocalPath').value = targetRepo.local_path || '';
        document.getElementById('editSchedule').value = targetRepo.schedule || '0 2 * * *';

        editAlert.style.display = 'none';
        editModal.style.display = 'flex';
      });
    });

    // Attach delete repo listeners
    document.querySelectorAll('.delete-repo-btn').forEach(btn => {
      btn.addEventListener('click', async (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        if (confirm(`确认要注销/删除仓库 [${repoId}] 吗？相关历史构建记录也将一并注销清除！`)) {
          try {
            await API.deleteRepo(repoId);
            showAlert(`仓库 [${repoId}] 已成功注销并从系统中删除！`, 'success');
            loadAdminRepos();
          } catch (err) {
            showAlert(`注销仓库失败: ${err.message}`, 'error');
          }
        }
      });
    });
  }

  // Load Admin Repos Table with Latest Build Status
  async function loadAdminRepos() {
    try {
      const repos = await API.getRepos();
      cachedRepos = repos || [];

      // Ensure domain dropdowns have options
      populateDomainDropdown(repoDomainSelect, repoCustomDomainInput, repoDomainSelect ? repoDomainSelect.value : '', true).catch(() => {});

      if (!repos || repos.length === 0) {
        adminTableBody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-secondary); padding: 2rem;">暂无已注册仓库。</td></tr>';
        renderAdminPagination(0, 1, 1);
        return;
      }

      // Fetch latest builds for all repos concurrently
      const buildPromises = repos.map(r => API.getBuildHistory(r.id).catch(() => []));
      const buildsList = await Promise.all(buildPromises);

      cachedBuildsMap.clear();
      repos.forEach((repo, i) => {
        cachedBuildsMap.set(repo.id, buildsList[i] || []);
      });

      renderAdminTable();
    } catch (err) {
      adminTableBody.innerHTML = `<tr><td colspan="7" style="color:var(--error-color);">加载失败: ${err.message}</td></tr>`;
    }
  }

  function showAlert(msg, type) {
    adminAlert.textContent = msg;
    adminAlert.className = `alert alert-${type}`;
    adminAlert.style.display = 'block';
  }

  refreshAdminBtn.addEventListener('click', loadAdminRepos);
  loadAdminRepos();

  // ==========================================
  // Microservice Projects Management Logic
  // ==========================================
  const adminProjectTableBody = document.querySelector('#adminProjectTable tbody');
  const openNewProjectModalBtn = document.getElementById('openNewProjectModalBtn');
  const refreshProjectsBtn = document.getElementById('refreshProjectsBtn');
  const projectModal = document.getElementById('projectModal');
  const closeProjectModalBtn = document.getElementById('closeProjectModalBtn');
  const cancelProjectBtn = document.getElementById('cancelProjectBtn');
  const projectForm = document.getElementById('projectForm');
  const projAlert = document.getElementById('projAlert');
  const projReposPicker = document.getElementById('projReposPicker');

  let cachedProjects = [];

  // Project Search & Filter & Pagination Elements
  const projSearchInput = document.getElementById('projSearchInput');
  const projClearSearchBtn = document.getElementById('projClearSearchBtn');
  const projDomainFilter = document.getElementById('projDomainFilter');
  const projStatusFilter = document.getElementById('projStatusFilter');
  const projPageSizeSelect = document.getElementById('projPageSizeSelect');
  const projPaginationBar = document.getElementById('projPaginationBar');

  // Project Search & Filter State
  let projSearchKeyword = '';
  let projDomainFilterVal = 'all';
  let projStatusFilterVal = 'all';
  let projCurrentPage = 1;
  let projPageSize = '10';
  let projSearchDebounceTimer = null;

  // Search input listeners
  if (projSearchInput) {
    projSearchInput.addEventListener('input', (e) => {
      const val = e.target.value.trim();
      if (projClearSearchBtn) {
        projClearSearchBtn.style.display = val ? 'block' : 'none';
      }
      clearTimeout(projSearchDebounceTimer);
      projSearchDebounceTimer = setTimeout(() => {
        projSearchKeyword = val;
        projCurrentPage = 1;
        renderProjectTable();
      }, 200);
    });
  }

  if (projClearSearchBtn) {
    projClearSearchBtn.addEventListener('click', () => {
      if (projSearchInput) {
        projSearchInput.value = '';
        projSearchInput.focus();
      }
      projClearSearchBtn.style.display = 'none';
      projSearchKeyword = '';
      projCurrentPage = 1;
      renderProjectTable();
    });
  }

  if (projDomainFilter) {
    projDomainFilter.addEventListener('change', (e) => {
      projDomainFilterVal = e.target.value;
      projCurrentPage = 1;
      renderProjectTable();
    });
  }

  if (projStatusFilter) {
    projStatusFilter.addEventListener('change', (e) => {
      projStatusFilterVal = e.target.value;
      projCurrentPage = 1;
      renderProjectTable();
    });
  }

  if (projPageSizeSelect) {
    projPageSizeSelect.addEventListener('change', (e) => {
      projPageSize = e.target.value;
      projCurrentPage = 1;
      renderProjectTable();
    });
  }

  // Update dynamic options for project domain filter
  function updateProjectDomainFilterOptions() {
    if (!projDomainFilter) return;
    const currentVal = projDomainFilterVal;
    const domains = new Set();
    (cachedProjects || []).forEach(p => {
      if (p.business_domain && p.business_domain.trim()) {
        domains.add(p.business_domain.trim());
      }
    });
    const sortedDomains = Array.from(domains).sort();

    let html = '<option value="all">全部业务域</option>';
    sortedDomains.forEach(d => {
      const selected = (d === currentVal) ? 'selected' : '';
      html += `<option value="${API.escapeHTML(d)}" ${selected}>${API.escapeHTML(d)}</option>`;
    });
    projDomainFilter.innerHTML = html;
    if (sortedDomains.includes(currentVal)) {
      projDomainFilter.value = currentVal;
    } else {
      projDomainFilterVal = 'all';
      projDomainFilter.value = 'all';
    }
  }

  // Filter projects based on searchKeyword, domain filter, and status filter
  function getFilteredProjects() {
    if (!cachedProjects || cachedProjects.length === 0) return [];
    const q = projSearchKeyword.toLowerCase();

    return cachedProjects.filter(p => {
      // 1. Business Domain Filter
      if (projDomainFilterVal !== 'all' && (p.business_domain || '') !== projDomainFilterVal) {
        return false;
      }

      // 2. Status Filter
      if (projStatusFilterVal !== 'all') {
        const st = p.task_status || p.status || 'never_built';
        if (projStatusFilterVal === 'running') {
          if (st !== 'queued' && st !== 'running' && st !== 'building') return false;
        } else if (projStatusFilterVal === 'success') {
          if (st !== 'success' && st !== 'completed') return false;
        } else if (projStatusFilterVal === 'failed') {
          if (st !== 'failed' && st !== 'interrupted') return false;
        } else if (projStatusFilterVal === 'never_built') {
          if (st !== 'never_built') return false;
        }
      }

      // 3. Keyword Search
      if (!q) return true;

      const subReposText = (p.repos || []).map(r => `${r.alias_name || ''} ${r.repo_id || ''} ${r.role || ''}`).join(' ').toLowerCase();

      return (p.id && p.id.toLowerCase().includes(q)) ||
             (p.name && p.name.toLowerCase().includes(q)) ||
             (p.tri_code && p.tri_code.toLowerCase().includes(q)) ||
             (p.query_key && p.query_key.toLowerCase().includes(q)) ||
             (p.business_domain && p.business_domain.toLowerCase().includes(q)) ||
             (p.description && p.description.toLowerCase().includes(q)) ||
             (p.local_path && p.local_path.toLowerCase().includes(q)) ||
             subReposText.includes(q);
    });
  }

  function showProjAlert(msg, type) {
    projAlert.textContent = msg;
    projAlert.className = `alert alert-${type}`;
    projAlert.style.display = 'block';
  }

  function renderReposPicker(selectedRepos = []) {
    const selectedMap = new Map();
    selectedRepos.forEach(r => selectedMap.set(r.id, r));

    // For Group Leader, filter repos to only those in their authorized domains
    let visibleRepos = cachedRepos || [];
    if (isGroupLeader && currentUser && currentUser.authorized_domains) {
      const allowedDomains = new Set(currentUser.authorized_domains);
      visibleRepos = visibleRepos.filter(r => !r.business_domain || allowedDomains.has(r.business_domain));
    }

    if (!visibleRepos || visibleRepos.length === 0) {
      projReposPicker.innerHTML = '<div style="color:var(--text-secondary); padding:0.5rem;">当前业务域下暂无可选的代码仓库。</div>';
      return;
    }

    let html = '<div style="display:flex; flex-direction:column; gap:0.5rem;">';
    visibleRepos.forEach(repo => {
      const isChecked = selectedMap.has(repo.id);
      const existing = selectedMap.get(repo.id) || {};
      const aliasVal = existing.alias_name || repo.id;
      const roleVal = existing.role || 'service';

      html += `
        <div class="repo-picker-item" style="display:flex; align-items:center; gap:0.75rem; padding:0.4rem; border-radius:4px; background:var(--bg-primary); border:1px solid var(--border-color);">
          <input type="checkbox" class="proj-repo-check" data-id="${repo.id}" id="check_${repo.id}" ${isChecked ? 'checked' : ''} style="cursor:pointer;" />
          <label for="check_${repo.id}" style="font-weight:600; min-width:140px; cursor:pointer;">
            ${API.escapeHTML(repo.name)} <span style="font-size:0.8rem; color:var(--text-secondary);">(${repo.id})</span>
            ${repo.business_domain ? `<span class="badge" style="background:#1e293b; border:1px solid #475569; font-size:0.7rem; margin-left:4px;">${API.escapeHTML(repo.business_domain)}</span>` : ''}
          </label>
          <div style="display:flex; align-items:center; gap:0.3rem;">
            <span style="font-size:0.8rem; color:var(--text-secondary);">别名:</span>
            <input type="text" class="proj-repo-alias" data-id="${repo.id}" value="${API.escapeHTML(aliasVal)}" style="padding:0.2rem 0.4rem; font-size:0.8rem; width:120px; border:1px solid var(--border-color); border-radius:4px; background:var(--bg-secondary); color:var(--text-primary);" />
          </div>
          <div style="display:flex; align-items:center; gap:0.3rem;">
            <span style="font-size:0.8rem; color:var(--text-secondary);">角色:</span>
            <select class="proj-repo-role" data-id="${repo.id}" style="padding:0.2rem 0.4rem; font-size:0.8rem; border:1px solid var(--border-color); border-radius:4px; background:var(--bg-secondary); color:var(--text-primary);">
              <option value="gateway" ${roleVal === 'gateway' ? 'selected' : ''}>网关 (Gateway)</option>
              <option value="service" ${roleVal === 'service' ? 'selected' : ''}>核心微服务 (Service)</option>
              <option value="sdk" ${roleVal === 'sdk' ? 'selected' : ''}>公共二方SDK (SDK)</option>
              <option value="job" ${roleVal === 'job' ? 'selected' : ''}>定时任务 (Job)</option>
              <option value="frontend" ${roleVal === 'frontend' ? 'selected' : ''}>前端应用 (Frontend)</option>
            </select>
          </div>
        </div>
      `;
    });
    html += '</div>';
    projReposPicker.innerHTML = html;
  }

  // Render the business domain <select> for microservice projects.
  async function populateDomainSelect(selectedDomain = '') {
    const domainSelect = document.getElementById('projBusinessDomain');
    if (!domainSelect) return;
    const domains = await getAvailableDomains(selectedDomain);
    renderDomainOptions(domains, selectedDomain, domainSelect);
  }

  function renderDomainOptions(domains, selectedDomain, domainSelect) {
    if (!domains || domains.length === 0) {
      domainSelect.innerHTML = '<option value="">(未指定业务域)</option>';
      return;
    }
    domainSelect.innerHTML = domains.map(d => `
      <option value="${API.escapeHTML(d)}" ${d === selectedDomain ? 'selected' : ''}>${API.escapeHTML(d)}</option>
    `).join('');
  }

  // Open Create Project Modal
  openNewProjectModalBtn.addEventListener('click', () => {
    document.getElementById('projectModalTitle').textContent = '🏢 创建微服务项目';
    document.getElementById('projIsEdit').value = '0';
    document.getElementById('projId').value = '';
    document.getElementById('projId').disabled = false;
    document.getElementById('projName').value = '';
    const projTriCodeInput = document.getElementById('projTriCode');
    if (projTriCodeInput) projTriCodeInput.value = '';
    document.getElementById('projDesc').value = '';
    const projArchMD = document.getElementById('projArchitectureMD');
    if (projArchMD) projArchMD.value = '';
    populateDomainSelect('').catch(()=>{});
    projAlert.style.display = 'none';
    renderReposPicker([]);
    projectModal.style.display = 'flex';
  });

  closeProjectModalBtn.addEventListener('click', () => {
    projectModal.style.display = 'none';
  });
  cancelProjectBtn.addEventListener('click', () => {
    projectModal.style.display = 'none';
  });

  // Project Form Submit (Create or Update)
  projectForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    projAlert.style.display = 'none';

    const isEdit = document.getElementById('projIsEdit').value === '1';
    const projId = document.getElementById('projId').value.trim();
    const projName = document.getElementById('projName').value.trim();
    const projTriCode = ((document.getElementById('projTriCode') || {}).value || '').trim().toUpperCase();
    const projDesc = document.getElementById('projDesc').value.trim();
    const projDomain = document.getElementById('projBusinessDomain').value.trim();

    // Collect selected repos
    const selectedRepos = [];
    document.querySelectorAll('.proj-repo-check:checked').forEach(cb => {
      const repoId = cb.getAttribute('data-id');
      const aliasInput = document.querySelector(`.proj-repo-alias[data-id="${repoId}"]`);
      const roleSelect = document.querySelector(`.proj-repo-role[data-id="${repoId}"]`);
      selectedRepos.push({
        repo_id: repoId,
        alias_name: (aliasInput ? aliasInput.value.trim() : '') || repoId,
        role: roleSelect ? roleSelect.value : 'service'
      });
    });

    const archMDValue = (document.getElementById('projArchitectureMD') || {}).value || '';
    const payload = {
      id: projId,
      name: projName,
      tri_code: projTriCode,
      description: projDesc,
      business_domain: projDomain,
      architecture_md: archMDValue,
      repos: selectedRepos
    };

    try {
      if (isEdit) {
        await API.updateProject(payload);
        showAlert(`微服务项目 [${projName}] 配置已更新成功！`, 'success');
      } else {
        await API.createProject(payload);
        showAlert(`微服务项目 [${projName}] 创建成功，虚拟大仓已初始化！`, 'success');
      }
      projectModal.style.display = 'none';
      loadAdminProjects();
    } catch (err) {
      showProjAlert(`操作失败: ${err.message}`, 'error');
    }
  });

  const PROJECT_TASK_STATUS_BADGES = {
    queued: '<span class="badge" style="background:rgba(245,158,11,0.18); color:#f59e0b; border:1px solid rgba(245,158,11,0.4);">⏳ 排队聚合中</span>',
    running: '<span class="badge" style="background:rgba(56,189,248,0.18); color:#38bdf8; border:1px solid rgba(56,189,248,0.4);">🔄 聚合构建中</span>',
    building: '<span class="badge" style="background:rgba(56,189,248,0.18); color:#38bdf8; border:1px solid rgba(56,189,248,0.4);">🔄 聚合构建中</span>',
    completed: '<span class="badge" style="background:rgba(34,197,94,0.18); color:#22c55e; border:1px solid rgba(34,197,94,0.4);">✅ 构建成功</span>',
    failed: '<span class="badge" style="background:rgba(239,68,68,0.18); color:#ef4444; border:1px solid rgba(239,68,68,0.4);">❌ 构建失败</span>',
    interrupted: '<span class="badge" style="background:rgba(251,146,60,0.18); color:#fb923c; border:1px solid rgba(251,146,60,0.4);">⚠️ 已中断</span>',
    never_built: '<span class="badge" style="background:rgba(148,163,184,0.15); color:var(--text-secondary); border:1px solid rgba(148,163,184,0.35);">⚪ 未构建</span>'
  };

  function formatProjectTaskStatusCell(proj) {
    const taskStatus = proj.task_status || (proj.status === 'building' ? 'running' : (proj.status === 'queued' ? 'queued' : (proj.status === 'failed' ? 'failed' : 'never_built')));
    const badge = PROJECT_TASK_STATUS_BADGES[taskStatus] || PROJECT_TASK_STATUS_BADGES.never_built;
    const lb = proj.latest_build;
    let detailHtml = '';
    if (lb) {
      const ts = lb.finished_at || lb.started_at;
      if (ts) {
        detailHtml += `<div style="font-size:0.72rem; color:var(--text-secondary); margin-top:4px;">${API.formatDate(ts)}</div>`;
      }
      if ((taskStatus === 'failed' || taskStatus === 'interrupted') && lb.error) {
        const snippet = lb.error.length > 48 ? lb.error.slice(0, 48) + '...' : lb.error;
        detailHtml += `<div style="font-size:0.72rem; color:#ef4444; margin-top:2px; max-width:170px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;" title="${API.escapeHTML(lb.error)}">${API.escapeHTML(snippet)}</div>`;
      }
    }
    return `${badge}${detailHtml}`;
  }

  // Render Projects Table with Filtering & Pagination
  function renderProjectTable() {
    const filtered = getFilteredProjects();
    const totalItems = filtered.length;
    const size = (projPageSize === 'all') ? (totalItems || 1) : parseInt(projPageSize, 10);
    const totalPages = Math.max(1, Math.ceil(totalItems / size));

    if (projCurrentPage > totalPages) projCurrentPage = totalPages;
    if (projCurrentPage < 1) projCurrentPage = 1;

    if (totalItems === 0) {
      if (!cachedProjects || cachedProjects.length === 0) {
        adminProjectTableBody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-secondary); padding: 2rem;">暂无微服务项目，点击右上角“创建微服务项目”开始编排。</td></tr>';
      } else {
        adminProjectTableBody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-secondary); padding: 2rem;">没有找到匹配的微服务项目。</td></tr>';
      }
      renderProjectPagination(0, 1, 1);
      return;
    }

    const startIndex = (projPageSize === 'all') ? 0 : (projCurrentPage - 1) * size;
    const endIndex = (projPageSize === 'all') ? totalItems : Math.min(startIndex + size, totalItems);
    const pageProjects = filtered.slice(startIndex, endIndex);

    adminProjectTableBody.innerHTML = pageProjects.map(proj => {
      const reposBadges = (proj.repos || []).map(r => `
        <span class="badge" style="background:var(--bg-secondary); border:1px solid var(--border-color); margin-right:4px; margin-bottom:4px; display:inline-block; font-size:0.75rem;">
          <strong>${API.escapeHTML(r.alias_name)}</strong>
          <span style="opacity:0.7;">(${r.role})</span>
        </span>
      `).join('') || '<span style="color:var(--text-secondary);">尚未绑定仓库</span>';

      const taskStatus = proj.task_status || proj.status || 'never_built';
      const isBusy = taskStatus === 'queued' || taskStatus === 'running' || taskStatus === 'building';
      const queryKey = proj.query_key || proj.tri_code || proj.id;
      const triBadge = proj.tri_code
        ? `<span class="badge" style="background:rgba(99,102,241,0.18); color:#818cf8; border:1px solid rgba(99,102,241,0.4); font-size:0.72rem; margin-left:6px;" title="MCP 查询三字码">${API.escapeHTML(proj.tri_code)}</span>`
        : '';

      return `
        <tr>
          <td>
            <div style="display:flex; align-items:center; flex-wrap:wrap; gap:2px;">
              <strong>${API.escapeHTML(proj.name)}</strong>${triBadge}
            </div>
            <div style="font-size:0.75rem; color:var(--text-secondary); margin-top:2px;">
              <code>${API.escapeHTML(proj.id)}</code> · MCP Key: <code>${API.escapeHTML(queryKey)}</code>
            </div>
            ${proj.description ? `<div style="font-size:0.75rem; color:var(--text-secondary); margin-top:2px;">${API.escapeHTML(proj.description)}</div>` : ''}
          </td>
          <td><span class="badge" style="background:#1e293b; border:1px solid #475569;">${API.escapeHTML(proj.business_domain || '-')}</span></td>
          <td>${reposBadges}</td>
          <td style="font-family:monospace; font-size:0.8rem; word-break:break-all;">${API.escapeHTML(proj.local_path || '-')}</td>
          <td class="col-center col-nowrap">${formatProjectTaskStatusCell(proj)}</td>
          <td class="col-center col-nowrap" style="font-size:0.8rem;">${API.formatDate(proj.updated_at)}</td>
          <td class="col-center col-nowrap">
            <div style="display:inline-flex; gap:0.35rem; align-items:center;">
              <button class="btn btn-primary btn-sm trigger-project-build-btn" data-id="${proj.id}" data-name="${API.escapeHTML(proj.name)}" ${isBusy ? 'disabled' : ''}>${isBusy ? '⏳ 构建中...' : '🛠️ 聚合构建'}</button>
              <button class="btn btn-secondary btn-sm view-project-log-btn" data-id="${proj.id}" data-name="${API.escapeHTML(proj.name)}">📋 日志</button>
              <button class="btn btn-secondary btn-sm inspect-links-btn" data-id="${proj.id}" data-name="${API.escapeHTML(proj.name)}">🔗 软链巡检</button>
              <button class="btn btn-secondary btn-sm edit-project-btn" data-id="${proj.id}">✏️ 编辑</button>
              <button class="btn btn-danger btn-sm delete-project-btn" data-id="${proj.id}">🗑️ 删除</button>
            </div>
          </td>
        </tr>
      `;
    }).join('');

    renderProjectPagination(totalItems, projCurrentPage, totalPages);
    bindProjectActionButtons();
  }

  // Render Project Pagination Bar
  function renderProjectPagination(totalItems, page, totalPages) {
    if (!projPaginationBar) return;
    if (totalItems === 0 || projPageSize === 'all' || totalPages <= 1) {
      projPaginationBar.innerHTML = `<span style="font-size:0.85rem; color:var(--text-secondary);">共 ${totalItems} 个微服务项目</span>`;
      return;
    }

    let html = `
      <div style="font-size:0.85rem; color:var(--text-secondary);">
        共 ${totalItems} 个项目 · 第 ${page} / ${totalPages} 页
      </div>
      <div class="pagination-controls" style="display:inline-flex; gap:0.25rem; align-items:center;">
        <button class="btn btn-secondary btn-sm proj-page-btn" ${page <= 1 ? 'disabled' : ''} data-page="${page - 1}">« 上一页</button>
    `;

    for (let p = 1; p <= totalPages; p++) {
      if (totalPages > 7) {
        if (p > 1 && p < page - 1 && p === 2) { html += '<span style="color:var(--text-secondary); padding:0 0.2rem;">...</span>'; continue; }
        if (p > page + 1 && p < totalPages && p === totalPages - 1) { html += '<span style="color:var(--text-secondary); padding:0 0.2rem;">...</span>'; continue; }
        if (p !== 1 && p !== totalPages && Math.abs(p - page) > 1) continue;
      }
      html += `<button class="btn ${p === page ? 'btn-primary' : 'btn-secondary'} btn-sm proj-page-btn" data-page="${p}">${p}</button>`;
    }

    html += `
        <button class="btn btn-secondary btn-sm proj-page-btn" ${page >= totalPages ? 'disabled' : ''} data-page="${page + 1}">下一页 »</button>
      </div>
    `;

    projPaginationBar.innerHTML = html;

    projPaginationBar.querySelectorAll('.proj-page-btn:not(:disabled)').forEach(btn => {
      btn.addEventListener('click', () => {
        const targetPage = parseInt(btn.getAttribute('data-page'), 10);
        if (!isNaN(targetPage) && targetPage !== projCurrentPage) {
          projCurrentPage = targetPage;
          renderProjectTable();
          document.querySelector('#adminProjectTable')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        }
      });
    });
  }

  // Bind Action Buttons for Microservice Projects
  function bindProjectActionButtons() {
    document.querySelectorAll('.trigger-project-build-btn').forEach(btn => {
      btn.addEventListener('click', async (e) => {
        const projId = e.currentTarget.getAttribute('data-id');
        const projName = e.currentTarget.getAttribute('data-name');
        try {
          await API.triggerProjectBuild(projId);
          showAlert(`微服务项目 [${projName}] 聚合构建已触发，正在扫描跨仓拓扑并生成系统概览！`, 'success');
          loadAdminProjects();
        } catch (err) {
          showAlert(`触发项目构建失败: ${err.message}`, 'error');
        }
      });
    });

    document.querySelectorAll('.view-project-log-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const projId = e.currentTarget.getAttribute('data-id');
        const projName = e.currentTarget.getAttribute('data-name');
        openBuildLogModal(projId, `微服务项目 [${projName || projId}]`);
      });
    });

    document.querySelectorAll('.inspect-links-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const projId = e.currentTarget.getAttribute('data-id');
        const projName = e.currentTarget.getAttribute('data-name');
        openLinksModal(projId, projName);
      });
    });

    document.querySelectorAll('.edit-project-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const projId = e.currentTarget.getAttribute('data-id');
        const proj = cachedProjects.find(p => p.id === projId);
        if (!proj) return;

        document.getElementById('projectModalTitle').textContent = `✏️ 编辑微服务项目 [${proj.name}]`;
        document.getElementById('projIsEdit').value = '1';
        document.getElementById('projId').value = proj.id;
        document.getElementById('projId').disabled = true;
        document.getElementById('projName').value = proj.name;
        const projTriCodeEdit = document.getElementById('projTriCode');
        if (projTriCodeEdit) projTriCodeEdit.value = proj.tri_code || '';
        document.getElementById('projDesc').value = proj.description || '';
        const archMDEdit = document.getElementById('projArchitectureMD');
        if (archMDEdit) archMDEdit.value = proj.architecture_md || '';
        populateDomainSelect(proj.business_domain || '').catch(()=>{});
        projAlert.style.display = 'none';

        renderReposPicker(proj.repos || []);
        projectModal.style.display = 'flex';
      });
    });

    document.querySelectorAll('.delete-project-btn').forEach(btn => {
      btn.addEventListener('click', async (e) => {
        const projId = e.currentTarget.getAttribute('data-id');
        if (confirm(`确认要删除微服务项目 [${projId}] 吗？底层仓库不会被删除，仅解除项目聚合！`)) {
          try {
            await API.deleteProject(projId);
            showAlert(`微服务项目 [${projId}] 已删除！`, 'success');
            loadAdminProjects();
          } catch (err) {
            showAlert(`删除项目失败: ${err.message}`, 'error');
          }
        }
      });
    });
  }

  // Cross-Disk Symlinks Modal & Repair (Task 16)
  const linksModal = document.getElementById('linksModal');
  const closeLinksModalBtn = document.getElementById('closeLinksModalBtn');
  if (closeLinksModalBtn) {
    closeLinksModalBtn.addEventListener('click', () => {
      linksModal.style.display = 'none';
    });
  }

  async function openLinksModal(projId, projName) {
    const title = document.getElementById('linksModalTitle');
    const localPathEl = document.getElementById('linksModalLocalPath');
    const healthSummaryEl = document.getElementById('linksModalHealthSummary');
    const volCountsEl = document.getElementById('linksModalVolumeCounts');
    const tableBody = document.querySelector('#linksDetailTable tbody');
    const repairBtn = document.getElementById('repairLinksBtn');

    title.textContent = `🔗 虚拟大仓 [${projName}] 跨盘软链巡检与分布图`;
    tableBody.innerHTML = '<tr><td colspan="5" style="text-align:center;">正在扫描跨盘软链与物理卷状态...</td></tr>';
    linksModal.style.display = 'flex';

    repairBtn.onclick = async () => {
      repairBtn.disabled = true;
      repairBtn.textContent = '⏳ 正在重新创建与修复软链...';
      try {
        await API.repairProjectLinks(projId);
        showAlert(`项目 [${projName}] 跨盘软链已成功修复并刷新！`, 'success');
        openLinksModal(projId, projName);
      } catch (err) {
        alert('修复失败: ' + err.message);
      } finally {
        repairBtn.disabled = false;
        repairBtn.textContent = '🛠️ 一键修复断链';
      }
    };

    try {
      const data = await API.getProjectLinksHealth(projId);
      localPathEl.textContent = data.local_path || '-';
      const healthColor = data.broken_count === 0 ? 'var(--success-color)' : 'var(--error-color)';
      healthSummaryEl.innerHTML = `<span style="color:${healthColor};">${data.healthy_count} 正常 / ${data.broken_count} 断链 (共 ${data.total_repos} 仓)</span>`;

      // Volume distribution badges
      const volBadges = Object.entries(data.volume_counts || {}).map(([vol, cnt]) => `
        <span class="badge" style="background:#3b82f6; color:#fff; font-size:0.8rem; padding:0.2rem 0.5rem; border-radius:4px;">
          💽 ${API.escapeHTML(vol)}: ${cnt} 仓
        </span>
      `).join('') || '<span style="color:var(--text-secondary);">-</span>';
      volCountsEl.innerHTML = volBadges;

      if (!data.items || data.items.length === 0) {
        tableBody.innerHTML = '<tr><td colspan="5" style="text-align:center; color:var(--text-secondary);">项目尚未绑定子仓库</td></tr>';
        return;
      }

      tableBody.innerHTML = data.items.map(item => {
        const isOk = item.status === 'ok';
        const statusBadge = isOk
          ? '<span style="color:var(--success-color);">✅ 正常链接</span>'
          : `<span style="color:var(--error-color);">❌ 异常 (${API.escapeHTML(item.status)})</span>`;

        return `
          <tr>
            <td><strong>${API.escapeHTML(item.alias_name)}</strong></td>
            <td>
              <div>${API.escapeHTML(item.repo_name)}</div>
              <div style="font-size:0.75rem; color:var(--text-secondary);">${API.escapeHTML(item.repo_id)}</div>
            </td>
            <td>
              <div style="font-size:0.8rem; font-family:monospace; color:var(--text-primary); word-break:break-all;">${API.escapeHTML(item.logical_path)}</div>
              <div style="font-size:0.75rem; color:var(--text-secondary); margin-top:2px;">↳ 物理存储卷: <code style="color:#38bdf8;">${API.escapeHTML(item.volume_id)}</code> (${API.escapeHTML(item.physical_path)})</div>
            </td>
            <td class="col-center col-nowrap">${statusBadge}</td>
            <td class="col-center col-nowrap" style="font-size:0.85rem;">${item.size_mb.toFixed(1)} MB</td>
          </tr>
        `;
      }).join('');

    } catch (err) {
      tableBody.innerHTML = `<tr><td colspan="5" style="color:var(--error-color); text-align:center;">巡检失败: ${err.message}</td></tr>`;
    }
  }

  // Load Projects & keep auto-polling while any project build is queued/running
  async function loadAdminProjects() {
    try {
      const projects = await API.getProjects();
      cachedProjects = projects || [];
      updateProjectDomainFilterOptions();
      renderProjectTable();
      const hasActiveProjectBuild = cachedProjects.some(p => {
        const st = p.task_status || p.status;
        return st === 'queued' || st === 'running' || st === 'building';
      });
      if (hasActiveProjectBuild && !projectPollTimer) {
        projectPollTimer = setInterval(loadAdminProjects, 4000);
      } else if (!hasActiveProjectBuild && projectPollTimer) {
        clearInterval(projectPollTimer);
        projectPollTimer = null;
      }
    } catch (err) {
      adminProjectTableBody.innerHTML = `<tr><td colspan="7" style="color:var(--error-color);">加载微服务项目失败: ${err.message}</td></tr>`;
    }
  }

  refreshProjectsBtn.addEventListener('click', loadAdminProjects);
  loadAdminProjects();

  // ==========================================
  // Batch Scheduler & Token Telemetry Logic
  // ==========================================
  let activeBatchId = localStorage.getItem('openwiki_active_batch_id') || '';
  let batchPollTimer = null;

  // Batch Import Modal elements
  const openBatchImportModalBtn = document.getElementById('openBatchImportModalBtn');
  const closeBatchImportModalBtn = document.getElementById('closeBatchImportModalBtn');
  const cancelBatchImportBtn = document.getElementById('cancelBatchImportBtn');
  const batchImportModal = document.getElementById('batchImportModal');
  const batchImportForm = document.getElementById('batchImportForm');
  const batchDataInput = document.getElementById('batchDataInput');
  const batchAlert = document.getElementById('batchAlert');
  const refreshBatchStatusBtn = document.getElementById('refreshBatchStatusBtn');

  // Batch Status elements
  const batchStatusBadge = document.getElementById('batchStatusBadge');
  const batchWindowBadge = document.getElementById('batchWindowBadge');
  const batchETABadge = document.getElementById('batchETABadge');
  const batchProgressText = document.getElementById('batchProgressText');
  const batchProgressBar = document.getElementById('batchProgressBar');
  const startBatchBtn = document.getElementById('startBatchBtn');
  const disableNightModeBtn = document.getElementById('disableNightModeBtn');
  const pauseBatchBtn = document.getElementById('pauseBatchBtn');
  const resumeBatchBtn = document.getElementById('resumeBatchBtn');
  const retryFailedBatchBtn = document.getElementById('retryFailedBatchBtn');
  const cancelBatchBtn = document.getElementById('cancelBatchBtn');
  const batchFailedPanel = document.getElementById('batchFailedPanel');
  const batchFailedCountText = document.getElementById('batchFailedCountText');
  const batchFailedList = document.getElementById('batchFailedList');
  const retryAllFailedPanelBtn = document.getElementById('retryAllFailedPanelBtn');
  const concurrencySlider = document.getElementById('concurrencySlider');
  const concurrencyVal = document.getElementById('concurrencyVal');
  const applyConcurrencyBtn = document.getElementById('applyConcurrencyBtn');
  const slotIndicators = document.getElementById('slotIndicators');

  // Token Telemetry elements
  const copyFeishuTSVBtn = document.getElementById('copyFeishuTSVBtn');
  const statPromptTokens = document.getElementById('statPromptTokens');
  const statCompletionTokens = document.getElementById('statCompletionTokens');
  const statTotalTokens = document.getElementById('statTotalTokens');
  const statSavedTokens = document.getElementById('statSavedTokens');
  const dailyTokenTableBody = document.querySelector('#dailyTokenTable tbody');

  // Modal open/close
  if (openBatchImportModalBtn) {
    openBatchImportModalBtn.addEventListener('click', () => {
      batchAlert.style.display = 'none';
      batchImportModal.style.display = 'flex';
    });
  }
  if (closeBatchImportModalBtn) {
    closeBatchImportModalBtn.addEventListener('click', () => {
      batchImportModal.style.display = 'none';
    });
  }
  if (cancelBatchImportBtn) {
    cancelBatchImportBtn.addEventListener('click', () => {
      batchImportModal.style.display = 'none';
    });
  }

  // Concurrency slider sync
  if (concurrencySlider) {
    concurrencySlider.addEventListener('input', () => {
      concurrencyVal.textContent = concurrencySlider.value;
    });
  }

  // Apply Concurrency Button
  if (applyConcurrencyBtn) {
    applyConcurrencyBtn.addEventListener('click', async () => {
      const c = Number(concurrencySlider.value);
      try {
        await API.updateBatchConcurrency(activeBatchId, c);
        showAlert(`并发已动态调整为 ${c} 并实时生效！`, 'success');
        refreshBatchStatus();
      } catch (err) {
        showAlert(`调整并发失败: ${err.message}`, 'error');
      }
    });
  }

  // Batch Control Buttons
  if (startBatchBtn) {
    startBatchBtn.addEventListener('click', async () => {
      if (!activeBatchId) return;
      try {
        await API.controlBatch(activeBatchId, 'start');
        showAlert('批跑调度已正式启动！', 'success');
        refreshBatchStatus();
      } catch (err) {
        showAlert(`启动批跑失败: ${err.message}`, 'error');
      }
    });
  }

  if (disableNightModeBtn) {
    disableNightModeBtn.addEventListener('click', async () => {
      if (!activeBatchId) return;
      try {
        await API.controlBatch(activeBatchId, 'disable-night-mode');
        showAlert('已解除夜间算力窗口限制，立即开始白天调度构建！', 'success');
        refreshBatchStatus();
      } catch (err) {
        showAlert(`解除夜间限制失败: ${err.message}`, 'error');
      }
    });
  }

  if (pauseBatchBtn) {
    pauseBatchBtn.addEventListener('click', async () => {
      if (!activeBatchId) return;
      try {
        await API.controlBatch(activeBatchId, 'pause');
        showAlert('批跑已成功暂停！', 'success');
        refreshBatchStatus();
      } catch (err) {
        showAlert(`暂停批跑失败: ${err.message}`, 'error');
      }
    });
  }

  if (resumeBatchBtn) {
    resumeBatchBtn.addEventListener('click', async () => {
      if (!activeBatchId) return;
      try {
        await API.controlBatch(activeBatchId, 'resume');
        showAlert('批跑已恢复执行！', 'success');
        refreshBatchStatus();
      } catch (err) {
        showAlert(`恢复批跑失败: ${err.message}`, 'error');
      }
    });
  }

  async function triggerRetryFailedBatch(repoIds = []) {
    if (!activeBatchId) return;
    try {
      const payload = repoIds && repoIds.length > 0 ? { repo_ids: repoIds } : null;
      const res = await API.controlBatch(activeBatchId, 'retry-failed', payload);
      const count = res && typeof res.retried_count === 'number' ? res.retried_count : (repoIds.length || '所有');
      showAlert(`已将 ${count} 个失败/未完成任务重新投入批量调度队列！`, 'success');
      refreshBatchStatus();
    } catch (err) {
      showAlert(`失败任务重跑发起失败: ${err.message}`, 'error');
    }
  }

  if (retryFailedBatchBtn) {
    retryFailedBatchBtn.addEventListener('click', () => triggerRetryFailedBatch([]));
  }
  if (retryAllFailedPanelBtn) {
    retryAllFailedPanelBtn.addEventListener('click', () => triggerRetryFailedBatch([]));
  }

  if (cancelBatchBtn) {
    cancelBatchBtn.addEventListener('click', async () => {
      if (!activeBatchId) return;
      if (confirm(`确认要中止批跑 [${activeBatchId}] 吗？`)) {
        try {
          await API.controlBatch(activeBatchId, 'cancel');
          showAlert('批跑已中止！', 'success');
          refreshBatchStatus();
        } catch (err) {
          showAlert(`中止批跑失败: ${err.message}`, 'error');
        }
      }
    });
  }

  // Helper to parse CSV or JSON baseline repos input
  function parseBatchInput(text) {
    text = (text || '').trim();
    if (text.startsWith('[') && text.endsWith(']')) {
      return JSON.parse(text);
    }
    const lines = text.split(/\r?\n/).filter(l => l.trim().length > 0);
    if (lines.length === 0) return [];

    let startIndex = 0;
    const header = lines[0].toLowerCase();
    if (header.includes('git_url') || header.includes('id') || header.includes('name')) {
      startIndex = 1;
    }

    const repos = [];
    for (let i = startIndex; i < lines.length; i++) {
      const parts = lines[i].split(/[,\t]/).map(p => p.trim());
      if (parts.length >= 2) {
        repos.push({
          id: parts[0],
          name: parts[1] || parts[0],
          git_url: parts[2] || '',
          branch: parts[3] || 'master',
          tier: parts[4] || 'A',
          business_domain: parts[5] || '',
          last_release_at: parts[6] || '',
          tri_code: (parts[7] || '').toUpperCase()
        });
      }
    }
    return repos;
  }

  // Batch Import Form Submit
  if (batchImportForm) {
    batchImportForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      batchAlert.style.display = 'none';

      let repos = [];
      try {
        repos = parseBatchInput(batchDataInput.value);
      } catch (err) {
        batchAlert.className = 'alert alert-danger';
        batchAlert.textContent = `解析数据失败: ${err.message}`;
        batchAlert.style.display = 'block';
        return;
      }

      if (repos.length === 0) {
        batchAlert.className = 'alert alert-danger';
        batchAlert.textContent = '未解析到任何有效仓库数据，请检查输入格式！';
        batchAlert.style.display = 'block';
        return;
      }

      // Enrich missing tri_code from Business Domain dictionary cache if available
      if (domainCodeByNameMap.size === 0) {
        try {
          const domains = await API.getDomains();
          (domains || []).forEach(d => {
            if (d.name && d.code) domainCodeByNameMap.set(d.name, String(d.code).toUpperCase());
          });
        } catch (_) {}
      }
      repos = repos.map(r => {
        if (!r.tri_code && r.business_domain && domainCodeByNameMap.has(r.business_domain)) {
          return { ...r, tri_code: domainCodeByNameMap.get(r.business_domain) };
        }
        return r;
      });

      const payload = {
        batch_name: document.getElementById('batchName').value.trim() || '生产基线批量调度',
        concurrency: Number(document.getElementById('batchInitialConcurrency').value) || 2,
        night_mode_only: document.getElementById('batchNightMode').checked,
        auto_start: document.getElementById('batchAutoStart').checked,
        repos: repos
      };

      try {
        const res = await API.batchImportRepos(payload);
        activeBatchId = res.batch_id;
        localStorage.setItem('openwiki_active_batch_id', activeBatchId);

        showAlert(`批量导入成功！已导入 ${res.total_imported} 个仓库并生成微服务虚拟Monorepo，批跑 [${res.batch_id}] 已启动！`, 'success');
        batchImportModal.style.display = 'none';
        batchImportForm.reset();

        refreshBatchStatus();
        loadAdminRepos();
        loadAdminProjects();
        loadDailyTokens();
      } catch (err) {
        batchAlert.className = 'alert alert-danger';
        batchAlert.textContent = err.message;
        batchAlert.style.display = 'block';
      }
    });
  }

  // Refresh and Render Batch Status
  async function refreshBatchStatus() {
    if (!activeBatchId) {
      try {
        const autoData = await API.getBatchStatus('');
        if (autoData && autoData.batch_run && autoData.batch_run.id) {
          activeBatchId = autoData.batch_run.id;
          localStorage.setItem('openwiki_active_batch_id', activeBatchId);
        }
      } catch (_) {}
    }

    if (!activeBatchId) {
      batchStatusBadge.textContent = '无正在运行的批跑';
      batchStatusBadge.style.background = '#64748b';
      batchETABadge.textContent = '-';
      batchProgressText.textContent = '0 / 0 (0%)';
      batchProgressBar.style.width = '0%';
      if (startBatchBtn) startBatchBtn.style.display = 'none';
      if (disableNightModeBtn) disableNightModeBtn.style.display = 'none';
      if (pauseBatchBtn) pauseBatchBtn.style.display = 'none';
      if (resumeBatchBtn) resumeBatchBtn.style.display = 'none';
      if (retryFailedBatchBtn) retryFailedBatchBtn.style.display = 'none';
      if (cancelBatchBtn) cancelBatchBtn.style.display = 'none';
      if (batchFailedPanel) batchFailedPanel.style.display = 'none';
      slotIndicators.innerHTML = '';
      return;
    }

    try {
      const data = await API.getBatchStatus(activeBatchId);
      const run = data.batch_run;

      // Status Badge
      const statusMap = {
        'pending': { text: '排队就绪', bg: '#3b82f6' },
        'running': { text: '正在调度构建', bg: '#10b981' },
        'paused': { text: '已暂停', bg: '#f59e0b' },
        'completed': { text: '批跑已全部完成', bg: '#059669' },
        'failed': { text: '已中止/失败', bg: '#ef4444' }
      };
      const info = statusMap[run.status] || { text: run.status, bg: '#64748b' };
      batchStatusBadge.textContent = `${info.text} (${run.name})`;
      batchStatusBadge.style.background = info.bg;

      // Night Window Badge
      if (run.night_mode_only) {
        batchWindowBadge.style.display = 'inline-block';
        if (data.is_window_open) {
          batchWindowBadge.textContent = '🌙 夜间窗口执行中 (22:00-07:00)';
          batchWindowBadge.style.background = '#10b981';
        } else {
          batchWindowBadge.textContent = '⏳ 等待夜间窗口 (22:00-07:00)';
          batchWindowBadge.style.background = '#f59e0b';
        }
      } else {
        batchWindowBadge.style.display = 'none';
      }

      // Progress & ETA
      const total = run.total_count || 1;
      const processed = run.processed_count || 0;
      const pct = Math.round((processed / total) * 100);
      batchProgressText.textContent = `${processed} / ${total} (${pct}%) [成功:${run.success_count} 跳过:${run.skipped_count} 失败:${run.failed_count}]`;
      batchProgressBar.style.width = `${pct}%`;

      if (run.status === 'completed') {
        batchETABadge.textContent = '已完成 🎉';
        batchETABadge.style.color = '#10b981';
      } else {
        batchETABadge.textContent = data.formatted_eta || '计算中...';
        batchETABadge.style.color = '#f59e0b';
      }

      // Concurrency Slider Sync
      if (run.max_concurrency) {
        concurrencySlider.value = run.max_concurrency;
        concurrencyVal.textContent = run.max_concurrency;
      }

      // Buttons
      if (startBatchBtn) startBatchBtn.style.display = 'none';
      if (disableNightModeBtn) disableNightModeBtn.style.display = 'none';
      if (pauseBatchBtn) pauseBatchBtn.style.display = 'none';
      if (resumeBatchBtn) resumeBatchBtn.style.display = 'none';
      if (retryFailedBatchBtn) retryFailedBatchBtn.style.display = 'none';
      if (cancelBatchBtn) cancelBatchBtn.style.display = 'none';

      if (run.night_mode_only && !data.is_window_open && run.status !== 'completed' && run.status !== 'failed') {
        if (disableNightModeBtn) disableNightModeBtn.style.display = 'inline-block';
      }

      if (run.status === 'running') {
        if (pauseBatchBtn) pauseBatchBtn.style.display = 'inline-block';
        if (cancelBatchBtn) cancelBatchBtn.style.display = 'inline-block';
      } else if (run.status === 'paused') {
        if (resumeBatchBtn) resumeBatchBtn.style.display = 'inline-block';
        if (cancelBatchBtn) cancelBatchBtn.style.display = 'inline-block';
      } else if (run.status === 'pending') {
        if (startBatchBtn) startBatchBtn.style.display = 'inline-block';
        if (cancelBatchBtn) cancelBatchBtn.style.display = 'inline-block';
      }

      const failedTasks = data.failed_tasks || [];
      const hasRetryable = (run.failed_count || 0) > 0 || (run.status === 'failed' && (data.pending_count || 0) > 0);
      if (hasRetryable && retryFailedBatchBtn) {
        retryFailedBatchBtn.textContent = (run.failed_count || 0) > 0
          ? `🔁 失败任务批量重跑 (${run.failed_count})`
          : `🔁 重新投入调度 (${data.pending_count || 0})`;
        retryFailedBatchBtn.style.display = 'inline-block';
      }

      if (batchFailedPanel && batchFailedList && batchFailedCountText) {
        if (failedTasks.length > 0) {
          batchFailedCountText.textContent = String(failedTasks.length);
          batchFailedList.innerHTML = failedTasks.map(ft => {
            const errText = ft.error || ft.summary || '执行失败';
            const snippet = errText.length > 90 ? errText.slice(0, 90) + '...' : errText;
            return `
              <div style="display:flex; justify-content:space-between; align-items:center; gap:0.75rem; padding:0.35rem 0.6rem; border-radius:4px; background:rgba(15, 23, 42, 0.55); border:1px solid rgba(239, 68, 68, 0.25); font-size:0.82rem;">
                <div style="min-width:0; flex:1;">
                  <strong style="color:#f8fafc;">${API.escapeHTML(ft.repo_id)}</strong>
                  <span style="color:#fca5a5; margin-left:0.5rem;" title="${API.escapeHTML(errText)}">${API.escapeHTML(snippet)}</span>
                </div>
                <button class="btn btn-secondary btn-sm retry-single-batch-task-btn" data-repo-id="${API.escapeHTML(ft.repo_id)}">🔁 重跑此项</button>
              </div>
            `;
          }).join('');
          batchFailedList.querySelectorAll('.retry-single-batch-task-btn').forEach(btn => {
            btn.addEventListener('click', (e) => {
              const repoId = e.currentTarget.getAttribute('data-repo-id');
              if (repoId) triggerRetryFailedBatch([repoId]);
            });
          });
          batchFailedPanel.style.display = 'block';
        } else {
          batchFailedPanel.style.display = 'none';
          batchFailedList.innerHTML = '';
        }
      }

      // Slot Indicators
      let slotHTML = '';
      const activeMap = {};
      (data.active_slots || []).forEach(s => {
        activeMap[s.slot_id] = s;
      });

      const totalSlots = Number(concurrencySlider.value) || 2;
      for (let i = 0; i < totalSlots; i++) {
        const slot = activeMap[i];
        if (slot) {
          slotHTML += `
            <div style="background: rgba(59, 130, 246, 0.2); border: 1px solid #3b82f6; color: #93c5fd; padding: 0.35rem 0.65rem; border-radius: 4px; font-size: 0.8rem; display: flex; align-items: center; gap: 0.4rem;">
              <span style="display:inline-block; width:8px; height:8px; border-radius:50%; background:#3b82f6; animation:pulse 1.5s infinite;"></span>
              <strong>[Slot ${i}]</strong> ${API.escapeHTML(slot.repo_id)} (${slot.stage} ${slot.elapsed_sec}s)
            </div>`;
        } else {
          slotHTML += `
            <div style="background: rgba(148, 163, 184, 0.1); border: 1px solid #475569; color: #94a3b8; padding: 0.35rem 0.65rem; border-radius: 4px; font-size: 0.8rem;">
              <strong>[Slot ${i}]</strong> 空闲
            </div>`;
        }
      }
      slotIndicators.innerHTML = slotHTML;

    } catch (err) {
      console.warn('refreshBatchStatus error:', err);
    }
  }

  if (refreshBatchStatusBtn) {
    refreshBatchStatusBtn.addEventListener('click', refreshBatchStatus);
  }

  // Load Daily Token Telemetry
  async function loadDailyTokens() {
    try {
      const data = await API.getDailyTokens(14);
      if (!data || data.length === 0) {
        dailyTokenTableBody.innerHTML = `<tr><td colspan="8" style="text-align:center; color:var(--text-secondary);">暂无 Token 消耗数据</td></tr>`;
        return;
      }

      // Today / Latest Metrics
      const latest = data[0];
      statPromptTokens.textContent = Number(latest.prompt_tokens || 0).toLocaleString();
      statCompletionTokens.textContent = Number(latest.completion_tokens || 0).toLocaleString();
      statTotalTokens.textContent = Number(latest.total_tokens || 0).toLocaleString();
      statSavedTokens.textContent = Number(latest.saved_tokens || 0).toLocaleString();

      let rowsHTML = '';
      data.forEach(item => {
        rowsHTML += `
          <tr>
            <td><strong>${API.escapeHTML(item.date)}</strong></td>
            <td style="color:#38bdf8;">${Number(item.prompt_tokens || 0).toLocaleString()}</td>
            <td style="color:#a855f7;">${Number(item.completion_tokens || 0).toLocaleString()}</td>
            <td style="color:#f59e0b; font-weight:bold;">${Number(item.total_tokens || 0).toLocaleString()}</td>
            <td style="color:#10b981; font-weight:500;">+${Number(item.saved_tokens || 0).toLocaleString()}</td>
            <td class="col-center">${item.build_count || 0}</td>
            <td class="col-center" style="color:#10b981;">${item.skipped_count || 0}</td>
            <td style="font-size:0.8rem; color:var(--text-secondary);">${API.escapeHTML(item.updated_at || '-')}</td>
          </tr>`;
      });
      dailyTokenTableBody.innerHTML = rowsHTML;

    } catch (err) {
      dailyTokenTableBody.innerHTML = `<tr><td colspan="8" style="color:var(--error-color);">加载 Token 统计失败: ${err.message}</td></tr>`;
    }
  }

  // Copy Feishu TSV to Clipboard (with fallback for non-secure HTTP contexts)
  if (copyFeishuTSVBtn) {
    copyFeishuTSVBtn.addEventListener('click', async () => {
      try {
        const tsv = await API.getTokensExportTSV();
        let copied = false;
        if (navigator.clipboard && navigator.clipboard.writeText) {
          try {
            await navigator.clipboard.writeText(tsv);
            copied = true;
          } catch (e) { /* fall through to legacy */ }
        }
        if (!copied) {
          const ta = document.createElement('textarea');
          ta.value = tsv;
          ta.style.position = 'fixed';
          ta.style.left = '-9999px';
          ta.setAttribute('readonly', '');
          document.body.appendChild(ta);
          ta.select();
          copied = document.execCommand('copy');
          document.body.removeChild(ta);
        }
        if (copied) {
          alert('📋 飞书多维表格数据已成功复制到剪贴板！\n\n请直接打开飞书表格或多维表格，在空白处按 Ctrl+V (或 Cmd+V) 即可完整导入！');
        } else {
          alert('复制失败，请尝试直接点击【下载 CSV】: 剪贴板不可用');
        }
      } catch (err) {
        alert('复制失败，请尝试直接点击【下载 CSV】: ' + err.message);
      }
    });
  }

  // ==========================================
  // Super Admin 1: LLM Quota Settings (Task 17)
  // ==========================================
  const llmQuotaForm = document.getElementById('llmQuotaForm');
  const refreshQuotaBtn = document.getElementById('refreshQuotaBtn');

  async function loadLLMQuota() {
    if (!isSuperAdmin) return;
    try {
      const q = await API.getLLMQuota();
      if (!q) return;
      document.getElementById('quotaMaxTPM').value = q.max_tpm ?? 0;
      document.getElementById('quotaMaxRPM').value = q.max_rpm ?? 0;
      document.getElementById('quotaDayConcurrency').value = q.day_max_concurrency ?? q.day_concurrency ?? 2;
      document.getElementById('quotaNightConcurrency').value = q.night_max_concurrency ?? q.night_concurrency ?? 4;
      const timeoutEl = document.getElementById('quotaLLMTimeoutSec');
      if (timeoutEl) timeoutEl.value = q.llm_timeout_sec ?? 600;
      const retriesEl = document.getElementById('quotaLLMMaxRetries');
      if (retriesEl) retriesEl.value = q.llm_proxy_max_retries ?? 6;
      const count429 = q.rate_limit_429_count ?? q.throttled_retries ?? 0;
      const count502 = q.upstream_502_count ?? 0;
      const statusEl = document.getElementById('quotaStatusText');
      if (statusEl) {
        statusEl.innerHTML = `近 60 分钟 429 拦截与自适应降级: <span id="quotaThrottledCount" style="color: #f59e0b; font-weight: bold;">${count429}</span> 次 &nbsp;|&nbsp; 502/超时异常: <b>${count502}</b> 次 &nbsp;|&nbsp; 实时窗口: <b>${q.current_tpm ?? 0}</b> TPM / <b>${q.current_rpm ?? 0}</b> RPM &nbsp;|&nbsp; 历史峰值: <b>${q.peak_tpm ?? 0}</b> TPM / <b>${q.peak_rpm ?? 0}</b> RPM`;
      }
    } catch (e) {
      console.warn('loadLLMQuota error', e);
    }
  }

  if (refreshQuotaBtn) refreshQuotaBtn.addEventListener('click', loadLLMQuota);
  if (llmQuotaForm) {
    llmQuotaForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        const dayConc = Number(document.getElementById('quotaDayConcurrency').value);
        const nightConc = Number(document.getElementById('quotaNightConcurrency').value);
        const timeoutEl = document.getElementById('quotaLLMTimeoutSec');
        const retriesEl = document.getElementById('quotaLLMMaxRetries');
        const payload = {
          max_tpm: Number(document.getElementById('quotaMaxTPM').value),
          max_rpm: Number(document.getElementById('quotaMaxRPM').value),
          day_max_concurrency: dayConc,
          night_max_concurrency: nightConc,
          day_concurrency: dayConc,
          night_concurrency: nightConc
        };
        if (timeoutEl && timeoutEl.value !== '') {
          payload.llm_timeout_sec = Number(timeoutEl.value);
        }
        if (retriesEl && retriesEl.value !== '') {
          payload.llm_proxy_max_retries = Number(retriesEl.value);
        }
        await API.updateLLMQuota(payload);
        showAlert('LLM 算力配额、超时与日夜并发已成功实时生效！', 'success');
        loadLLMQuota();
      } catch (err) {
        alert('更新配额失败: ' + err.message);
      }
    });
  }

  // ==========================================
  // Super Admin: Unified Git Credentials
  // ==========================================
  const gitCredsForm = document.getElementById('gitCredsForm');
  const refreshGitCredsBtn = document.getElementById('refreshGitCredsBtn');
  const gitAuthEnabledInput = document.getElementById('gitAuthEnabled');
  const gitOverrideInlineInput = document.getElementById('gitOverrideInline');
  const gitSslNoVerifyInput = document.getElementById('gitSslNoVerify');
  const gitAuthTypeSelect = document.getElementById('gitAuthType');
  const gitHttpHostInput = document.getElementById('gitHttpHost');
  const gitHttpUsernameInput = document.getElementById('gitHttpUsername');
  const gitHttpSecretInput = document.getElementById('gitHttpSecret');
  const gitSecretLabel = document.getElementById('gitSecretLabel');
  const gitUsernameHint = document.getElementById('gitUsernameHint');
  const gitSecretState = document.getElementById('gitSecretState');
  const gitClearSecretWrap = document.getElementById('gitClearSecretWrap');
  const gitClearSecretInput = document.getElementById('gitClearSecret');
  const gitCredsSourceBadge = document.getElementById('gitCredsSourceBadge');
  const gitTestUrlInput = document.getElementById('gitTestUrl');
  const testGitCredsBtn = document.getElementById('testGitCredsBtn');
  const gitCredsAlert = document.getElementById('gitCredsAlert');

  let gitCredsState = { has_token: false, has_password: false, masked_secret: '' };

  function showGitCredsAlert(msg, type = 'success') {
    if (!gitCredsAlert) return;
    gitCredsAlert.textContent = msg;
    gitCredsAlert.className = 'alert alert-' + (type === 'success' ? 'success' : 'error');
    gitCredsAlert.style.display = 'block';
    setTimeout(() => {
      gitCredsAlert.style.display = 'none';
    }, 5000);
  }

  function updateGitAuthTypeUI() {
    if (!gitAuthTypeSelect) return;
    const isPassword = gitAuthTypeSelect.value === 'password';
    if (gitSecretLabel) gitSecretLabel.textContent = isPassword ? '登录密码' : 'Access Token';
    if (gitUsernameHint) gitUsernameHint.textContent = isPassword ? '(账号密码模式必填)' : '(Token 模式留空默认 oauth2)';
    const hasSaved = isPassword ? gitCredsState.has_password : gitCredsState.has_token;
    if (gitSecretState) {
      if (hasSaved && gitCredsState.masked_secret) {
        gitSecretState.textContent = `已配置 (${gitCredsState.masked_secret})`;
        gitSecretState.style.color = '#10b981';
      } else if (hasSaved) {
        gitSecretState.textContent = '已配置';
        gitSecretState.style.color = '#10b981';
      } else {
        gitSecretState.textContent = '未设置';
        gitSecretState.style.color = '#f59e0b';
      }
    }
    if (gitClearSecretWrap) {
      gitClearSecretWrap.style.display = hasSaved ? 'inline-flex' : 'none';
    }
    if (gitHttpSecretInput) {
      gitHttpSecretInput.placeholder = hasSaved
        ? '已保存凭据；留空保持不变，输入新值则覆盖'
        : (isPassword ? '请输入 Git 登录密码' : '请输入 Access Token');
    }
  }

  function applyGitCredsResponse(data) {
    if (!data) return;
    gitCredsState = {
      has_token: !!data.has_token,
      has_password: !!data.has_password,
      masked_secret: data.masked_secret || ''
    };
    if (gitAuthEnabledInput) gitAuthEnabledInput.checked = !!data.enabled;
    if (gitOverrideInlineInput) gitOverrideInlineInput.checked = data.override_inline !== false;
    if (gitSslNoVerifyInput) gitSslNoVerifyInput.checked = !!data.ssl_no_verify;
    if (gitAuthTypeSelect) gitAuthTypeSelect.value = data.auth_type === 'password' ? 'password' : 'token';
    if (gitHttpHostInput) gitHttpHostInput.value = data.host || '';
    if (gitHttpUsernameInput) gitHttpUsernameInput.value = data.username || '';
    if (gitHttpSecretInput) gitHttpSecretInput.value = '';
    if (gitClearSecretInput) gitClearSecretInput.checked = false;

    if (gitCredsSourceBadge) {
      const srcMap = {
        database: { text: data.enabled ? '已启用 · 数据库配置' : '已禁用 · 数据库配置', bg: data.enabled ? '#065f46' : '#475569' },
        env: { text: '来自环境变量默认值', bg: '#1d4ed8' },
        config: { text: '来自 config.yaml 默认值', bg: '#1d4ed8' },
        none: { text: '未配置', bg: '#334155' }
      };
      const meta = srcMap[data.source] || srcMap.none;
      gitCredsSourceBadge.textContent = meta.text;
      gitCredsSourceBadge.style.background = meta.bg;
    }
    updateGitAuthTypeUI();
  }

  async function loadGitCredentials() {
    if (!isSuperAdmin || !gitCredsForm) return;
    try {
      const data = await API.getGitCredentials();
      applyGitCredsResponse(data);
    } catch (e) {
      console.warn('loadGitCredentials error', e);
    }
  }

  function buildGitCredsPayload() {
    const authType = gitAuthTypeSelect ? gitAuthTypeSelect.value : 'token';
    const secretVal = gitHttpSecretInput ? gitHttpSecretInput.value : '';
    const clearSecret = gitClearSecretInput ? gitClearSecretInput.checked : false;
    const payload = {
      enabled: gitAuthEnabledInput ? gitAuthEnabledInput.checked : true,
      auth_type: authType,
      host: gitHttpHostInput ? gitHttpHostInput.value.trim() : '',
      username: gitHttpUsernameInput ? gitHttpUsernameInput.value.trim() : '',
      override_inline: gitOverrideInlineInput ? gitOverrideInlineInput.checked : true,
      ssl_no_verify: gitSslNoVerifyInput ? gitSslNoVerifyInput.checked : false,
    };
    if (authType === 'password') {
      if (clearSecret) payload.clear_password = true;
      else if (secretVal !== '') payload.password = secretVal;
    } else {
      if (clearSecret) payload.clear_token = true;
      else if (secretVal !== '') payload.token = secretVal;
    }
    return payload;
  }

  if (gitAuthTypeSelect) gitAuthTypeSelect.addEventListener('change', updateGitAuthTypeUI);
  if (refreshGitCredsBtn) refreshGitCredsBtn.addEventListener('click', loadGitCredentials);

  if (gitCredsForm) {
    gitCredsForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        const res = await API.updateGitCredentials(buildGitCredsPayload());
        applyGitCredsResponse(res);
        showGitCredsAlert('全局 Git 认证配置已保存并立即生效！', 'success');
      } catch (err) {
        showGitCredsAlert('保存失败: ' + err.message, 'error');
      }
    });
  }

  if (testGitCredsBtn) {
    testGitCredsBtn.addEventListener('click', async () => {
      const gitUrl = gitTestUrlInput ? gitTestUrlInput.value.trim() : '';
      if (!gitUrl) {
        showGitCredsAlert('请先输入一个仓库 HTTP/HTTPS 地址用于测试连接', 'error');
        return;
      }
      testGitCredsBtn.disabled = true;
      const origText = testGitCredsBtn.textContent;
      testGitCredsBtn.textContent = '⏳ 测试中...';
      try {
        const payload = buildGitCredsPayload();
        payload.git_url = gitUrl;
        const res = await API.testGitCredentials(payload);
        showGitCredsAlert(res.message || 'Git 仓库认证与连通性校验成功！', 'success');
      } catch (err) {
        showGitCredsAlert('连接测试失败: ' + err.message, 'error');
      } finally {
        testGitCredsBtn.disabled = false;
        testGitCredsBtn.textContent = origText;
      }
    });
  }

  // ==========================================
  // Super Admin 2: Storage Volumes & Migrations (Task 17)
  // ==========================================
  const refreshVolumesBtn = document.getElementById('refreshVolumesBtn');
  const generateRebalanceBtn = document.getElementById('generateRebalanceBtn');
  const volumesGrid = document.getElementById('volumesGrid');
  const rebalancePlanArea = document.getElementById('rebalancePlanArea');
  const rebalancePlanList = document.getElementById('rebalancePlanList');
  const applyRebalancePlanBtn = document.getElementById('applyRebalancePlanBtn');
  let currentRebalancePlan = [];

  // Storage Volume Modal elements
  const openAddVolumeModalBtn = document.getElementById('openAddVolumeModalBtn');
  const addVolumeModal = document.getElementById('addVolumeModal');
  const closeAddVolumeModalBtn = document.getElementById('closeAddVolumeModalBtn');
  const cancelAddVolumeBtn = document.getElementById('cancelAddVolumeBtn');
  const addVolumeForm = document.getElementById('addVolumeForm');
  const volumeIdInput = document.getElementById('volumeIdInput');
  const volumeMountPointInput = document.getElementById('volumeMountPointInput');
  const volumeModalTitle = document.getElementById('volumeModalTitle');
  const volumeAlert = document.getElementById('volumeAlert');

  function showVolumeAlert(msg, type = 'error') {
    if (!volumeAlert) return;
    volumeAlert.textContent = msg;
    volumeAlert.className = `alert alert-${type}`;
    volumeAlert.style.display = 'block';
  }

  function hideVolumeAlert() {
    if (volumeAlert) volumeAlert.style.display = 'none';
  }

  if (openAddVolumeModalBtn) {
    openAddVolumeModalBtn.addEventListener('click', () => {
      if (volumeModalTitle) volumeModalTitle.textContent = '➕ 注册新存储卷挂载点';
      if (volumeIdInput) {
        volumeIdInput.value = '';
        volumeIdInput.disabled = false;
        volumeIdInput.readOnly = false;
      }
      if (volumeMountPointInput) volumeMountPointInput.value = '';
      hideVolumeAlert();
      if (addVolumeModal) addVolumeModal.style.display = 'flex';
      setTimeout(() => volumeIdInput && volumeIdInput.focus(), 50);
    });
  }

  if (closeAddVolumeModalBtn) {
    closeAddVolumeModalBtn.addEventListener('click', () => {
      if (addVolumeModal) addVolumeModal.style.display = 'none';
    });
  }

  if (cancelAddVolumeBtn) {
    cancelAddVolumeBtn.addEventListener('click', () => {
      if (addVolumeModal) addVolumeModal.style.display = 'none';
    });
  }

  if (addVolumeForm) {
    addVolumeForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const id = volumeIdInput ? volumeIdInput.value.trim() : '';
      const mount_point = volumeMountPointInput ? volumeMountPointInput.value.trim() : '';
      if (!id || !mount_point) {
        showVolumeAlert('卷 ID 与挂载路径均为必填项', 'error');
        return;
      }
      try {
        await API.registerStorageVolume({ id, mount_point });
        if (addVolumeModal) addVolumeModal.style.display = 'none';
        showAlert(`存储卷 [${id}] 配置已保存并成功自动探测水位！`, 'success');
        loadStorageVolumes();
      } catch (err) {
        showVolumeAlert('保存存储卷失败: ' + err.message, 'error');
      }
    });
  }

  async function loadStorageVolumes() {
    if (!isSuperAdmin) return;
    try {
      const res = await API.getStorageVolumes();
      const vols = Array.isArray(res) ? res : (res && Array.isArray(res.volumes) ? res.volumes : []);
      if (!vols || vols.length === 0) {
        volumesGrid.innerHTML = '<div style="color:var(--text-secondary); padding:1.5rem; text-align:center; background:var(--bg-secondary); border-radius:8px; border:1px dashed var(--border-color); grid-column:1/-1;">暂无存储卷数据。请先配置或注册存储卷挂载点。</div>';
        return;
      }
      volumesGrid.innerHTML = vols.map(v => {
        const total = v.total_bytes || v.capacity_bytes || 0;
        const used = v.used_bytes || 0;
        const usedPct = total > 0 ? (used / total * 100) : 0;
        const color = usedPct > 80 ? '#ef4444' : (usedPct > 60 ? '#f59e0b' : '#10b981');
        const capGB = (total / (1024*1024*1024)).toFixed(1);
        const usedGB = (used / (1024*1024*1024)).toFixed(1);

        return `
          <div style="background:var(--bg-secondary); padding:1rem; border-radius:8px; border:1px solid var(--border-color); display:flex; flex-direction:column; justify-content:space-between;">
            <div>
              <div style="display:flex; justify-content:space-between; align-items:center;">
                <strong>💽 ${API.escapeHTML(v.id)}</strong>
                <span class="badge" style="background:${color}; color:#fff; font-size:0.75rem;">${usedPct.toFixed(1)}% 水位</span>
              </div>
              <div style="font-size:0.8rem; color:var(--text-secondary); margin:0.35rem 0; word-break:break-all;">
                挂载路径: <code>${API.escapeHTML(v.mount_point)}</code>
              </div>
              <div style="width:100%; height:6px; background:#334155; border-radius:3px; overflow:hidden; margin:0.5rem 0;">
                <div style="width:${Math.min(100, usedPct)}%; height:100%; background:${color};"></div>
              </div>
              <div style="font-size:0.8rem; color:var(--text-secondary); display:flex; justify-content:space-between;">
                <span>已用: ${usedGB} GB / ${capGB} GB</span>
                <span>状态: <span style="color:${v.status === 'active' ? 'var(--success-color)' : 'var(--text-secondary)'}; font-weight:600;">${v.status || 'active'}</span></span>
              </div>
            </div>
            <div style="margin-top:0.75rem; padding-top:0.5rem; border-top:1px dashed var(--border-color); display:flex; justify-content:flex-end; gap:0.5rem;">
              <button class="btn btn-secondary btn-sm edit-volume-btn" data-id="${API.escapeHTML(v.id)}" data-mount="${API.escapeHTML(v.mount_point)}">✏️ 修改路径</button>
              <button class="btn btn-danger btn-sm delete-volume-btn" data-id="${API.escapeHTML(v.id)}">🗑️ 删除卷</button>
            </div>
          </div>
        `;
      }).join('');

      // Wire up edit and delete buttons
      volumesGrid.querySelectorAll('.edit-volume-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const id = e.currentTarget.getAttribute('data-id');
          const mount = e.currentTarget.getAttribute('data-mount');
          if (volumeModalTitle) volumeModalTitle.textContent = `✏️ 修改存储卷挂载路径 [${id}]`;
          if (volumeIdInput) {
            volumeIdInput.value = id;
            volumeIdInput.disabled = true;
            volumeIdInput.readOnly = true;
          }
          if (volumeMountPointInput) volumeMountPointInput.value = mount || '';
          hideVolumeAlert();
          if (addVolumeModal) addVolumeModal.style.display = 'flex';
          setTimeout(() => volumeMountPointInput && volumeMountPointInput.focus(), 50);
        });
      });

      volumesGrid.querySelectorAll('.delete-volume-btn').forEach(btn => {
        btn.addEventListener('click', async (e) => {
          const id = e.currentTarget.getAttribute('data-id');
          if (!confirm(`确定要从存储池删除卷【${id}】吗？\n\n提示：此操作不会删除物理磁盘上的任何文件或目录，但会将关联到该卷的仓库重置为未指定卷。`)) {
            return;
          }
          try {
            await API.deleteStorageVolume(id);
            showAlert(`存储卷 [${id}] 已成功从存储池移除！`, 'success');
            loadStorageVolumes();
          } catch (err) {
            alert('删除存储卷失败: ' + err.message);
          }
        });
      });
    } catch (e) {
      console.warn('loadStorageVolumes error', e);
      if (volumesGrid) {
        volumesGrid.innerHTML = `<div style="color:var(--error-color); padding:1rem; grid-column:1/-1;">加载存储卷失败: ${API.escapeHTML(e.message)}</div>`;
      }
    }
  }

  if (refreshVolumesBtn) refreshVolumesBtn.addEventListener('click', loadStorageVolumes);
  if (generateRebalanceBtn) {
    generateRebalanceBtn.addEventListener('click', async () => {
      try {
        const planRes = await API.getRebalancePlan();
        currentRebalancePlan = planRes.plan || planRes.moves || (Array.isArray(planRes) ? planRes : []);
        if (currentRebalancePlan.length === 0) {
          rebalancePlanArea.style.display = 'block';
          rebalancePlanList.innerHTML = '<span style="color:var(--success-color);">✅ 当前各卷水位均衡，暂无需迁移的仓库！</span>';
          applyRebalancePlanBtn.style.display = 'none';
          return;
        }
        rebalancePlanArea.style.display = 'block';
        applyRebalancePlanBtn.style.display = 'inline-block';
        rebalancePlanList.innerHTML = currentRebalancePlan.map(m => `
          <div style="margin-bottom:0.4rem;">
            📦 仓库 <strong>${API.escapeHTML(m.repo_id)}</strong> (${(m.size_bytes / (1024*1024)).toFixed(1)} MB): 
            从 <code style="color:#ef4444;">${API.escapeHTML(m.from_volume_id || m.from_volume)}</code> 迁移到 
            <code style="color:#10b981;">${API.escapeHTML(m.to_volume_id || m.to_volume)}</code>
            ${m.reason ? `<span style="font-size:0.8rem; color:var(--text-secondary);">(${API.escapeHTML(m.reason)})</span>` : ''}
          </div>
        `).join('');
      } catch (err) {
        alert('生成均衡计划失败: ' + err.message);
      }
    });
  }

  if (applyRebalancePlanBtn) {
    applyRebalancePlanBtn.addEventListener('click', async () => {
      if (!currentRebalancePlan || currentRebalancePlan.length === 0) return;
      applyRebalancePlanBtn.disabled = true;
      applyRebalancePlanBtn.textContent = '⏳ 正在并发提交迁移任务...';
      try {
        for (const m of currentRebalancePlan) {
          await API.executeMigration({
            repo_id: m.repo_id,
            to_volume_id: m.to_volume_id || m.to_volume
          });
        }
        showAlert(`已触发 ${currentRebalancePlan.length} 个仓库平滑在线迁移任务！后台状态机正在运行。`, 'success');
        rebalancePlanArea.style.display = 'none';
        loadStorageVolumes();
      } catch (err) {
        alert('执行迁移失败: ' + err.message);
      } finally {
        applyRebalancePlanBtn.disabled = false;
        applyRebalancePlanBtn.textContent = '🚀 确认并一键执行批量迁移';
      }
    });
  }

  // ==========================================
  // Super Admin 3: User RBAC & Domains (Task 17)
  // ==========================================
  const usersTableBody = document.querySelector('#usersTable tbody');
  const refreshUsersBtn = document.getElementById('refreshUsersBtn');
  const userModal = document.getElementById('userModal');
  const closeUserModalBtn = document.getElementById('closeUserModalBtn');
  const cancelUserModalBtn = document.getElementById('cancelUserModalBtn');
  const userRbacForm = document.getElementById('userRbacForm');
  let selectedUserForModal = null;

  async function loadUsers() {
    if (!isSuperAdmin) return;
    try {
      const users = await API.getUsers();
      if (!users || users.length === 0) {
        usersTableBody.innerHTML = '<tr><td colspan="5" style="text-align:center; color:var(--text-secondary);">暂无用户</td></tr>';
        return;
      }
      usersTableBody.innerHTML = users.map(u => {
        const roleBadge = u.role === 'super_admin'
          ? '<span class="badge" style="background:#ef4444; color:#fff;">超管 (super_admin)</span>'
          : (u.role === 'group_leader'
            ? '<span class="badge" style="background:#3b82f6; color:#fff;">组长 (group_leader)</span>'
            : '<span class="badge" style="background:#64748b; color:#fff;">普通用户 (user)</span>');

        const domainTags = (u.authorized_domains || []).map(d => `
          <span class="badge" style="background:#1e293b; border:1px solid #475569; margin:2px;">${API.escapeHTML(d)}</span>
        `).join('') || '<span style="color:var(--text-secondary);">-</span>';

        return `
          <tr>
            <td><strong>${API.escapeHTML(u.username)}</strong></td>
            <td>${API.escapeHTML(u.display_name || '-')}</td>
            <td>${roleBadge}</td>
            <td>${domainTags}</td>
            <td class="col-center col-nowrap">
              <button class="btn btn-secondary btn-sm manage-user-btn" data-username="${API.escapeHTML(u.username)}">⚙️ 角色与授权</button>
            </td>
          </tr>
        `;
      }).join('');

      document.querySelectorAll('.manage-user-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const uname = e.currentTarget.getAttribute('data-username');
          const user = users.find(x => x.username === uname);
          if (user) openUserModal(user);
        });
      });
    } catch (e) {
      console.warn('loadUsers error', e);
    }
  }

  function openUserModal(user) {
    selectedUserForModal = user;
    document.getElementById('userModalUsername').value = user.username;
    document.getElementById('userModalUserDesc').textContent = `${user.display_name} (${user.username})`;
    document.getElementById('userModalRole').value = user.role || 'user';
    renderUserModalDomains(user.authorized_domains || []);
    document.getElementById('userModalAlert').style.display = 'none';
    userModal.style.display = 'flex';
  }

  function renderUserModalDomains(domains) {
    const list = document.getElementById('userDomainsList');
    if (!domains || domains.length === 0) {
      list.innerHTML = '<span style="color:var(--text-secondary); font-size:0.85rem;">暂无授权业务域</span>';
      return;
    }
    list.innerHTML = domains.map(d => `
      <span class="badge" style="background:#3b82f6; color:#fff; display:inline-flex; align-items:center; gap:0.25rem; padding:0.2rem 0.5rem; border-radius:4px;">
        ${API.escapeHTML(d)}
        <span class="revoke-domain-btn" data-domain="${API.escapeHTML(d)}" style="cursor:pointer; font-weight:bold;">&times;</span>
      </span>
    `).join('');

    list.querySelectorAll('.revoke-domain-btn').forEach(b => {
      b.addEventListener('click', async (e) => {
        const dom = e.currentTarget.getAttribute('data-domain');
        try {
          await API.revokeDomain(selectedUserForModal.username, dom);
          selectedUserForModal.authorized_domains = selectedUserForModal.authorized_domains.filter(x => x !== dom);
          renderUserModalDomains(selectedUserForModal.authorized_domains);
          loadUsers();
        } catch (err) {
          alert('收回失败: ' + err.message);
        }
      });
    });
  }

  document.getElementById('addDomainBtn')?.addEventListener('click', async () => {
    const input = document.getElementById('newDomainInput');
    const val = input.value.trim();
    if (!val || !selectedUserForModal) return;
    try {
      await API.grantDomain(selectedUserForModal.username, val);
      if (!selectedUserForModal.authorized_domains) selectedUserForModal.authorized_domains = [];
      if (!selectedUserForModal.authorized_domains.includes(val)) selectedUserForModal.authorized_domains.push(val);
      input.value = '';
      renderUserModalDomains(selectedUserForModal.authorized_domains);
      loadUsers();
    } catch (err) {
      alert('授权失败: ' + err.message);
    }
  });

  if (closeUserModalBtn) closeUserModalBtn.addEventListener('click', () => userModal.style.display = 'none');
  if (cancelUserModalBtn) cancelUserModalBtn.addEventListener('click', () => userModal.style.display = 'none');
  if (userRbacForm) {
    userRbacForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const role = document.getElementById('userModalRole').value;
      try {
        await API.setUserRole(selectedUserForModal.username, role);
        userModal.style.display = 'none';
        showAlert(`用户 [${selectedUserForModal.username}] 角色已设置为 ${role}！`, 'success');
        loadUsers();
      } catch (err) {
        alert('保存角色失败: ' + err.message);
      }
    });
  }
  if (refreshUsersBtn) refreshUsersBtn.addEventListener('click', loadUsers);

  // ==========================================
  // Super Admin 4: Log Disk Cleanup (Task 17)
  // ==========================================
  const refreshLogStatsBtn = document.getElementById('refreshLogStatsBtn');
  const cleanupLogsBtn = document.getElementById('cleanupLogsBtn');
  const cleanupDaysInput = document.getElementById('cleanupDaysInput');
  const logDiskStatsText = document.getElementById('logDiskStatsText');

  async function loadLogStats() {
    if (!isSuperAdmin) return;
    try {
      const stats = await API.getLogStats();
      if (!stats) return;
      const sizeMB = (stats.total_bytes / (1024*1024)).toFixed(1);
      logDiskStatsText.innerHTML = `
        <span style="color:#38bdf8;">${sizeMB} MB</span> 
        <span style="font-size:0.85rem; color:var(--text-secondary); font-weight:normal;">(${stats.file_count || 0} 个日志文件 · 挂载点: <code>${API.escapeHTML(stats.log_dir)}</code>)</span>
      `;
    } catch (e) {
      console.warn('loadLogStats error', e);
    }
  }

  if (refreshLogStatsBtn) refreshLogStatsBtn.addEventListener('click', loadLogStats);
  if (cleanupLogsBtn) {
    cleanupLogsBtn.addEventListener('click', async () => {
      const days = Number(cleanupDaysInput.value) || 7;
      if (!confirm(`确定要清理日志盘中超过 ${days} 天的历史流水线日志吗？此操作不可逆！`)) return;
      cleanupLogsBtn.disabled = true;
      cleanupLogsBtn.textContent = '⏳ 清理中...';
      try {
        const res = await API.cleanupLogs({ older_than_days: days });
        const freedMB = ((res.deleted_bytes || 0) / (1024*1024)).toFixed(1);
        showAlert(`已成功清理 ${res.deleted_files || 0} 个过期日志文件，释放空间 ${freedMB} MB！`, 'success');
        loadLogStats();
      } catch (err) {
        alert('清理日志失败: ' + err.message);
      } finally {
        cleanupLogsBtn.disabled = false;
        cleanupLogsBtn.textContent = '🗑️ 一键安全清理';
      }
    });
  }

  // ==========================================
  // Business Domain Dictionary Management
  // ==========================================
  const domainDictSection = document.getElementById('domainDictSection');
  const domainsTableBody = document.querySelector('#domainsTable tbody');
  const refreshDomainsBtn = document.getElementById('refreshDomainsBtn');
  const addDomainBtn = document.getElementById('addDomainBtn');
  const domainAlert = document.getElementById('domainAlert');

  function showDomainAlert(msg, type) {
    if (!domainAlert) return;
    domainAlert.textContent = msg;
    domainAlert.className = 'alert alert-' + (type === 'success' ? 'success' : 'error');
    domainAlert.style.display = 'block';
    setTimeout(() => { domainAlert.style.display = 'none'; }, 3000);
  }

  async function loadDomains() {
    if (!domainsTableBody) return;
    domainsTableBody.innerHTML = '<tr><td colspan="6" style="text-align:center; color:var(--text-secondary);">正在加载业务域字典...</td></tr>';
    try {
      const list = await API.getDomains();
      configuredDomainsCache = (list || []).map(d => d.name); // refresh cache
      domainCodeByNameMap = new Map();
      (list || []).forEach(d => {
        if (d.name && d.code) domainCodeByNameMap.set(d.name, String(d.code).toUpperCase());
      });
      populateDomainDropdown(repoDomainSelect, repoCustomDomainInput, repoDomainSelect ? repoDomainSelect.value : '', true).catch(() => {});
      if (!list || list.length === 0) {
        domainsTableBody.innerHTML = '<tr><td colspan="6" style="text-align:center; color:var(--text-secondary);">尚未配置业务域。新增后将作为仓库导入与项目编排的下拉选项。</td></tr>';
        return;
      }
      domainsTableBody.innerHTML = list.map(d => `
        <tr data-id="${d.id}">
          <td><strong>${API.escapeHTML(d.name)}</strong></td>
          <td class="col-center col-nowrap">${d.code ? `<span class="badge" style="background:rgba(99,102,241,0.18); color:#818cf8; border:1px solid rgba(99,102,241,0.4);">${API.escapeHTML(d.code)}</span>` : '<span style="color:var(--text-secondary);">-</span>'}</td>
          <td>${API.escapeHTML(d.description || '-')}</td>
          <td class="col-center col-nowrap">${d.sort_order || 0}</td>
          <td class="col-center col-nowrap">${d.status === 'active' ? '<span style="color:var(--success-color);">启用</span>' : '<span style="color:var(--text-secondary);">停用</span>'}</td>
          <td class="col-center col-nowrap">
            <button class="btn btn-secondary btn-sm edit-domain-btn" data-id="${d.id}">✏️ 编辑</button>
            <button class="btn btn-danger btn-sm delete-domain-btn" data-id="${d.id}" data-name="${API.escapeHTML(d.name)}">🗑️ 删除</button>
          </td>
        </tr>
      `).join('');
      // wire actions
      domainsTableBody.querySelectorAll('.delete-domain-btn').forEach(btn => {
        btn.addEventListener('click', async (e) => {
          const id = e.currentTarget.getAttribute('data-id');
          const name = e.currentTarget.getAttribute('data-name');
          if (!confirm(`确认删除业务域【${name}】？已绑定该域的仓库/项目不受影响，但下拉选项将移除。`)) return;
          try {
            await API.deleteDomain(id);
            showDomainAlert('业务域已删除', 'success');
            loadDomains();
          } catch (err) {
            showDomainAlert('删除失败: ' + err.message, 'error');
          }
        });
      });
      domainsTableBody.querySelectorAll('.edit-domain-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const id = e.currentTarget.getAttribute('data-id');
          const row = list.find(d => String(d.id) === id);
          if (!row) return;
          document.getElementById('newDomainName').value = row.name;
          const codeEl = document.getElementById('newDomainCode');
          if (codeEl) codeEl.value = row.code || '';
          document.getElementById('newDomainDesc').value = row.description || '';
          document.getElementById('newDomainOrder').value = row.sort_order || 0;
          addDomainBtn.dataset.editId = id;
          addDomainBtn.textContent = '💾 保存修改';
          document.getElementById('newDomainName').focus();
        });
      });
    } catch (err) {
      domainsTableBody.innerHTML = `<tr><td colspan="6" style="color:var(--error-color); text-align:center;">加载业务域失败: ${API.escapeHTML(err.message)}</td></tr>`;
    }
  }

  if (refreshDomainsBtn) refreshDomainsBtn.addEventListener('click', loadDomains);
  if (addDomainBtn) {
    addDomainBtn.addEventListener('click', async () => {
      const name = document.getElementById('newDomainName').value.trim();
      const code = ((document.getElementById('newDomainCode') || {}).value || '').trim().toUpperCase();
      const desc = document.getElementById('newDomainDesc').value.trim();
      const order = parseInt(document.getElementById('newDomainOrder').value || '0', 10);
      if (!name) { showDomainAlert('业务域名称不能为空', 'error'); return; }
      try {
        if (addDomainBtn.dataset.editId) {
          await API.updateDomain({ id: parseInt(addDomainBtn.dataset.editId, 10), name, code, description: desc, sort_order: order, status: 'active' });
          showDomainAlert('业务域已更新', 'success');
          delete addDomainBtn.dataset.editId;
          addDomainBtn.textContent = '➕ 新增业务域';
        } else {
          await API.createDomain({ name, code, description: desc, sort_order: order, status: 'active' });
          showDomainAlert('业务域已新增', 'success');
        }
        document.getElementById('newDomainName').value = '';
        const codeEl = document.getElementById('newDomainCode');
        if (codeEl) codeEl.value = '';
        document.getElementById('newDomainDesc').value = '';
        document.getElementById('newDomainOrder').value = '0';
        loadDomains();
      } catch (err) {
        showDomainAlert('操作失败: ' + err.message, 'error');
      }
    });
  }

  // Initial load & Polling
  refreshBatchStatus();
  loadDailyTokens();
  loadLLMQuota();
  loadGitCredentials();
  loadStorageVolumes();
  loadUsers();
  if (isSuperAdmin) loadDomains();
  loadLogStats();
  batchPollTimer = setInterval(() => {
    refreshBatchStatus();
    loadDailyTokens();
    if (activeBatchId) {
      loadAdminProjects();
    }
  }, 4000);
});
