// Run with: node --test extension/test   (Node >= 18, no dependencies)
import test from 'node:test';
import assert from 'node:assert/strict';
import * as core from '../lib/core.js';

test('normalizeDaemonUrl', () => {
  assert.equal(core.normalizeDaemonUrl(''), 'http://127.0.0.1:7979');
  assert.equal(core.normalizeDaemonUrl('127.0.0.1:7979'), 'http://127.0.0.1:7979');
  assert.equal(core.normalizeDaemonUrl(' https://nas.lan:8443/adesc/api/v1/ '), 'https://nas.lan:8443/adesc');
  assert.equal(core.normalizeDaemonUrl('http://localhost:7979/?x=1#y'), 'http://localhost:7979');
  assert.throws(() => core.normalizeDaemonUrl('ftp://host'), /http/);
  assert.throws(() => core.normalizeDaemonUrl('http://user:pw@host'), /usuario/);
  assert.throws(() => core.normalizeDaemonUrl('http://'), /válida/);
});

test('apiUrl, originPattern, isLocalDaemon', () => {
  assert.equal(core.apiUrl('http://127.0.0.1:7979/', '/links'), 'http://127.0.0.1:7979/api/v1/links');
  assert.equal(core.apiUrl('https://nas.lan/adesc', 'stats'), 'https://nas.lan/adesc/api/v1/stats');
  assert.equal(core.originPattern('https://nas.lan:8443/x'), 'https://nas.lan/*');
  assert.equal(core.isLocalDaemon('http://localhost:1'), true);
  assert.equal(core.isLocalDaemon('http://[::1]:7979'), true);
  assert.equal(core.isLocalDaemon('http://192.168.1.2:7979'), false);
});

test('authHeaders never sends an empty bearer', () => {
  assert.deepEqual(core.authHeaders(''), { Accept: 'application/json' });
  assert.equal(core.authHeaders('abc').Authorization, 'Bearer abc');
});

test('buildLinksPayload priorities', () => {
  const cases = [
    [{ linkUrl: 'https://mega.nz/file/x', selectionText: 'a', pageUrl: 'https://p' }, { urls: ['https://mega.nz/file/x'] }],
    [{ srcUrl: 'https://cdn/x.mp4', pageUrl: 'https://p' }, { urls: ['https://cdn/x.mp4'] }],
    [{ linkUrl: 'javascript:void(0)', selectionText: ' https://a https://b ', pageUrl: 'https://p' }, { text: 'https://a https://b' }],
    [{ pageUrl: 'https://www.mediafire.com/folder/abc' }, { urls: ['https://www.mediafire.com/folder/abc'] }],
  ];
  for (const [info, want] of cases) {
    assert.deepEqual(core.buildLinksPayload(info), { ...want, source: 'extension' });
  }
  assert.equal(core.buildLinksPayload({ pageUrl: 'chrome://newtab' }), null);
  assert.equal(core.buildLinksPayload({ srcUrl: 'blob:https://x/1' }), null);
  assert.equal(core.buildLinksPayload({ pageUrl: 'https://p' }, { autoConfirm: true }).auto_confirm, true);
});

test('lists, extensions and host rules', () => {
  assert.deepEqual(core.parseList('a, b;c\n d'), ['a', 'b', 'c', 'd']);
  assert.deepEqual(core.parseExtensions('.ZIP, *.rar zip 7z'), ['zip', 'rar', '7z']);
  assert.equal(core.hostMatches('www.mediafire.com', 'mediafire.com'), true);
  assert.equal(core.hostMatches('mediafire.com', '*.mediafire.com'), true);
  assert.equal(core.hostMatches('evilmediafire.com', 'mediafire.com'), false);
  assert.equal(core.hostMatches('cdn12.example.org', '/^cdn\\d+\\.example\\.org$/'), true);
  assert.equal(core.hostMatches('x', '/(/'), false);
  assert.deepEqual(core.invalidHostRules(['ok.com', '/(/', '/fine/']), ['/(/']);
  assert.equal(core.fileExtension('C:\\dl\\Movie.Part1.RAR'), 'rar');
  assert.equal(core.fileExtension('.bashrc'), '');
  assert.equal(core.fileExtension('noext'), '');
  assert.equal(core.extensionFromUrl('https://h/a%20b.7z?token=1'), '7z');
});

