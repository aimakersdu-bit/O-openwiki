document.addEventListener('DOMContentLoaded', async () => {
  // Check if user is already logged in
  const user = await Auth.checkAuth(false);
  if (user) {
    window.location.href = 'index.html';
    return;
  }

  const loginForm = document.getElementById('loginForm');
  const loginError = document.getElementById('loginError');

  loginForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    loginError.style.display = 'none';

    const username = document.getElementById('username').value.trim();
    const password = document.getElementById('password').value;

    try {
      await Auth.login(username, password);
      window.location.href = 'index.html';
    } catch (err) {
      loginError.textContent = err.message;
      loginError.style.display = 'block';
    }
  });
});
