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
      branch: document.getElementById('branch').value.trim(),
      local_path: localPathInput.value.trim(),
      schedule: document.getElementById('schedule').value.trim()
    };

    try {
      await API.registerRepo(repoData);
      showAlert('仓库注册成功，已成功加入 Cron 调度队列！', 'success');
      registerForm.reset();
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

  let cachedRepos = [];

  // Load Admin Repos Table with Latest Build Status
  async function loadAdminRepos() {
    try {
      const repos = await API.getRepos();
      cachedRepos = repos || [];
      if (!repos || repos.length === 0) {
        adminTableBody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-secondary);">暂无已注册仓库。</td></tr>';
        return;
      }

      // Fetch latest builds for all repos concurrently
      const buildPromises = repos.map(r => API.getBuildHistory(r.id).catch(() => []));
      const buildsList = await Promise.all(buildPromises);

      adminTableBody.innerHTML = repos.map((repo, i) => {
        const builds = buildsList[i] || [];
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
            <td>
              <strong>${API.escapeHTML(repo.name)}</strong>
              <div style="font-size:0.75rem; color:var(--text-secondary);">${API.escapeHTML(repo.id)}</div>
            </td>
            <td>
              <code>${API.escapeHTML(repo.git_url)}</code>
              <div><span class="badge">${API.escapeHTML(repo.branch)}</span></div>
            </td>
            <td><code style="font-size:0.8rem;">${API.escapeHTML(repo.local_path)}</code></td>
            <td><code>${API.escapeHTML(repo.schedule)}</code></td>
            <td>
              <span class="badge" style="background:${repo.status === 'active' ? 'rgba(74, 222, 128, 0.15)' : 'rgba(248, 113, 113, 0.15)'}; color:${repo.status === 'active' ? 'var(--success-color)' : 'var(--error-color)'}">
                ${API.escapeHTML(repo.status)}
              </span>
            </td>
            <td>${buildBadge}</td>
            <td>
              <div style="display:flex; gap:0.35rem; flex-wrap:wrap;">
                <button class="btn btn-secondary btn-sm trigger-build-btn" data-id="${repo.id}">
                  ⚡ 立即构建
                </button>
                <button class="btn btn-outline btn-sm view-log-btn" data-id="${repo.id}">
                  📋 日志
                </button>
                <button class="btn btn-secondary btn-sm edit-repo-btn" data-id="${repo.id}">
                  ✏️ 编辑
                </button>
                <button class="btn btn-secondary btn-sm delete-repo-btn" data-id="${repo.id}" style="color:var(--error-color); border-color:rgba(248,113,113,0.4);">
                  🗑️ 删除
                </button>
              </div>
            </td>
          </tr>
        `;
      }).join('');

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
          document.getElementById('editBranch').value = targetRepo.branch || 'main';
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
});
