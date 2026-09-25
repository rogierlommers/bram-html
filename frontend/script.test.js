const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');

class FakeElement {
  constructor() {
    this.attributes = new Map();
    this.children = [];
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.dataset = {};
    this.hidden = false;
    this.listeners = new Map();
    this.options = [];
    this.value = '';
  }

  addEventListener(type, listener) { this.listeners.set(type, listener); }
  append(...children) {
    this.children.push(...children);
    this.options.push(...children.filter((child) => child.tagName === 'option'));
  }
  close() { this.open = false; }
  focus() {}
  insertBefore(child) { this.options.push(child); }
  querySelector() { return new FakeElement(); }
  removeAttribute(name) { this.attributes.delete(name); }
  replaceChildren(...children) {
    this.children = children;
    this.options = children.filter((child) => child.tagName === 'option');
  }
  setAttribute(name, value) { this.attributes.set(name, value); }
  showModal() { this.open = true; }
}

function response(status, data = {}) {
  return { ok: status >= 200 && status < 300, status, json: async () => data };
}

function loadApp(authRequest) {
  const elements = new Map();
  const element = (selector) => {
    if (!elements.has(selector)) elements.set(selector, new FakeElement());
    return elements.get(selector);
  };
  element('#email-input').value = ' Kid@Example.com ';

  const document = {
    hidden: false,
    addEventListener() {},
    createElement(tagName) {
      const created = new FakeElement();
      created.tagName = tagName;
      return created;
    },
    querySelector: element,
    querySelectorAll() { return []; }
  };
  const context = {
    console,
    document,
    fetch(path) {
      if (path === '/api/auth/me') return Promise.resolve(response(401, { error: 'not signed in' }));
      if (path === '/api/auth/request') return authRequest();
      throw new Error(`Unexpected request: ${path}`);
    },
    window: {
      clearTimeout() {},
      confirm() { return true; },
      setInterval() { return 0; },
      setTimeout() { return 0; }
    }
  };
  vm.runInNewContext(fs.readFileSync(`${__dirname}/script.js`, 'utf8'), context);
  return { elements, submit: element('#auth-form').listeners.get('submit') };
}

test('sign-in submission ignores a second request while email is sending', async () => {
  let calls = 0;
  let finishRequest;
  const app = loadApp(() => {
    calls++;
    return new Promise((resolve) => { finishRequest = () => resolve(response(202)); });
  });
  const event = { preventDefault() {} };

  const first = app.submit(event);
  const second = app.submit(event);

  assert.equal(calls, 1);
  assert.equal(app.elements.get('#auth-submit').disabled, true);
  assert.equal(app.elements.get('#auth-progress').hidden, false);
  assert.equal(app.elements.get('#auth-form').attributes.get('aria-busy'), 'true');
  await second;

  finishRequest();
  await first;
  assert.equal(app.elements.get('#auth-submit').disabled, false);
  assert.equal(app.elements.get('#auth-submit').textContent, 'Verify code');
  assert.equal(app.elements.get('#auth-progress').hidden, true);
  assert.equal(app.elements.get('#auth-form').attributes.has('aria-busy'), false);
});

test('sign-in submission restores controls when sending fails', async () => {
  let rejectRequest;
  const app = loadApp(() => new Promise((_, reject) => {
    rejectRequest = () => reject(new Error('mail unavailable'));
  }));

  const submission = app.submit({ preventDefault() {} });
  rejectRequest();
  await submission;

  assert.equal(app.elements.get('#auth-submit').disabled, false);
  assert.equal(app.elements.get('#auth-submit').textContent, 'Email my sign-in code');
  assert.equal(app.elements.get('#auth-progress').hidden, true);
  assert.equal(app.elements.get('#auth-form').attributes.has('aria-busy'), false);
  assert.equal(app.elements.get('#auth-message').textContent, 'mail unavailable');
});
