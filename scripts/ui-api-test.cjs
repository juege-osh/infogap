const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const requests = [];
const context = {
  document: { addEventListener() {} },
  location: { search: '', hash: '#feed' },
  URL, URLSearchParams, Intl, Date, setTimeout, clearTimeout,
  fetch: async (url, options) => {
    requests.push({ url, options });
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
(async () => {
  for (const method of ['GET', 'HEAD', 'POST', 'DELETE', 'delete']) {
    await vm.runInContext(`api('/api/admin/test-mode',{method:${JSON.stringify(method)}})`, context);
    const call = requests.at(-1).options;
    assert.equal(call.credentials, 'same-origin');
    assert.equal(call.headers['Content-Type'], ['GET', 'HEAD'].includes(method) ? undefined : 'application/json');
  }
  context.fetch = async () => ({ ok: false, status: 403, json: async () => ({ error: '没有操作权限', request_id: 'RQ-test-reference' }) });
  await assert.rejects(vm.runInContext("api('/api/admin/overview')", context), error => {
    assert.equal(error.status, 403);
    assert.equal(error.requestID, 'RQ-test-reference');
    assert.match(error.message, /没有操作权限.*RQ-test-reference/);
    return true;
  });
  console.log('PASS: bodyless API mutations send JSON headers, preserve session cookies, and expose support references.');
})().catch(error => { console.error(error); process.exitCode = 1; });
