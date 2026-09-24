const lessons = {
  blank: `<html>
</html>`,
  welcome: `<!DOCTYPE html>
<html>
  <head>
    <style>
      body {
        font-family: Arial, sans-serif;
        text-align: center;
        padding: 50px 20px;
        background: #fff8e7;
        color: #233347;
      }

      h1 { color: #ff6b55; }
      p { font-size: 20px; }
    </style>
  </head>
  <body>
    <h1>Hello, world! 👋</h1>
    <p>I made my first web page.</p>
  </body>
</html>`,
  colors: `<!DOCTYPE html>
<html>
  <head>
    <style>
      body {
        font-family: Arial, sans-serif;
        padding: 40px;
        background: #eaf4ff;
      }

      h1 { color: #3157d5; }

      .rainbow {
        padding: 24px;
        border-radius: 18px;
        color: white;
        background: linear-gradient(135deg, #ff6b55, #9b5de5, #3f7ee8);
      }
    </style>
  </head>
  <body>
    <h1>CSS adds color! 🎨</h1>
    <div class="rainbow">Try changing these colors.</div>
  </body>
</html>`,
  card: `<!DOCTYPE html>
<html>
  <head>
    <style>
      body {
        font-family: Arial, sans-serif;
        display: grid;
        min-height: 80vh;
        place-items: center;
        background: #fff4dc;
      }

      .card {
        width: 220px;
        padding: 28px;
        border: 3px solid #233347;
        border-radius: 20px;
        background: white;
        box-shadow: 8px 8px 0 #ffc857;
      }
    </style>
  </head>
  <body>
    <div class="card">
      <h1>Space Cat 🚀</h1>
      <p>Explorer of the CSS galaxy.</p>
    </div>
  </body>
</html>`
};

const editor = document.querySelector('#code-editor');
const preview = document.querySelector('#preview');
const lessonButtons = document.querySelectorAll('.lesson');
const resetButton = document.querySelector('#reset-button');
const saveButton = document.querySelector('#save-button');
const pageTitleInput = document.querySelector('#page-title-input');
const authButton = document.querySelector('#auth-button');
const pagesButton = document.querySelector('#pages-button');
const userEmail = document.querySelector('#user-email');
const authDialog = document.querySelector('#auth-dialog');
const pagesDialog = document.querySelector('#pages-dialog');
const authForm = document.querySelector('#auth-form');
const authMessage = document.querySelector('#auth-message');
const pagesMessage = document.querySelector('#pages-message');
const savedPages = document.querySelector('#saved-pages');
const toast = document.querySelector('#toast');

let currentLesson = 'blank';
let currentPageID = null;
let signedInUser = null;
let renderTimer;
let tabExitsEditor = false;
let toastTimer;

function renderPreview() {
  preview.srcdoc = editor.value;
}

function schedulePreview() {
  window.clearTimeout(renderTimer);
  renderTimer = window.setTimeout(renderPreview, 120);
}

function selectLesson(lessonName) {
  window.clearTimeout(renderTimer);
  currentLesson = lessonName;
  editor.value = lessons[lessonName];
  currentPageID = null;
  pageTitleInput.value = lessonName === 'welcome' ? 'My first page' : document.querySelector(`[data-lesson="${lessonName}"] strong`).textContent;
  lessonButtons.forEach((button) => {
    const isActive = button.dataset.lesson === lessonName;
    button.classList.toggle('active', isActive);
    button.setAttribute('aria-pressed', isActive);
  });
  renderPreview();
}

editor.addEventListener('input', schedulePreview);

editor.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    tabExitsEditor = true;
    return;
  }

  if (event.key !== 'Tab') return;
  if (event.shiftKey || tabExitsEditor) {
    tabExitsEditor = false;
    return;
  }

  event.preventDefault();
  const start = editor.selectionStart;
  const end = editor.selectionEnd;
  editor.setRangeText('  ', start, end, 'end');
  schedulePreview();
});

lessonButtons.forEach((button) => {
  button.addEventListener('click', () => selectLesson(button.dataset.lesson));
});

resetButton.addEventListener('click', () => selectLesson(currentLesson));

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...options.headers }
  });
  const data = response.status === 204 ? null : await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || 'Something went wrong.');
    error.status = response.status;
    if (response.status === 401) setSignedInUser(null);
    throw error;
  }
  return data;
}

function setSignedInUser(user) {
  signedInUser = user;
  const isSignedIn = Boolean(user);
  userEmail.hidden = !isSignedIn;
  pagesButton.hidden = !isSignedIn;
  userEmail.textContent = user?.email || '';
  authButton.textContent = isSignedIn ? 'Sign out' : 'Sign in';
}

