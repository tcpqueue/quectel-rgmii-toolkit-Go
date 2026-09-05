const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '../development/simpleadmin/www/js/pages/settings.js'), 'utf8');

function settings(setPassword) {
  const redirects = [];
  const SimpleAdmin = { Api: { setPassword } };
  const window = { SimpleAdmin, SimpleAdminSpaMode: true, location: { replace: value => redirects.push(value) } };
  const context = vm.createContext({ window, SimpleAdmin, console: { error() {} } });
  vm.runInContext(source, context);
  const state = window.SimpleAdmin.Pages.settings();
  state.currentPassword = 'old-password';
  state.newPassword = state.confirmPassword = 'new-password';
  return { state, redirects };
}

test('successful password change clears fields and returns to login', async () => {
  const calls = [];
  const { state, redirects } = settings(async (...args) => {
    calls.push(args);
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  });
  await state.changeLoginPassword();
  assert.deepEqual(calls, [['old-password', 'new-password', 'new-password']]);
  assert.deepEqual(redirects, ['/login.html']);
  assert.equal(state.currentPassword + state.newPassword + state.confirmPassword, '');
  assert.equal(state.isSavingPassword, false);
});

test('wrong current password leaves form available for correction', async () => {
  const { state, redirects } = settings(async () => ({ ok: false, status: 403, json: async () => ({ error: 'current password incorrect' }) }));
  await state.changeLoginPassword();
  assert.equal(state.passwordSaveMessage, '当前密码不正确');
  assert.deepEqual(redirects, []);
  assert.equal(state.isSavingPassword, false);
});

test('mismatch and duplicate submission do not send requests', async () => {
  let calls = 0;
  let complete;
  const { state } = settings(() => { calls++; return new Promise(resolve => { complete = resolve; }); });
  state.confirmPassword = 'different';
  await state.changeLoginPassword();
  assert.equal(calls, 0);
  assert.equal(state.passwordSaveMessage, '两次输入的新密码不一致');
  state.confirmPassword = state.newPassword;
  const first = state.changeLoginPassword();
  await state.changeLoginPassword();
  assert.equal(calls, 1);
  complete({ ok: true, status: 200, json: async () => ({ ok: true }) });
  await first;
});
