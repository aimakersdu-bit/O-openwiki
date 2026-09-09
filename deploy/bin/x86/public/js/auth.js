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

  // Update navbar user badge
  renderNavbarUser(user) {
    const userContainer = document.getElementById('navbarUser');
    if (!userContainer) return;

    if (user) {
      const name = user.display_name || user.user_id;
      const initial = name.charAt(0).toUpperCase();
      userContainer.innerHTML = `
        <div class="user-info">
          <span class="user-avatar">${initial}</span>
          <span class="username">${name}</span>
          <button onclick="Auth.logout()" class="btn btn-outline btn-sm">退出</button>
        </div>
      `;
    } else {
      userContainer.innerHTML = `<a href="/portal/login.html" class="btn btn-primary btn-sm">登录</a>`;
    }
  }
};
