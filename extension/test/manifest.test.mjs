// Static checks of manifest.json and the extension files (a light "lint").
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync, readdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'));
const exists = (p) => existsSync(join(root, p));

test('manifest is MV3 for Chrome and Firefox', () => {
  assert.equal(manifest.manifest_version, 3);
  assert.ok(manifest.name && manifest.version && manifest.description);
  assert.equal(manifest.background.service_worker, 'background.js');
  assert.deepEqual(manifest.background.scripts, ['background.js']);
  assert.equal(manifest.background.type, 'module');
  assert.ok(manifest.browser_specific_settings.gecko.id);
  for (const p of ['contextMenus', 'storage', 'alarms', 'notifications', 'downloads']) {
    assert.ok(manifest.permissions.includes(p), p);
  }
  assert.ok(!manifest.permissions.includes('cookies'), 'cookies must be optional');
  assert.deepEqual(manifest.optional_permissions, ['cookies']);
  for (const h of manifest.host_permissions) assert.match(h, /^http:\/\/(127\.0\.0\.1|localhost)\/\*$/);
  assert.deepEqual(manifest.optional_host_permissions, ['<all_urls>']);
});

test('every referenced file exists', () => {
  const files = [
    manifest.background.service_worker,
    ...manifest.background.scripts,
    manifest.action.default_popup,
    manifest.options_ui.page,
    ...Object.values(manifest.icons),
    ...Object.values(manifest.action.default_icon),
  ];
  for (const f of files) assert.ok(exists(f), f);
  for (const html of ['popup.html', 'options.html']) {
    const src = readFileSync(join(root, html), 'utf8');
    for (const m of src.matchAll(/(?:src|href)="([^"]+)"/g)) assert.ok(exists(m[1]), `${html}: ${m[1]}`);
    assert.ok(!/<script(?![^>]*\bsrc=)[^>]*>/.test(src), `${html}: inline scripts are blocked by the MV3 CSP`);
  }
});

test('JS files parse and never log settings or tokens', () => {
  const js = ['background.js', 'options.js', 'popup.js', ...readdirSync(join(root, 'lib')).map((f) => `lib/${f}`)];
  for (const f of js) {
    execFileSync(process.execPath, ['--check', join(root, f)]);
    const src = readFileSync(join(root, f), 'utf8');
    assert.ok(!/console\.(log|info|debug)\([^)]*(token|settings|cookie)/i.test(src), `${f} logs a secret`);
  }
});
