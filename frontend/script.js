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
const pageSelect = document.querySelector('#page-select');
const authButton = document.querySelector('#auth-button');
const pagesButton = document.querySelector('#pages-button');
const userEmail = document.querySelector('#user-email');
const adminLink = document.querySelector('#admin-link');
const authDialog = document.querySelector('#auth-dialog');
const saveDialog = document.querySelector('#save-dialog');
const pagesDialog = document.querySelector('#pages-dialog');
const authForm = document.querySelector('#auth-form');
const authDescription = document.querySelector('#auth-description');
const emailInput = document.querySelector('#email-input');
const codeLabel = document.querySelector('#code-label');
const codeInput = document.querySelector('#code-input');
const authSubmit = document.querySelector('#auth-submit');
const authProgress = document.querySelector('#auth-progress');
const authBack = document.querySelector('#auth-back');
const authMessage = document.querySelector('#auth-message');
const saveForm = document.querySelector('#save-form');
const saveCloseButton = saveDialog.querySelector('[data-close]');
const pageNameInput = document.querySelector('#page-name-input');
const saveMessage = document.querySelector('#save-message');
const pagesMessage = document.querySelector('#pages-message');
const savedPages = document.querySelector('#saved-pages');
const toast = document.querySelector('#toast');

let currentLesson = 'blank';
let currentPageID = null;
let currentPageTitle = 'Blank page';
let signedInUser = null;
let pendingEmail = '';
let authInProgress = false;
let pageLoadGeneration = 0;
let pageListGeneration = 0;
let saveInProgress = false;
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
  pageLoadGeneration++;
  saveButton.disabled = false;
  currentLesson = lessonName;
  editor.value = lessons[lessonName];
  currentPageID = null;
  currentPageTitle = lessonName === 'welcome' ? 'My first page' : document.querySelector(`[data-lesson="${lessonName}"] strong`).textContent;
  pageSelect.value = 'new';
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

pageSelect.addEventListener('change', () => {
  if (pageSelect.value === 'new') {
    selectLesson('blank');
    return;
  }
  openSavedPage(Number(pageSelect.value), false);
});

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...options.headers }
  });
  const data = response.status === 204 ? null : await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || 'Something went wrong.');
    error.status = response.status;
    throw error;
  }
  return data;
}

function setSignedInUser(user) {
  signedInUser = user;
  pageListGeneration++;
  const isSignedIn = Boolean(user);
  userEmail.hidden = !isSignedIn;
  pagesButton.hidden = !isSignedIn;
  adminLink.hidden = !user?.isAdmin;
  userEmail.textContent = user?.email || '';
  authButton.textContent = isSignedIn ? 'Sign out' : 'Sign in';
  if (!isSignedIn) {
    currentPageID = null;
    pageLoadGeneration++;
    renderPageOptions([]);
    savedPages.replaceChildren();
    pagesMessage.textContent = '';
    if (pagesDialog.open) pagesDialog.close();
  }
}

function renderPageOptions(pages) {
  pageSelect.replaceChildren();

  const newPageOption = document.createElement('option');
  newPageOption.value = 'new';
  newPageOption.textContent = 'New page';
  pageSelect.append(newPageOption);

  pages.forEach((page) => {
    const option = document.createElement('option');
    option.value = String(page.id);
    option.textContent = page.title;
    pageSelect.append(option);
  });

  pageSelect.value = currentPageID === null ? 'new' : String(currentPageID);
}

function selectPageOption(page) {
  pageListGeneration++;
  let option = Array.from(pageSelect.options).find((item) => item.value === String(page.id));
  if (!option) {
    option = document.createElement('option');
    pageSelect.insertBefore(option, pageSelect.options[1] || null);
  }
  option.value = String(page.id);
  option.textContent = page.title;
  pageSelect.value = option.value;
}

async function refreshPageOptions() {
  if (!signedInUser) {
    renderPageOptions([]);
    return;
  }
  const user = signedInUser;
  const generation = ++pageListGeneration;
  const pages = await api('/api/pages');
  if (signedInUser === user && pageListGeneration === generation) renderPageOptions(pages);
}

async function refreshAuth() {
  const previousUser = signedInUser;
  try {
    const user = await api('/api/auth/me');
    if (signedInUser !== previousUser) return;
    setSignedInUser(user);
    void reportActivity();
    try {
      await refreshPageOptions();
    } catch (error) {
      if (!handleExpiredSession(error)) showToast(error.message);
    }
  } catch (error) {
    if (signedInUser !== previousUser) return;
    if (error.status === 401) {
      setSignedInUser(null);
    } else {
      showToast(error.message);
    }
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
  (pendingEmail ? codeInput : emailInput).focus();
}

function handleExpiredSession(error) {
  if (error.status !== 401) return false;
  setSignedInUser(null);
  showSignIn();
  return true;
}

function showCodeEntry(email) {
  pendingEmail = email;
  emailInput.readOnly = true;
  codeLabel.hidden = false;
  codeInput.hidden = false;
  codeInput.required = true;
  authBack.hidden = false;
  authSubmit.textContent = 'Verify code';
  authDescription.textContent = `Enter the six-digit code sent to ${email}.`;
  authMessage.textContent = '';
  codeInput.value = '';
  codeInput.focus();
}

function resetSignIn(focusEmail = true) {
  pendingEmail = '';
  emailInput.readOnly = false;
  codeLabel.hidden = true;
  codeInput.hidden = true;
  codeInput.required = false;
  codeInput.value = '';
  authBack.hidden = true;
  authSubmit.textContent = 'Email my sign-in code';
  authDescription.textContent = 'Enter an email address. We’ll send you a six-digit sign-in code.';
  authMessage.textContent = '';
  if (focusEmail) emailInput.focus();
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
  if (authInProgress) return;
  authInProgress = true;
  const sendingEmail = !pendingEmail;
  authSubmit.disabled = true;
  authSubmit.textContent = sendingEmail ? 'Sending email…' : 'Checking…';
  authProgress.hidden = !sendingEmail;
  authForm.setAttribute('aria-busy', 'true');
  authMessage.textContent = sendingEmail ? 'Sending your sign-in code…' : 'Checking…';
  try {
    if (!pendingEmail) {
      const email = emailInput.value.trim().toLowerCase();
      await api('/api/auth/request', {
        method: 'POST',
        body: JSON.stringify({ email })
      });
      showCodeEntry(email);
      return;
    }

    const user = await api('/api/auth/verify', {
      method: 'POST',
      body: JSON.stringify({ email: pendingEmail, code: codeInput.value })
    });
    setSignedInUser(user);
    void reportActivity();
    authDialog.close();
    resetSignIn(false);
    showToast('You’re signed in. Your pages can now be saved!');
    try {
      await refreshPageOptions();
    } catch (error) {
      if (!handleExpiredSession(error)) showToast(`Signed in, but pages could not be loaded: ${error.message}`);
    }
  } catch (error) {
    authMessage.textContent = error.message;
  } finally {
    authInProgress = false;
    authSubmit.disabled = false;
    authSubmit.textContent = pendingEmail ? 'Verify code' : 'Email my sign-in code';
    authProgress.hidden = true;
    authForm.removeAttribute('aria-busy');
  }
});

