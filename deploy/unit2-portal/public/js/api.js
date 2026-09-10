// Shared API Client Wrapper for Portal & Orchestrator REST/SSE endpoints
window.API = {
  // Fetch repos list via Portal proxy
  async getRepos() {
    const res = await fetch('/portal/repos');
    if (!res.ok) throw new Error('无法获取仓库列表 (' + res.status + ')');
    return await res.json();
  },

  // Register new repo via Orchestrator API
  async registerRepo(repoData) {
    const res = await fetch('/portal/repos', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(repoData)
    });
    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || '注册仓库失败');
    }
    return await res.json();
  },

  // Update existing repo configuration
  async updateRepo(repoData) {
    const res = await fetch('/portal/repos', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(repoData)
    });
    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || '更新仓库失败');
    }
    return await res.json();
  },

  // Delete repo configuration
  async deleteRepo(repoId) {
    const res = await fetch(`/portal/repos?id=${encodeURIComponent(repoId)}`, {
      method: 'DELETE'
    });
    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || '删除仓库失败');
    }
    return await res.json();
  },

  // Trigger manual build for a repo
  async triggerBuild(repoId) {
    const res = await fetch('/api/build/trigger', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo_id: repoId })
    });
    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || '触发构建失败');
    }
    return await res.json();
  },

  // Fetch build history for a repo
  async getBuildHistory(repoId) {
    const res = await fetch(`/api/build/status?repo_id=${encodeURIComponent(repoId)}`);
    if (!res.ok) throw new Error('无法获取构建历史 (' + res.status + ')');
    return await res.json();
  },

  // Fetch QA Chat History
  async getQASessions(repoID) {
    const res = await fetch(`/portal/sessions?repo_id=${encodeURIComponent(repoID)}`);
    if (!res.ok) throw new Error('无法获取问答历史 (' + res.status + ')');
    return await res.json();
  },

  // Format timestamp into local YYYY-MM-DD HH:mm:ss
  formatDate(dateStr) {
    if (!dateStr) return '-';
    // If dateStr is already formatted as local "YYYY-MM-DD HH:mm:ss", return directly
    if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(dateStr)) {
      return dateStr;
    }
    try {
      const d = new Date(dateStr);
      if (isNaN(d.getTime())) return dateStr;
      return d.getFullYear() + '-' +
        String(d.getMonth() + 1).padStart(2, '0') + '-' +
        String(d.getDate()).padStart(2, '0') + ' ' +
        String(d.getHours()).padStart(2, '0') + ':' +
        String(d.getMinutes()).padStart(2, '0') + ':' +
        String(d.getSeconds()).padStart(2, '0');
    } catch (e) {
      return dateStr;
    }
  },

  // Helper for escaping HTML special characters
  escapeHTML(str) {
    return (str || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }
};
