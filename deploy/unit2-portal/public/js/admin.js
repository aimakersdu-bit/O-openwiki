document.addEventListener('DOMContentLoaded', async () => {
  // Check auth and render user in navbar
  const currentUser = await Auth.checkAuth(true);
  if (currentUser && currentUser.role !== 'admin') {
    alert('权限不足：仓库配置与管理界面仅允许系统管理员访问！');
    window.location.href = '/portal/index.html';
    return;
  }
  Auth.renderNavbarUser(currentUser);

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

  // Register Repo Form Handler
  registerForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    adminAlert.style.display = 'none';

    const repoData = {
      id: repoIdInput.value.trim(),
      name: document.getElementById('repoName').value.trim(),
      git_url: document.getElementById('gitUrl').value.trim(),
      branch: document.getElementById('branch').value.trim() || 'master',
      local_path: localPathInput.value.trim(),
      schedule: document.getElementById('schedule').value.trim()
    };

    try {
      await API.registerRepo(repoData);
      showAlert('仓库注册成功，已成功加入 Cron 调度队列！', 'success');
      registerForm.reset();
      // 保持添加仓库默认分支为 master
      document.getElementById('branch').value = 'master';
      loadAdminRepos();
    } catch (err) {
      showAlert(err.message, 'error');
    }
  });

  // Edit Repo Form Handler
  editRepoForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    editAlert.style.display = 'none';

    const repoData = {
      id: document.getElementById('editRepoId').value,
      name: document.getElementById('editRepoName').value.trim(),
      git_url: document.getElementById('editGitUrl').value.trim(),
      branch: document.getElementById('editBranch').value.trim(),
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
  let cachedRepos = [];
  let cachedBuildsMap = new Map();
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
            <div style="font-size:0.75rem; color:var(--text-secondary);">${API.escapeHTML(repo.id)}</div>
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
      btn.addEventListener('click', async (e) => {
        const repoId = e.currentTarget.getAttribute('data-id');
        logModalTitle.textContent = `📋 构建日志详情: ${repoId}`;
        logModalMeta.textContent = '加载日志中...';
        logModalContent.textContent = '';
        logModal.style.display = 'flex';

        try {
          const builds = await API.getBuildHistory(repoId);
          if (!builds || builds.length === 0) {
            logModalMeta.textContent = '该仓库暂无历史构建记录';
            logModalContent.textContent = '无构建日志信息';
            return;
          }

          const latest = builds[0];
          logModalMeta.innerHTML = `
            <div><strong>构建状态:</strong> ${latest.status}</div>
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

  function showProjAlert(msg, type) {
    projAlert.textContent = msg;
    projAlert.className = `alert alert-${type}`;
    projAlert.style.display = 'block';
  }

  function renderReposPicker(selectedRepos = []) {
    const selectedMap = new Map();
    selectedRepos.forEach(r => selectedMap.set(r.id, r));

    if (!cachedRepos || cachedRepos.length === 0) {
      projReposPicker.innerHTML = '<div style="color:var(--text-secondary); padding:0.5rem;">尚未注册任何代码仓库，请先在上方注册仓库。</div>';
      return;
    }

    let html = '<div style="display:flex; flex-direction:column; gap:0.5rem;">';
    cachedRepos.forEach(repo => {
      const isChecked = selectedMap.has(repo.id);
      const existing = selectedMap.get(repo.id) || {};
      const aliasVal = existing.alias_name || repo.id;
      const roleVal = existing.role || 'service';

      html += `
        <div class="repo-picker-item" style="display:flex; align-items:center; gap:0.75rem; padding:0.4rem; border-radius:4px; background:var(--bg-primary); border:1px solid var(--border-color);">
          <input type="checkbox" class="proj-repo-check" data-id="${repo.id}" id="check_${repo.id}" ${isChecked ? 'checked' : ''} style="cursor:pointer;" />
          <label for="check_${repo.id}" style="font-weight:600; min-width:140px; cursor:pointer;">${API.escapeHTML(repo.name)} <span style="font-size:0.8rem; color:var(--text-secondary);">(${repo.id})</span></label>
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

  // Open Create Project Modal
  openNewProjectModalBtn.addEventListener('click', () => {
    document.getElementById('projectModalTitle').textContent = '🏢 创建微服务项目';
    document.getElementById('projIsEdit').value = '0';
    document.getElementById('projId').value = '';
    document.getElementById('projId').disabled = false;
    document.getElementById('projName').value = '';
    document.getElementById('projDesc').value = '';
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
    const projDesc = document.getElementById('projDesc').value.trim();

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

    const payload = {
      id: projId,
      name: projName,
      description: projDesc,
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

  // Render Projects Table
  function renderProjectTable(projects = []) {
    if (!projects || projects.length === 0) {
      adminProjectTableBody.innerHTML = '<tr><td colspan="6" style="text-align:center; color: var(--text-secondary); padding: 2rem;">暂无微服务项目，点击右上角“创建微服务项目”开始编排。</td></tr>';
      return;
    }

    adminProjectTableBody.innerHTML = projects.map(proj => {
      const reposBadges = (proj.repos || []).map(r => `
        <span class="badge" style="background:var(--bg-secondary); border:1px solid var(--border-color); margin-right:4px; margin-bottom:4px; display:inline-block; font-size:0.75rem;">
          <strong>${API.escapeHTML(r.alias_name)}</strong>
          <span style="opacity:0.7;">(${r.role})</span>
        </span>
      `).join('') || '<span style="color:var(--text-secondary);">尚未绑定仓库</span>';

      const statusBadge = proj.status === 'building'
        ? '<span style="color:var(--accent-color);">⚡ 聚合构建中...</span>'
        : '<span style="color:var(--success-color);">✅ 就绪</span>';

      return `
        <tr>
          <td>
            <strong>${API.escapeHTML(proj.name)}</strong>
            <div style="font-size:0.75rem; color:var(--text-secondary);">${API.escapeHTML(proj.id)}</div>
            ${proj.description ? `<div style="font-size:0.75rem; color:var(--text-secondary); margin-top:2px;">${API.escapeHTML(proj.description)}</div>` : ''}
          </td>
          <td>${reposBadges}</td>
          <td style="font-family:monospace; font-size:0.8rem; word-break:break-all;">${API.escapeHTML(proj.local_path || '-')}</td>
          <td class="col-center col-nowrap">${statusBadge}</td>
          <td class="col-center col-nowrap" style="font-size:0.8rem;">${API.formatDate(proj.updated_at)}</td>
          <td class="col-center col-nowrap">
            <button class="btn btn-primary btn-sm trigger-project-build-btn" data-id="${proj.id}" data-name="${API.escapeHTML(proj.name)}">🛠️ 聚合构建</button>
            <button class="btn btn-secondary btn-sm edit-project-btn" data-id="${proj.id}">✏️ 编辑</button>
            <button class="btn btn-danger btn-sm delete-project-btn" data-id="${proj.id}">🗑️ 删除</button>
          </td>
        </tr>
      `;
    }).join('');

    // Attach listeners
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
        document.getElementById('projDesc').value = proj.description || '';
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

  // Load Projects
  async function loadAdminProjects() {
    try {
      const projects = await API.getProjects();
      cachedProjects = projects || [];
      renderProjectTable(cachedProjects);
    } catch (err) {
      adminProjectTableBody.innerHTML = `<tr><td colspan="6" style="color:var(--error-color);">加载微服务项目失败: ${err.message}</td></tr>`;
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
  const pauseBatchBtn = document.getElementById('pauseBatchBtn');
  const resumeBatchBtn = document.getElementById('resumeBatchBtn');
  const cancelBatchBtn = document.getElementById('cancelBatchBtn');
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
          last_release_at: parts[6] || ''
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
      batchStatusBadge.textContent = '无正在运行的批跑';
      batchStatusBadge.style.background = '#64748b';
      batchETABadge.textContent = '-';
      batchProgressText.textContent = '0 / 0 (0%)';
      batchProgressBar.style.width = '0%';
      pauseBatchBtn.style.display = 'none';
      resumeBatchBtn.style.display = 'none';
      cancelBatchBtn.style.display = 'none';
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
      if (run.status === 'running') {
        pauseBatchBtn.style.display = 'inline-block';
        resumeBatchBtn.style.display = 'none';
        cancelBatchBtn.style.display = 'inline-block';
      } else if (run.status === 'paused') {
        pauseBatchBtn.style.display = 'none';
        resumeBatchBtn.style.display = 'inline-block';
        cancelBatchBtn.style.display = 'inline-block';
      } else {
        pauseBatchBtn.style.display = 'none';
        resumeBatchBtn.style.display = 'none';
        cancelBatchBtn.style.display = 'none';
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

  // Copy Feishu TSV to Clipboard
  if (copyFeishuTSVBtn) {
    copyFeishuTSVBtn.addEventListener('click', async () => {
      try {
        const tsv = await API.getTokensExportTSV();
        await navigator.clipboard.writeText(tsv);
        alert('📋 飞书多维表格数据已成功复制到剪贴板！\n\n请直接打开飞书表格或多维表格，在空白处按 Ctrl+V (或 Cmd+V) 即可完整导入！');
      } catch (err) {
        alert('复制失败，请尝试直接点击【下载 CSV】: ' + err.message);
      }
    });
  }

  // Initial load & Polling
  refreshBatchStatus();
  loadDailyTokens();
  batchPollTimer = setInterval(() => {
    refreshBatchStatus();
    loadDailyTokens();
  }, 4000);
});

