// End to end inside Node: AppStore + real Api client + SSE stream parser
// against the in-memory mock backend (same fixtures as npm run dev:mock).
import { afterEach, describe, expect, it } from 'vitest';
import { AppStore } from '../lib/store/app.svelte';
import { createMock, type Mock } from '../lib/mock/server';
import { dkey, pkey } from '../lib/store/rows';

const until = async (cond: () => boolean, ms = 3000) => {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timeout');
    await new Promise((r) => setTimeout(r, 10));
  }
};

let mock: Mock | null = null;
afterEach(() => mock?.stop());

async function started(opts: Parameters<typeof createMock>[0] = {}) {
  mock = createMock({ tickMs: 0, latency: 0, ...opts });
  const app = new AppStore();
  await app.start(mock.fetch, { mock: true });
  return app;
}

describe('AppStore with the mock backend', () => {
  it('loads the design data and its filter counts', async () => {
    const app = await started();
    expect(app.status).toBe('ready');
    expect(app.counts).toMatchObject({ todas: 7, activas: 2, espera: 1, pausadas: 1, fallidas: 1, completadas: 1 });
    const curso = app.items.find((i) => i.kind === 'package');
    expect(curso?.kind === 'package' && curso.pkg.name).toBe('Curso_Fotografia');
    expect(curso?.kind === 'package' && curso.children).toHaveLength(12);
    expect(app.currentEgressLabel).toBe('WireGuard');
    expect(app.publicIP).toBe('203.0.113.24');
    expect(app.grabberEntries).toHaveLength(3);
    expect(app.accounts.length).toBeGreaterThan(0);
    expect(app.settings?.appearance?.estilo).toBe('grafito');
  });

  it('applies live SSE events from actions (pause, resume)', async () => {
    const app = await started();
    await until(() => app.live);
    const ds = Object.values(app.data.downloads).find((d) => d.name === 'dataset_v2.zip')!;
    await app.pause(dkey(ds.id));
    await until(() => app.data.downloads[ds.id].state === 'paused');
    app.select(dkey(ds.id));
    await app.togglePause();
    await until(() => app.data.downloads[ds.id].state === 'downloading' || app.data.downloads[ds.id].state === 'queued');
  });

  it('progress ticks move bytes and Drive per-file quota fails again on retry', async () => {
    const app = await started();
    await until(() => app.live);
    const d = Object.values(app.data.downloads).find((x) => x.name === 'clase_01.mp4')!;
    const before = d.done;
    mock!.tick(1000);
    await until(() => app.data.downloads[d.id].done > before);
    const v = Object.values(app.data.downloads).find((x) => x.name === 'video_presentacion.mp4')!;
    const ds = Object.values(app.data.downloads).find((x) => x.name === 'dataset_v2.zip')!;
    await app.pause(dkey(ds.id), pkey(1)); // free every slot (max_active = 3)
    await app.retry(dkey(v.id));
    await until(() => app.data.downloads[v.id].state !== 'failed');
    mock!.tick(500);
    mock!.tick(500);
    await until(() => app.data.downloads[v.id].state === 'failed');
    expect(app.statusOf(app.data.downloads[v.id]).label).toBe('Fallida · cuota del archivo');
  });

  it('linkgrabber: add links, then confirm a subset as a folder package', async () => {
    const app = await started();
    await until(() => app.live);
    const added = await app.addLinks('mira https://mega.nz/folder/AAAA1234#k y https://example.com/a.iso');
    expect(added).toHaveLength(2);
    for (let i = 0; i < 4; i++) mock!.tick(1000);
    const folder = added!.find((e) => e.is_folder)!;
    await until(() => app.data.grabber[folder.id]?.state === 'ready');
    const ok = await app.confirmGrabber({ items: [{ entry_id: folder.id, file_indexes: [0, 1] }], paused: true });
    expect(ok).toBe(true);
    const pkg = Object.values(app.data.packages).find((p) => p.source_url?.includes('AAAA1234'))!;
    await until(() => app.childrenOf(pkg.id).length === 2);
    expect(app.items.some((i) => i.key === pkey(pkg.id))).toBe(true);
    expect(app.data.grabber[folder.id]).toBeUndefined();
  });

  it('shows the login screen on 401 and logs in', async () => {
    const app = await started({ login: true });
    expect(app.status).toBe('login');
    await expect(app.login('mal')).rejects.toThrow('Contraseña incorrecta');
    await app.login('admin');
    expect(app.status).toBe('ready');
  });

  it('web mode: discovers no desktop, opens the session without password and recovers CSRF', async () => {
    mock = createMock({ tickMs: 0, latency: 0, openLogin: true });
    const calls: string[] = [];
    const f = mock.fetch;
    const spy: typeof f = (input, init) => {
      calls.push(`${init?.method ?? 'GET'} ${new URL(input).pathname} ${(init?.headers as Record<string, string>)?.['X-CSRF-Token'] ?? ''}`.trim());
      return f(input, init);
    };
    const app = new AppStore();
    await app.start(spy);
    expect(app.connection?.mode).toBe('web');
    expect(app.status).toBe('ready');
    expect(calls[0]).toBe('GET /__adesc/connection');
    expect(calls).toContain('GET /api/v1/auth/session');
    expect(calls).toContain('POST /api/v1/auth/login');
    await app.pauseAll();
    expect(calls).toContain('POST /api/v1/pause-all mock-csrf');
    expect(app.clipboard.available).toBe(false); // no clipboard monitor in a browser
  });

  it('mock mode emulates the clipboard monitor through settings', async () => {
    const app = await started();
    expect(app.clipboard).toMatchObject({ available: true, enabled: true });
    await app.setClipboard(false);
    expect(app.clipboardEnabled).toBe(false);
    expect(app.settings?.clipboard?.enabled).toBe(false);
  });

  it('settings round-trip keeps commands read-only', async () => {
    const app = await started();
    await app.saveSettings({ downloads: { max_active: 5 }, network: { router: { script: ['rm', '-rf'] } } });
    expect(app.settings?.downloads?.max_active).toBe(5);
    expect(app.settings?.network?.router?.script).toEqual([]);
  });
});