authBack.addEventListener('click', resetSignIn);

saveButton.addEventListener('click', () => {
  if (!signedInUser) {
    showSignIn();
    return;
  }

  saveMessage.textContent = '';
  pageNameInput.value = currentPageTitle;
  saveDialog.showModal();
  pageNameInput.focus();
  pageNameInput.select();
});

saveForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const title = pageNameInput.value.trim();
  if (!title) {
    pageNameInput.focus();
    saveMessage.textContent = 'Enter a page name.';
    return;
  }

  const submitButton = saveForm.querySelector('[type="submit"]');
  const pageID = currentPageID;
  const content = editor.value;
  saveInProgress = true;
  submitButton.disabled = true;
  saveCloseButton.disabled = true;
  saveMessage.textContent = 'Saving…';
  try {
    const page = await api(pageID ? `/api/pages/${pageID}` : '/api/pages', {
      method: pageID ? 'PUT' : 'POST',
      body: JSON.stringify({ title, content })
    });
    currentPageID = page.id;
    currentPageTitle = page.title;
    selectPageOption(page);
    saveDialog.close();
    showToast('Page saved!');
  } catch (error) {
    if (error.status === 401) {
      saveDialog.close();
      handleExpiredSession(error);
    } else {
      saveMessage.textContent = error.message;
    }
  } finally {
    saveInProgress = false;
    submitButton.disabled = false;
    saveCloseButton.disabled = false;
  }
});

saveDialog.addEventListener('cancel', (event) => {
  if (saveInProgress) event.preventDefault();
});

pagesButton.addEventListener('click', async () => {
  pagesDialog.showModal();
  pagesMessage.textContent = 'Loading…';
  const user = signedInUser;
  const generation = ++pageListGeneration;
  try {
    const pages = await api('/api/pages');
    if (signedInUser !== user || pageListGeneration !== generation) return;
    renderSavedPages(pages);
    renderPageOptions(pages);
    pagesMessage.textContent = '';
  } catch (error) {
    if (signedInUser !== user) return;
    if (!handleExpiredSession(error)) pagesMessage.textContent = error.message;
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

async function openSavedPage(id, closeDialog = true) {
  const loadGeneration = ++pageLoadGeneration;
  saveButton.disabled = true;
  try {
    const page = await api(`/api/pages/${id}`);
    if (loadGeneration !== pageLoadGeneration) return;
    currentPageID = page.id;
    currentPageTitle = page.title;
    pageSelect.value = String(page.id);
    editor.value = page.content;
    renderPreview();
    if (closeDialog) pagesDialog.close();
    showToast('Page opened.');
  } catch (error) {
    if (loadGeneration !== pageLoadGeneration) return;
    if (handleExpiredSession(error)) return;
    if (closeDialog) {
      pagesMessage.textContent = error.message;
    } else {
      pageSelect.value = currentPageID === null ? 'new' : String(currentPageID);
      showToast(error.message);
    }
  } finally {
    if (loadGeneration === pageLoadGeneration) saveButton.disabled = false;
  }
}

async function deleteSavedPage(id, title, row) {
  if (!window.confirm(`Delete “${title}”?`)) return;
  try {
    await api(`/api/pages/${id}`, { method: 'DELETE' });
    pageListGeneration++;
    row.remove();
    pageSelect.querySelector(`option[value="${id}"]`)?.remove();
    if (currentPageID === id) selectLesson('blank');
    if (savedPages.children.length === 0) renderSavedPages([]);
  } catch (error) {
    if (!handleExpiredSession(error)) pagesMessage.textContent = error.message;
  }
}

document.querySelectorAll('[data-close]').forEach((button) => {
  button.addEventListener('click', () => document.querySelector(`#${button.dataset.close}`).close());
});

selectLesson(currentLesson);
refreshAuth();

async function reportActivity() {
  if (!signedInUser || document.hidden) return;
  const user = signedInUser;
  try {
    await api('/api/activity', { method: 'POST' });
  } catch (error) {
    if (error.status === 401 && signedInUser === user) setSignedInUser(null);
  }
}

window.setInterval(reportActivity, 60_000);
document.addEventListener('visibilitychange', reportActivity);