test('shouldCapture', () => {
  const settings = core.mergeSettings({
    daemonUrl: 'http://127.0.0.1:7979',
    capture: { enabled: true, extensions: ['zip', 'mkv', '001'], minSizeMB: 1, excludeHosts: ['bank.example'] },
  });
  const big = 50 * 1024 * 1024;
  const base = { id: 1, url: 'https://files.example/a.zip', state: 'in_progress', totalBytes: big };
  const check = (item, s = settings) => core.shouldCapture({ ...base, ...item }, s, 'me');
  assert.equal(check({}).capture, true);
  assert.equal(check({}).url, 'https://files.example/a.zip');
  assert.equal(check({ finalUrl: 'https://cdn.example/b.mkv' }).url, 'https://cdn.example/b.mkv');
  assert.equal(check({}, core.mergeSettings({})).reason, 'disabled');
  assert.equal(check({ url: 'blob:https://x/1' }).reason, 'scheme');
  assert.equal(check({ url: 'data:text/plain,hi' }).reason, 'scheme');
  assert.equal(check({ byExtensionId: 'me' }).reason, 'own');
  assert.equal(check({ incognito: true }).reason, 'incognito');
  assert.equal(check({ url: 'http://127.0.0.1:7979/api/v1/x.zip' }).reason, 'daemon');
  assert.equal(check({ url: 'https://www.bank.example/a.zip' }).reason, 'excluded');
  assert.equal(check({ url: 'https://h/a.txt' }).reason, 'extension');
  assert.equal(check({ url: 'https://h/download?id=1', filename: '/home/u/x.mkv' }).capture, true);
  assert.equal(check({ url: 'https://h/download?id=1', mime: 'application/zip' }).capture, true);
  assert.equal(check({ url: 'https://h/a.7z.003' }).capture, true);
  assert.equal(check({ totalBytes: 1000 }).reason, 'size');
  assert.equal(check({ totalBytes: 0, fileSize: -1 }).capture, true, 'unknown size is not filtered');
  const onlyHost = core.mergeSettings({ capture: { enabled: true, extensions: [], includeHosts: ['example.org'] } });
  assert.equal(check({ url: 'https://other.net/a.bin' }, onlyHost).reason, 'not-included');
  assert.equal(check({ url: 'https://dl.example.org/a.bin' }, onlyHost).capture, true);
});

test('toNetscape', () => {
  const txt = core.toNetscape([
    { domain: '.mega.nz', hostOnly: false, path: '/', secure: true, httpOnly: true, expirationDate: 1893456000.7, name: 'sid', value: 'v1' },
    { domain: 'www.mediafire.com', hostOnly: true, path: '/', secure: false, session: true, name: 'a', value: 'b\tc' },
    { domain: 'terabox.com', hostOnly: false, path: '/x', secure: false, expirationDate: 2000000000, name: 'ndus', value: 'z' },
  ]);
  const lines = txt.trimEnd().split('\n');
  assert.equal(lines[0], '# Netscape HTTP Cookie File');
  assert.ok(lines.includes('#HttpOnly_.mega.nz\tTRUE\t/\tTRUE\t1893456000\tsid\tv1'));
  assert.ok(lines.includes('www.mediafire.com\tFALSE\t/\tFALSE\t0\ta\tb c'));
  assert.ok(lines.includes('.terabox.com\tTRUE\t/x\tFALSE\t2000000000\tndus\tz'));
  for (const l of lines.filter((l) => l && !l.startsWith('# '))) assert.equal(l.split('\t').length, 7);
});

test('dedupeCookies and hoster table', () => {
  const c = { domain: '.mega.nz', path: '/', name: 'sid', value: '1', storeId: '0' };
  assert.equal(core.dedupeCookies([c, { ...c, value: '2' }]).length, 1);
  const ids = core.HOSTERS.map((h) => h.id);
  assert.equal(new Set(ids).size, ids.length);
  for (const want of ['mega', 'terabox', 'mediafire', 'gdrive', 'pixeldrain', 'gofile', 'onefichier', 'dropbox', 'onedrive', 'yandex']) {
    assert.ok(core.hosterById(want), want);
  }
  assert.deepEqual(core.hosterOrigins(core.hosterById('mediafire')), ['*://*.mediafire.com/*']);
  for (const h of core.HOSTERS) for (const d of h.domains) assert.match(d, /^[a-z0-9.-]+\.[a-z]+$/);
});

test('stats, badge and messages', () => {
  const s = core.parseStats({ speed: 2048, active: 3, queued: 1, grabber_size: 4, paused: false });
  assert.equal(s.active, 3);
  assert.equal(s.grabberSize, 4);
  assert.equal(s.failed, 0);
  assert.equal(core.parseStats({ stats: { active: 2 } }).active, 2);
  assert.equal(core.parseStats(null).active, 0);
  assert.deepEqual(core.badgeForStats({ active: 0 }).text, '');
  assert.deepEqual(core.badgeForStats({ active: 7 }).text, '7');
  assert.equal(core.badgeForError().text, '!');
  assert.equal(core.countAdded([1, 2]), 2);
  assert.equal(core.countAdded({ entries: [1] }), 1);
  assert.equal(core.countAdded({}), null);
  assert.match(core.errorMessage(401), /Token/);
  assert.match(core.errorMessage(500, '{"error":"boom"}'), /HTTP 500.*boom/);
  assert.match(core.errorMessage(0), /conectar/);
  assert.equal(core.formatSpeed(0), '0 B/s');
  assert.equal(core.formatSpeed(1536), '1.5 KB/s');
});

test('mergeSettings fills defaults without sharing state', () => {
  const s = core.mergeSettings({ token: 't', capture: { enabled: true } });
  assert.equal(s.token, 't');
  assert.equal(s.daemonUrl, core.DEFAULT_DAEMON_URL);
  assert.equal(s.capture.enabled, true);
  assert.ok(s.capture.extensions.includes('zip'));
  assert.equal(core.DEFAULT_SETTINGS.capture.enabled, false);
});
