const state = document.querySelector('#page-state');
const detail = document.querySelector('#page-detail');
const pageID = window.location.pathname.split('/').pop();
const dateFormatter = new Intl.DateTimeFormat('nl-NL', { dateStyle: 'medium', timeStyle: 'short' });

async function loadPage() {
  try {
    const response = await fetch(`/api/admin/pages/${encodeURIComponent(pageID)}`, {
      headers: { Accept: 'application/json' }
    });
    const page = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(page.error || 'De opgeslagen pagina kon niet worden geladen.');

    document.title = `${page.title} · bram-html`;
    document.querySelector('#page-title').textContent = page.title;
    document.querySelector('#page-owner').textContent = page.ownerEmail;
    document.querySelector('#page-updated').textContent = dateFormatter.format(new Date(page.updatedAt));
    document.querySelector('#page-preview').srcdoc = page.content;
    state.hidden = true;
    detail.hidden = false;
  } catch (error) {
    state.textContent = error.message;
    state.hidden = false;
    detail.hidden = true;
  }
}

loadPage();
