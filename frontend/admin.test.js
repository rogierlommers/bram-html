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

  assert.equal(elements.get('#pages-count').textContent, '1 page');
  const row = elements.get('#admin-pages').children[0];
  assert.equal(row.children[0].textContent, '<img src=x onerror=alert(1)>');
  assert.equal(row.children[1].textContent, 'kid@example.com');
  assert.notEqual(row.children[2].textContent, '');
  assert.notEqual(row.children[3].textContent, '');
});

test('admin page overview renders an empty state', () => {
  const { context, elements } = loadAdminScript();
  context.renderPages([]);

  assert.equal(elements.get('#pages-count').textContent, '0 pages');
  const cell = elements.get('#admin-pages').children[0].children[0];
  assert.equal(cell.colSpan, 4);
  assert.equal(cell.textContent, 'No pages have been saved yet.');
});