async function refreshAuth() {
  try {
    setSignedInUser(await api('/api/auth/me'));
    if (new URLSearchParams(window.location.search).has('signed-in')) {
      showToast('You’re signed in. Your pages can now be saved!');
      window.history.replaceState({}, '', '/');
    }
  } catch (error) {
    if (error.status !== 401) showToast(error.message);
  }
}

function showToast(message) {
  window.clearTimeout(toastTimer);
  toast.textContent = message;
  toast.classList.add('visible');
  toastTimer = window.setTimeout(() => toast.classList.remove('visible'), 2800);
}

function showSignIn() {
  authMessage.textContent = '';
  authDialog.showModal();
  document.querySelector('#email-input').focus();
}

authButton.addEventListener('click', async () => {
  if (!signedInUser) {
    showSignIn();
    return;
  }
  try {
    await api('/api/auth/logout', { method: 'POST' });
    setSignedInUser(null);
    selectLesson('blank');
    showToast('You’re signed out.');
  } catch (error) {
    showToast(error.message);
  }
});

authForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const button = authForm.querySelector('[type="submit"]');
  button.disabled = true;
  authMessage.textContent = 'Sending…';
  try {
    await api('/api/auth/request', {
      method: 'POST',
      body: JSON.stringify({ email: document.querySelector('#email-input').value })
    });
    authMessage.textContent = 'Check your inbox and open the sign-in link. You can close this window.';
  } catch (error) {
    authMessage.textContent = error.message;
  } finally {
    button.disabled = false;
  }
});

saveButton.addEventListener('click', async () => {
  if (!signedInUser) {
    showSignIn();
    return;
  }
  const payload = { title: pageTitleInput.value.trim(), content: editor.value };
  if (!payload.title) {
    pageTitleInput.focus();
    showToast('Give your page a title first.');
    return;
  }
  saveButton.disabled = true;
  try {
    const page = await api(currentPageID ? `/api/pages/${currentPageID}` : '/api/pages', {
      method: currentPageID ? 'PUT' : 'POST',
      body: JSON.stringify(payload)
    });
    currentPageID = page.id;
    showToast('Page saved!');
  } catch (error) {
    if (error.status === 401) {
      showSignIn();
    } else {
      showToast(error.message);
    }
  } finally {
    saveButton.disabled = false;
  }
});

pagesButton.addEventListener('click', async () => {
  pagesDialog.showModal();
  pagesMessage.textContent = 'Loading…';
  try {
    renderSavedPages(await api('/api/pages'));
    pagesMessage.textContent = '';
  } catch (error) {
    pagesMessage.textContent = error.message;
  }
});

function renderSavedPages(pages) {
  savedPages.replaceChildren();
  if (pages.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No saved pages yet. Make something awesome!';
    savedPages.append(empty);
    return;
  }
  pages.forEach((page) => {
    const row = document.createElement('div');
    row.className = 'saved-page';
    const open = document.createElement('button');
    open.className = 'open-page';
    open.type = 'button';
    open.textContent = page.title;
    const date = document.createElement('small');
    date.textContent = `Updated ${new Date(page.updatedAt).toLocaleDateString()}`;
    open.append(date);
    open.addEventListener('click', () => openSavedPage(page.id));
    const remove = document.createElement('button');
    remove.className = 'delete-page';
    remove.type = 'button';
    remove.textContent = 'Delete';
    remove.setAttribute('aria-label', `Delete ${page.title}`);
    remove.addEventListener('click', () => deleteSavedPage(page.id, page.title, row));
    row.append(open, remove);
    savedPages.append(row);
  });
}

async function openSavedPage(id) {
  try {
    const page = await api(`/api/pages/${id}`);
    currentPageID = page.id;
    pageTitleInput.value = page.title;
    editor.value = page.content;
    renderPreview();
    pagesDialog.close();
    showToast('Page opened.');
  } catch (error) {
    pagesMessage.textContent = error.message;
  }
}

async function deleteSavedPage(id, title, row) {
  if (!window.confirm(`Delete “${title}”?`)) return;
  try {
    await api(`/api/pages/${id}`, { method: 'DELETE' });
    row.remove();
    if (currentPageID === id) currentPageID = null;
    if (savedPages.children.length === 0) renderSavedPages([]);
  } catch (error) {
    pagesMessage.textContent = error.message;
  }
}

document.querySelectorAll('[data-close]').forEach((button) => {
  button.addEventListener('click', () => document.querySelector(`#${button.dataset.close}`).close());
});

selectLesson(currentLesson);
refreshAuth();