describe('secret vault, account events, OAuth paste and known links (mock)', () => {
  it('locked vault: account creation is refused (423), wrong password, unlock, then live account events', async () => {
    const app = await started({ vault: 'locked' });
    await until(() => app.live);
    expect(app.secrets).toMatchObject({ backend: 'vault', lockable: true, initialized: true, locked: true });
    expect(app.vaultLocked).toBe(true);

    const a = await app.createAccount({ hoster: 'pixeldrain', kind: 'apikey', label: 'PD', api_key: 'k' });
    expect(a).toBeUndefined();
    expect(app.toasts.at(-1)?.message).toMatch(/bloqueadas/); // "accounts: secret store is locked" in Spanish

    const bad = await app.unlockSecrets('nope');
    expect(bad).toMatchObject({ ok: false, status: 403, code: 'wrong_password' });
    expect(app.vaultLocked).toBe(true);

    expect(await app.unlockSecrets('demo1234')).toEqual({ ok: true });
    expect(app.vaultLocked).toBe(false);

    const n = app.accounts.length;
    const created = await app.createAccount({ hoster: 'pixeldrain', kind: 'apikey', label: 'PD', api_key: 'k' });
    expect(created?.id).toBeTruthy();
    expect(app.accounts).toHaveLength(n + 1);

    // Changes made by another client arrive as account.* events.
    const other = (method: string, path: string, body?: unknown) =>
      mock!.fetch(`http://x/api/v1/${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });
    await other('PATCH', `accounts/${created!.id}`, { label: 'Renombrada' });
    await until(() => app.data.accounts[created!.id]?.label === 'Renombrada');
    await other('DELETE', `accounts/${created!.id}`);
    await until(() => !app.data.accounts[created!.id]);
    expect(app.accounts).toHaveLength(n);

    // Locking elsewhere: secrets.updated flips the banner.
    await other('POST', 'secrets/lock');
    await until(() => app.vaultLocked);
  });

  it('rate limits the unlock (429 + Retry-After)', async () => {
    const app = await started({ vault: 'locked' });
    for (let i = 0; i < 5; i++) await app.unlockSecrets('x');
    const r = await app.unlockSecrets('demo1234');
    expect(r).toMatchObject({ ok: false, status: 429, code: 'rate_limited' });
    expect(r.ok === false && r.retryAfter).toBeGreaterThan(0);
    expect(r.ok === false && r.message).toMatch(/Espera \d+ s/);
  });

  it('a new vault: the first unlock chooses the master password (>= 8 chars); lock and change it', async () => {
    const app = await started({ vault: 'new' });
    expect(app.secrets).toMatchObject({ initialized: false, locked: true });
    const weak = await app.unlockSecrets('corta');
    expect(weak.ok === false && weak.message).toBe('La contraseña maestra debe tener al menos 8 caracteres.');
    expect(await app.unlockSecrets('una-buena-clave')).toEqual({ ok: true });
    expect(app.secrets).toMatchObject({ initialized: true, locked: false });

    await app.lockSecrets();
    expect(app.vaultLocked).toBe(true);
    const wrong = await app.changeMasterPassword('otra-cosa', 'nueva-clave-1');
    expect(wrong).toMatchObject({ ok: false, status: 403 });
    expect(await app.changeMasterPassword('una-buena-clave', 'nueva-clave-1')).toEqual({ ok: true });
    await until(() => !app.vaultLocked);
    await app.lockSecrets();
    expect((await app.unlockSecrets('una-buena-clave')).ok).toBe(false);
    expect((await app.unlockSecrets('nueva-clave-1')).ok).toBe(true);
  });

  it('keyring: nothing to lock', async () => {
    const app = await started({ vault: 'keyring' });
    expect(app.secrets).toMatchObject({ backend: 'keyring', lockable: false, locked: false });
    expect(app.vaultLocked).toBe(false);
  });

  it('Google OAuth paste flow: bad URL, then the final URL connects the account', async () => {
    const app = await started();
    await until(() => app.live);
    const acc = await app.createAccount({ hoster: 'gdrive', kind: 'oauth', label: 'Drive nuevo' });
    expect(acc?.status).toBe('unknown');
    await app.googleOAuth(acc!.id);
    expect(app.oauth).toMatchObject({ accountId: acc!.id, label: 'Drive nuevo', blocked: true }); // no window in Node
    expect(app.oauth?.url).toBeTruthy();
    const bad = await app.completeGoogleOAuth('http://127.0.0.1:7979/api/v1/oauth/google/callback?state=otro&code=x');
    expect(bad.ok === false && bad.message).toMatch(/caducó/);
    expect(app.oauth).not.toBeNull();
    const [state] = mock!.oauthStates();
    const ok = await app.completeGoogleOAuth(`http://127.0.0.1:7979/api/v1/oauth/google/callback?state=${state}&code=demo-code`);
    expect(ok).toEqual({ ok: true });
    expect(app.oauth).toBeNull();
    await until(() => app.data.accounts[acc!.id]?.status === 'ok');
  });

  it('links/known reports the links already queued or in the LinkGrabber', async () => {
    const app = await started();
    const queued = Object.values(app.data.downloads)[0].url;
    const inGrabber = Object.values(app.data.grabber)[0].url;
    const known = await app.knownLinks([queued, inGrabber, 'https://example.com/nuevo.zip']);
    expect(known.sort()).toEqual([queued, inGrabber].sort());
  });
});
