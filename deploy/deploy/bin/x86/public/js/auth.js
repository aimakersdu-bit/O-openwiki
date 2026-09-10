// Shared Authentication & Session Manager
window.Auth = {
  // Check session status. If unauthenticated and redirectIfUnauth is true, redirect to login.html
  async checkAuth(redirectIfUnauth = true) {
    try {
      const res = await fetch('/portal/me');
      if (res.ok) {
        const user = await res.json();
        return user;
      }
    } catch (e) {
      console.warn('Auth check error:', e);
    }

    if (redirectIfUnauth) {
      window.location.href = '/portal/login.html';
    }
    return null;
  },

  // Perform login
  async login(username, password) {
    const res = await fetch('/portal/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });
    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || '登录失败，请检查账号和密码');
    }
    return await res.json();
  },

  // Perform logout and redirect to login.html
  async logout() {
    try {
      await fetch('/portal/logout', { method: 'POST' });
    } catch (e) {
      console.warn('Logout request failed:', e);
    }
    window.location.href = '/portal/login.html';
  },

  // Update navbar user badge and toggle role-based UI elements
  renderNavbarUser(user) {
    const userContainer = document.getElementById('navbarUser');

    // Toggle admin-only elements
    const adminElements = document.querySelectorAll('.admin-only');
    adminElements.forEach(el => {
      if (user && user.role === 'admin') {
        el.style.display = '';
      } else {
        el.style.display = 'none';
      }
    });

    if (!userContainer) return;

    if (user) {
      const name = user.display_name || user.user_id;
      const initial = name.charAt(0).toUpperCase();
      const roleBadge = user.role === 'admin' ? '<span class="badge badge-admin" style="background:#0284c7;color:#fff;font-size:10px;padding:2px 6px;border-radius:4px;margin-left:4px;">管理员</span>' : '<span class="badge badge-user" style="background:#64748b;color:#fff;font-size:10px;padding:2px 6px;border-radius:4px;margin-left:4px;">普通用户</span>';
      userContainer.innerHTML = `
        <div class="user-info" style="display:flex;align-items:center;gap:8px;">
          <span class="user-avatar">${initial}</span>
          <span class="username">${name}${roleBadge}</span>
          <button onclick="Auth.logout()" class="btn btn-outline btn-sm">退出</button>
        </div>
      `;
    } else {
      userContainer.innerHTML = `<a href="/portal/login.html" class="btn btn-primary btn-sm">登录</a>`;
    }
  }
};
