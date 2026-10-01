const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');

class FakeElement {
  constructor() {
    this.attributes = new Map();
    this.children = [];
    this.hidden = false;
    this.textContent = '';
  }

  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(name, value) { this.attributes.set(name, value); }
}

function loadAdminScript() {
  const elements = new Map();
  const element = (selector) => {
    if (!elements.has(selector)) elements.set(selector, new FakeElement());
    return elements.get(selector);
  };
  const context = {
    Headers,
    Intl,
    fetch: () => new Promise(() => {}),
    document: {
      hidden: false,
      addEventListener() {},
      createElement: () => new FakeElement(),
      createElementNS: () => new FakeElement(),
      querySelector: element
    },
    window: { clearTimeout() {}, setTimeout() {} }
  };
  vm.runInNewContext(fs.readFileSync(`${__dirname}/admin.js`, 'utf8'), context);
  return { context, elements };
}

test('admin page overview renders saved-page metadata as text', () => {
  const { context, elements } = loadAdminScript();
  context.renderPages([{
    id: 7,
    title: '<img src=x onerror=alert(1)>',
    ownerEmail: 'kid@example.com',
    createdAt: '2026-09-20T10:00:00Z',
    updatedAt: '2026-09-24T12:30:00Z'
  }]);

  assert.equal(elements.get('#pages-count').textContent, '1 pagina');
  const row = elements.get('#admin-pages').children[0];
  assert.equal(row.children[0].children[0].textContent, '<img src=x onerror=alert(1)>');
  assert.equal(row.children[0].children[0].href, '/admin/pages/7');
  assert.equal(row.children[1].textContent, 'kid@example.com');
  assert.notEqual(row.children[2].textContent, '');
  assert.notEqual(row.children[3].textContent, '');
});

test('admin page overview renders an empty state', () => {
  const { context, elements } = loadAdminScript();
  context.renderPages([]);

  assert.equal(elements.get('#pages-count').textContent, '0 pagina’s');
  const cell = elements.get('#admin-pages').children[0].children[0];
  assert.equal(cell.colSpan, 4);
  assert.equal(cell.textContent, 'Er zijn nog geen pagina’s opgeslagen.');
});

test('admin page preview loads cross-user content into the sandboxed iframe', async () => {
  const elements = new Map();
  const element = (selector) => {
    if (!elements.has(selector)) elements.set(selector, new FakeElement());
    return elements.get(selector);
  };
  const page = {
    id: 7,
    title: 'Kid page',
    content: '<script>window.top.location="https://example.com"</script><h1>Hello</h1>',
    ownerEmail: 'kid@example.com',
    updatedAt: '2026-09-24T12:30:00Z'
  };
  const context = {
    encodeURIComponent,
    fetch: async () => ({ ok: true, json: async () => page }),
    Intl,
    document: { title: '', querySelector: element },
    window: { location: { pathname: '/admin/pages/7' } }
  };
  vm.runInNewContext(fs.readFileSync(`${__dirname}/admin-page.js`, 'utf8'), context);
  await context.loadPage();

  assert.equal(element('#page-title').textContent, 'Kid page');
  assert.equal(element('#page-owner').textContent, 'kid@example.com');
  assert.equal(element('#page-preview').srcdoc, page.content);
  assert.match(fs.readFileSync(`${__dirname}/admin-page.html`, 'utf8'), /<iframe[^>]+sandbox(?:\s|>)/);
});
