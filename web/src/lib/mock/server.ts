// In-browser mock of the AdminDescargas API (VITE_MOCK=1 / npm run dev:mock).
// It is a fetch() replacement, so the real client, SSE parser and store are
// exercised. Progress is simulated on a timer and published as core.Event
// messages over a streamed text/event-stream response.
import type {
  Account, ConfirmRequest, CoreEvent, CreateAccountRequest, Download, EgressStatus, GrabberEntry, GrabberFile, Package,
  Progress, SecretsStatus, Settings, State, Stats,
} from '../api/types';
import { MIN_MASTER_PASSWORD } from '../api/types';
import type { FetchLike } from '../api/client';
import { fakeFolderFiles, seed, SERIES_FILES, type MockDB } from './data';

export interface MockOptions {
  /** Require POST /auth/login with password "admin" first. */
  login?: boolean;
  /**
   * Start logged out but with no password configured (loopback daemon):
   * POST /auth/login with "" opens the session.
   */
  openLogin?: boolean;
  /** Simulation tick in ms (0 = no timer; call tick() manually). */
  tickMs?: number;
  /** Simulated bytes per real second = speed / slowdown. */
  slowdown?: number;
  /** Network latency in ms. */
  latency?: number;
  now?: () => number;
  /**
   * Secret store: "unlocked" (default), "locked" (vault with master password
   * MOCK_MASTER_PASSWORD), "new" (vault without password yet), "keyring" or
   * "memory".
   */
  vault?: 'unlocked' | 'locked' | 'new' | 'keyring' | 'memory';
}

/** Master password of the mock vault. */
export const MOCK_MASTER_PASSWORD = 'demo1234';

export interface Mock {
  fetch: FetchLike;
  tick(dtMs: number): void;
  stop(): void;
  db: MockDB;
  /** States of the Google OAuth flows started (the demo consent page shows them). */
  oauthStates(): string[];
}

const json = (body: unknown, status = 200) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const fail = (status: number, code: string, message: string, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify({ error: { code, message } }), { status, headers: { 'Content-Type': 'application/json', ...headers } });
// The real daemon's accounts sentinels (English, translated by the client).
const LOCKED = () => fail(423, 'locked', 'accounts: secret store is locked');

const ACTIVE: State[] = ['resolving', 'downloading', 'verifying', 'postprocessing'];

export function createMock(opts: MockOptions = {}): Mock {
  const now = opts.now ?? (() => Date.now());
  const db = seed(now());
  const slowdown = opts.slowdown ?? 6;
  let authed = !opts.login && !opts.openLogin;
  let seq = 0;
  const log: { seq: number; text: string }[] = [];
  const subs = new Set<ReadableStreamDefaultController<Uint8Array>>();
  const enc = new TextEncoder();
  const rotating = new Set<string>(['tor']);
  let rotationDone = now() + 6000;
  let grabberTick = 0;
  let firstSub = true;

  // ---- secret store
  const vaultMode = opts.vault ?? 'unlocked';
  const lockable = vaultMode === 'locked' || vaultMode === 'unlocked' || vaultMode === 'new';
  let master = vaultMode === 'new' || !lockable ? '' : MOCK_MASTER_PASSWORD;
  const secrets: SecretsStatus = {
    backend: lockable ? 'vault' : vaultMode,
    lockable,
    initialized: vaultMode !== 'new',
    locked: vaultMode === 'locked' || vaultMode === 'new',
    warning: vaultMode === 'memory' ? 'No hay llavero del sistema ni archivo cifrado: las credenciales se pierden al reiniciar el servicio (demo).' : undefined,
  };
  // Unlock rate limit like the API's: 5 failures -> 30 s.
  let failures = 0;
  let blockedUntil = 0;
  // Google OAuth flows started (state -> account id).
  const oauthFlows = new Map<string, string>();

  function publish(ev: Omit<CoreEvent, 'seq' | 'time'>): void {
    const full: CoreEvent = { ...ev, seq: ++seq, time: new Date(now()).toISOString() } as CoreEvent;
    const text = `id: ${full.seq}\nevent: ${full.type}\ndata: ${JSON.stringify(full)}\n\n`;
    log.push({ seq: full.seq, text });
    if (log.length > 500) log.shift();
    for (const c of subs) {
      try {
        c.enqueue(enc.encode(text));
      } catch {
        subs.delete(c);
      }
    }
  }

  const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
  const nextId = () => ++db.nextId;
  const accountChanged = (a: Account) => publish({ type: 'account.updated', account_id: a.id, account: clone(a) });
  const secretsChanged = (m: 'locked' | 'unlocked') => publish({ type: 'secrets.updated', message: m });
  const blocked = (): Response | null => {
    const wait = Math.ceil((blockedUntil - now()) / 1000);
    return wait > 0 ? fail(429, 'rate_limited', 'demasiados intentos fallidos; espera antes de volver a intentarlo', { 'Retry-After': String(wait) }) : null;
  };
  const wrongPassword = (): Response => {
    if (++failures >= 5) {
      failures = 0;
      blockedUntil = now() + 30_000;
    }
    return fail(403, 'wrong_password', `Contraseña maestra incorrecta (en la demo es «${MOCK_MASTER_PASSWORD}»)`);
  };
  const weakPassword = () => fail(400, 'invalid_argument', `accounts: the master password must have at least ${MIN_MASTER_PASSWORD} characters`);

  /** A page standing in for Google's consent screen: it shows the final address to paste. */
  function consentURL(state: string): string {
    const final = `http://127.0.0.1:7979/api/v1/oauth/google/callback?state=${state}&code=demo-code&scope=drive`;
    const html = `<!doctype html><html lang="es"><meta charset="utf-8"><title>Google (demo)</title>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem;line-height:1.5">
<h1>Permiso de Google (demo)</h1><p>En la app real aquí está la pantalla de Google. Al aceptar, el navegador vuelve a esta dirección;
si el servicio está en otro equipo no carga: cópiala y pégala en AdminDescargas.</p>
<p><input readonly style="width:100%;font-family:monospace" value="${final}" onfocus="this.select()"></p></body></html>`;
    try {
      return URL.createObjectURL(new Blob([html], { type: 'text/html' }));
    } catch {
      return 'about:blank';
    }
  }

  function stats(): Stats {
    const s: Stats = { speed: 0, active: 0, queued: 0, waiting: 0, failed: 0, completed: 0, grabber_size: db.grabber.size, paused: db.paused };
    for (const d of db.downloads.values()) {
      if (d.state === 'downloading') {
        s.speed += d.speed;
        s.active++;
      } else if (d.state === 'queued') s.queued++;
      else if (d.state === 'waiting') s.waiting++;
      else if (d.state === 'failed') s.failed++;
      else if (d.state === 'completed') s.completed++;
    }
    return s;
  }

  function aggregate(p: Package): boolean {
    const kids = [...db.downloads.values()].filter((d) => d.package_id === p.id);
    const before = JSON.stringify([p.state, p.files_done, p.files_failed, p.files_total]);
    p.files_total = kids.length;
    p.size = kids.reduce((a, d) => a + Math.max(0, d.size), 0);
    p.done = kids.reduce((a, d) => a + d.done, 0);
    p.speed = kids.reduce((a, d) => a + (d.state === 'downloading' ? d.speed : 0), 0);
    p.eta = p.speed > 0 ? Math.round((p.size - p.done) / p.speed) : -1;
    p.files_done = kids.filter((d) => d.state === 'completed').length;
    p.files_failed = kids.filter((d) => d.state === 'failed').length;
    const states = new Set(kids.map((d) => d.state));
    if (kids.some((d) => ACTIVE.includes(d.state))) p.state = 'downloading';
    else if (states.size === 1) p.state = kids[0].state;
    else if (kids.length && kids.every((d) => d.state === 'completed' || d.state === 'failed')) p.state = p.files_failed ? 'failed' : 'completed';
    else if (states.has('waiting')) p.state = 'waiting';
    else if (states.has('queued')) p.state = 'queued';
    else p.state = kids[0]?.state ?? 'queued';
    return before !== JSON.stringify([p.state, p.files_done, p.files_failed, p.files_total]);
  }

  function updated(d: Download): void {
    publish({ type: 'download.updated', download: clone(d) });
    const p = db.packages.get(d.package_id);
    if (p && aggregate(p)) publish({ type: 'package.updated', package: clone(p) });
  }

  const SPEEDS: Record<string, number> = { mega: 9e6, gdrive: 6e6, mediafire: 4.5e6, terabox: 3e6, generic: 7e6, pixeldrain: 11e6 };

  function start(d: Download): void {
    d.state = 'downloading';
    d.speed = SPEEDS[d.hoster] ?? 5e6;
    d.connections = d.resumable === false ? 1 : 4;
    d.started_at = new Date(now()).toISOString();
    d.error = undefined;
    d.error_kind = undefined;
    d.quota_scope = undefined;
    d.waiting_until = undefined;
    if (!d.egress_id) d.egress_id = 'direct';
    updated(d);
  }

  function schedule(): void {
    if (db.paused) return;
    const max = Number(db.settings.downloads?.max_active ?? 3);
    let active = [...db.downloads.values()].filter((d) => d.state === 'downloading').length;
    const queued = [...db.downloads.values()].filter((d) => d.state === 'queued').sort((a, b) => b.priority - a.priority || a.id - b.id);
    for (const d of queued) {
      if (active >= max) break;
      start(d);
      active++;
    }
  }

  function tick(dtMs: number): void {
    const t = now();
    const prog: Progress[] = [];
    for (const d of db.downloads.values()) {
      if (d.state === 'downloading') {
        const jitter = 0.85 + Math.random() * 0.3;
        d.speed = Math.max(2e5, Math.round((d.speed * 0.8 + (SPEEDS[d.hoster] ?? 5e6) * jitter * 0.2)));
        if (d.name === 'clase_01.mp4' && d.done / d.size < 0.95) d.speed = Math.round(9.6e6 * jitter);
        d.done = Math.min(d.size, d.done + Math.round((d.speed * dtMs) / 1000 / slowdown));
        d.eta = Math.round((d.size - d.done) / d.speed);
        // video_presentacion.mp4 keeps failing: Drive per-file quota.
        if (d.name === 'video_presentacion.mp4' && d.done > 0) {
          Object.assign(d, { state: 'failed', speed: 0, eta: -1, done: 0, error_kind: 'quota', quota_scope: 'file', error: 'quota (file): se superó la cuota de descargas de este archivo en Google Drive', attempts: d.attempts + 1 });
          updated(d);
          publish({ type: 'notice', level: 'error', message: 'video_presentacion.mp4: cuota del archivo en Google Drive. Rotar la IP no ayuda.' });
          continue;
        }
        if (d.done >= d.size) {
          Object.assign(d, { state: 'completed', speed: 0, eta: -1, connections: 0, completed_at: new Date(t).toISOString() });
          updated(d);
          publish({ type: 'notice', level: 'success', message: `${d.name} se descargó` });
          continue;
        }
        prog.push({ id: d.id, package_id: d.package_id, state: d.state, done: d.done, size: d.size, speed: d.speed, eta: d.eta, connections: d.connections, egress_id: d.egress_id, public_ip: d.public_ip });
      } else if (d.state === 'waiting' && d.waiting_until && Date.parse(d.waiting_until) <= t) {
        d.egress_id = 'wg:mullvad-se';
        d.public_ip = '203.0.113.24';
        start(d);
      }
    }
    // Rotation of Tor finishes a few seconds after start.
    if (rotating.size && t >= rotationDone) {
      for (const id of rotating) {
        const e = db.egress.get(id);
        if (e) e.public_ip = `185.220.101.${10 + Math.floor(Math.random() * 200)}`;
        publish({ type: 'rotation', egress_id: id, message: 'finished' });
        publish({ type: 'egress.updated', egress_id: id });
      }
      rotating.clear();
    }
    // Progressive listing of the MEGA folder in the LinkGrabber.
    grabberTick += dtMs;
    if (grabberTick >= 1000) {
      grabberTick = 0;
      for (const g of db.grabber.values()) {
        if (g.state !== 'resolving' && g.state !== 'pending') continue;
        const src = g.id === 1 ? SERIES_FILES : fakeFolderFiles(g.package_name);
        const have = g.files?.length ?? 0;
        const add = src.slice(have, have + (g.is_folder ? 3 : 1));
        g.state = 'resolving';
        g.files = [...(g.files ?? []), ...add.map(([rel, size], i) => ({ index: have + i, name: rel.split('/').pop()!, rel_path: g.is_folder ? rel : undefined, size }))];
        if (!g.is_folder || g.files.length >= src.length) {
          g.state = 'ready';
          g.total_size = g.files.reduce((a, f) => a + f.size, 0);
        }
        publish({ type: 'linkgrabber.updated', linkgrabber: clone(g) });
      }
    }
    schedule();
    for (const p of db.packages.values()) if (aggregate(p)) publish({ type: 'package.updated', package: clone(p) });
    if (prog.length) publish({ type: 'progress', progress: prog });
    publish({ type: 'stats', stats: stats() });
  }

  // ---- actions

  function actDownload(action: string, d: Download, deleteFiles: boolean): void {
    switch (action) {
      case 'pause':
        if (d.state === 'completed' || d.state === 'failed') return;
        Object.assign(d, { state: 'paused', speed: 0, eta: -1, waiting_until: undefined });
        return updated(d);
      case 'resume':
        if (d.state !== 'paused') return;
        d.state = 'queued';
        updated(d);
        return schedule();
      case 'retry':
        if (d.state !== 'failed') return;
        Object.assign(d, { state: 'queued', error: undefined, error_kind: undefined, quota_scope: undefined });
        updated(d);
        return schedule();
      case 'restart':
        Object.assign(d, { state: 'queued', done: 0, speed: 0, eta: -1, error: undefined, error_kind: undefined, quota_scope: undefined, waiting_until: undefined });
        updated(d);
        return schedule();
      case 'delete': {
        void deleteFiles;
        db.downloads.delete(d.id);
        publish({ type: 'download.removed', id: d.id });
        const p = db.packages.get(d.package_id);
        if (p && ![...db.downloads.values()].some((x) => x.package_id === p.id)) {
          db.packages.delete(p.id);
          publish({ type: 'package.removed', id: p.id });
        } else if (p && aggregate(p)) publish({ type: 'package.updated', package: clone(p) });
      }
    }
  }

  function hosterOf(url: string): { hoster: string; folder: boolean; name: string } | null {
    let u: URL;
    try {
      u = new URL(url);
    } catch {
      return null;
    }
    const h = u.hostname.replace(/^www\./, '');
    const last = decodeURIComponent(u.pathname.split('/').filter(Boolean).pop() ?? 'archivo');
    const map: [RegExp, string, RegExp][] = [
      [/mega\.(nz|co\.nz)$/, 'mega', /\/folder\/|#F!/],
      [/mediafire\.com$/, 'mediafire', /\/folder\//],
      [/drive\.google\.com$/, 'gdrive', /\/folders\//],
      [/terabox|1024tera/, 'terabox', /\/s\//],
      [/pixeldrain\.com$/, 'pixeldrain', /\/l\//],
      [/gofile\.io$/, 'gofile', /\/d\//],
    ];
    for (const [re, hoster, folder] of map) {
      if (re.test(h)) {
        const isFolder = folder.test(u.pathname + u.hash);
        return { hoster, folder: isFolder, name: isFolder ? (last.length > 12 ? `Carpeta_${last.slice(0, 6)}` : last) : last.includes('.') ? last : `${last}.bin` };
      }
    }
    return { hoster: 'generic', folder: false, name: last.includes('.') ? last : `${last}.bin` };
  }

  function addLinks(text: string, urls: string[] = [], source = 'api'): GrabberEntry[] {
    const found = [...(text.match(/https?:\/\/[^\s<>"')]+/g) ?? []), ...urls];
    const out: GrabberEntry[] = [];
    for (const url of [...new Set(found)]) {
      const h = hosterOf(url);
      const id = nextId();
      const g: GrabberEntry = {
        id, url, hoster: h?.hoster, state: h ? 'pending' : 'unsupported', package_name: h?.name ?? url, is_folder: !!h?.folder,
        files: [], total_size: -1, source, added_at: new Date(now()).toISOString(), error: h ? undefined : 'Ningún hoster reconoce este enlace',
      };
      if (url.includes('error')) Object.assign(g, { state: 'error', error: 'El archivo no existe o fue eliminado' });
      db.grabber.set(id, g);
      publish({ type: 'linkgrabber.added', linkgrabber: clone(g) });
      out.push(clone(g));
    }
    return out;
  }

  function confirm(req: ConfirmRequest): Package[] {
    const out: Package[] = [];
    for (const item of req.items) {
      const g = db.grabber.get(item.entry_id);
      if (!g || g.state !== 'ready') continue;
      let files: GrabberFile[] = g.files ?? [];
      if (item.file_indexes?.length) files = files.filter((f) => item.file_indexes!.includes(f.index));
      const name = item.package_name || g.package_name;
      const p: Package = {
        id: nextId(), name, dest_dir: `${req.dest_dir || db.settings.download_dir}/${g.is_folder ? name : ''}`.replace(/\/$/, ''),
        source_url: g.url, hoster: g.hoster, is_folder: g.is_folder, state: req.paused ? 'paused' : 'queued', size: 0, done: 0, speed: 0,
        eta: -1, files_total: files.length, files_done: 0, files_failed: 0, priority: req.priority ?? 0,
        exists_policy: req.exists_policy || 'skip', created_at: new Date(now()).toISOString(),
      };
      db.packages.set(p.id, p);
      publish({ type: 'package.added', package: clone(p) });
      for (const f of files) {
        const d: Download = {
          id: nextId(), package_id: p.id, hoster: g.hoster ?? 'generic', url: g.url, name: f.name, rel_path: f.rel_path, dest_path: `${p.dest_dir}/${f.rel_path ?? f.name}`,
          state: req.paused ? 'paused' : 'queued', size: f.size, done: 0, speed: 0, eta: -1, connections: 0, attempts: 0, priority: 0,
          resumable: g.hoster !== 'generic', created_at: new Date(now()).toISOString(),
        };
        db.downloads.set(d.id, d);
        publish({ type: 'download.added', download: clone(d) });
      }
      aggregate(p);
      db.grabber.delete(g.id);
      publish({ type: 'linkgrabber.removed', id: g.id });
      out.push(clone(p));
    }
    schedule();
    return out;
  }

  // ---- event stream

  function events(lastId: number, signal?: AbortSignal | null): Response {
    let ctrl: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        ctrl = c;
        c.enqueue(enc.encode(': adesc mock\nretry: 2000\n\n'));
        for (const e of log) if (lastId && e.seq > lastId) c.enqueue(enc.encode(e.text));
        subs.add(c);
        if (firstSub) {
          firstSub = false;
          rotationDone = now() + 8000;
        }
        // Rotation state is only known from events: tell the new subscriber.
        setTimeout(() => {
          for (const id of rotating) publish({ type: 'rotation', egress_id: id, message: 'started' });
        }, 0);
      },
      cancel() {
        subs.delete(ctrl);
      },
    });
    signal?.addEventListener('abort', () => {
      subs.delete(ctrl);
      try {
        ctrl.error(new DOMException('aborted', 'AbortError'));
      } catch {
        /* closed */
      }
    });
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
  }

  // ---- router

  async function route(method: string, path: string, q: URLSearchParams, body: any, headers: Headers, signal?: AbortSignal | null): Promise<Response> {
    if (path === 'auth/login' && method === 'POST') {
      if (body?.password === 'admin' || (opts.openLogin && body?.password === '')) {
        authed = true;
        return json({ csrf: 'mock-csrf' });
      }
      return fail(401, 'bad_password', 'Contraseña incorrecta');
    }
    if (path === 'auth/session') return json({ authenticated: authed, method: authed ? 'cookie' : '', csrf: authed ? 'mock-csrf' : undefined, password_required: !!opts.login });
    if (!authed) return fail(401, 'unauthorized', 'Hay que iniciar sesión');
    const m = (re: RegExp) => re.exec(path);
    let r: RegExpExecArray | null;

    if (path === 'events') return events(Number(headers.get('Last-Event-ID') ?? 0), signal);
    if (path === 'health') return json({ ok: true });
    if (path === 'stats') return json(stats());
    if (path === 'ip') return json({ egresses: [...db.egress.values()].map((e) => ({ id: e.id, kind: e.kind, public_ip: e.public_ip ?? '', error: e.healthy ? undefined : e.last_error })) });

    if (path === 'downloads' && method === 'GET') {
      let list = [...db.downloads.values()];
      if (q.get('state')) list = list.filter((d) => d.state === q.get('state'));
      if (q.get('package_id')) list = list.filter((d) => d.package_id === Number(q.get('package_id')));
      if (q.get('q')) list = list.filter((d) => d.name.toLowerCase().includes(q.get('q')!.toLowerCase()));
      return json(clone(list));
    }
    if (path === 'downloads/retry-failed') {
      for (const d of db.downloads.values()) if (d.state === 'failed') actDownload('retry', d, false);
      return json({ ok: true });
    }
    if ((r = m(/^downloads\/(pause|resume|restart|retry|delete)$/))) {
      for (const id of body?.ids ?? []) {
        const d = db.downloads.get(id);
        if (d) actDownload(r[1], d, !!body?.delete_files);
      }
      return json({ ok: true });
    }
    if ((r = m(/^downloads\/(\d+)$/))) {
      const d = db.downloads.get(Number(r[1]));
      return d ? json(clone(d)) : fail(404, 'not_found', 'La descarga no existe');
    }
    if (path === 'packages' && method === 'GET') return json(clone([...db.packages.values()]));
    if ((r = m(/^packages\/(\d+)\/rescan$/))) {
      const p = db.packages.get(Number(r[1]));
      if (!p) return fail(404, 'not_found', 'El paquete no existe');
      publish({ type: 'notice', level: 'info', message: `«${p.name}»: no hay archivos nuevos` });
      return json({ added: 0 });
    }
    if ((r = m(/^packages\/(pause|resume|restart|retry|delete)$/))) {
      for (const pid of body?.ids ?? []) {
        for (const d of [...db.downloads.values()].filter((x) => x.package_id === pid)) actDownload(r[1], d, !!body?.delete_files);
      }
      return json({ ok: true });
    }
    if ((r = m(/^packages\/(\d+)$/))) {
      const p = db.packages.get(Number(r[1]));
      return p ? json({ package: clone(p), downloads: clone([...db.downloads.values()].filter((d) => d.package_id === p.id)) }) : fail(404, 'not_found', 'El paquete no existe');
    }
    if (path === 'pause-all') {
      db.paused = true;
      for (const d of db.downloads.values()) if (d.state === 'downloading') Object.assign(d, { state: 'queued', speed: 0, eta: -1 }), updated(d);
      publish({ type: 'stats', stats: stats() });
      return json({ ok: true });
    }
    if (path === 'resume-all') {
      db.paused = false;
      schedule();
      publish({ type: 'stats', stats: stats() });
      return json({ ok: true });
    }

    if (path === 'links' && method === 'POST') {
      const entries = addLinks(String(body?.text ?? ''), body?.urls ?? [], body?.source);
      return json(entries);
    }
    if (path === 'linkgrabber') return json(clone([...db.grabber.values()]));
    if (path === 'linkgrabber/confirm') return json(confirm(body as ConfirmRequest));
    if (path === 'linkgrabber/remove') {
      for (const id of body?.ids ?? []) if (db.grabber.delete(id)) publish({ type: 'linkgrabber.removed', id });
      return json({ ok: true });
    }

    if (path === 'secrets' && method === 'GET') return json(clone(secrets));
    if (path === 'secrets/unlock') {
      const b = blocked();
      if (b) return b;
      const pw = String(body?.password ?? '');
      if (!pw) return fail(400, 'invalid_argument', 'falta password');
      if (!secrets.lockable) return fail(409, 'invalid_state', 'accounts: the secret store has no master password');
      if (!secrets.initialized) {
        if (pw.length < MIN_MASTER_PASSWORD) return weakPassword();
        master = pw;
        secrets.initialized = true;
      } else if (pw !== master) return wrongPassword();
      failures = 0;
      if (secrets.locked) {
        secrets.locked = false;
        secretsChanged('unlocked');
      }
      return json(clone(secrets));
    }
    if (path === 'secrets/lock') {
      if (!secrets.lockable) return fail(409, 'invalid_state', 'accounts: the secret store has no master password');
      if (!secrets.locked) {
        secrets.locked = true;
        secretsChanged('locked');
      }
      return json(clone(secrets));
    }
    if (path === 'secrets/password') {
      const b = blocked();
      if (b) return b;
      if (!secrets.lockable) return fail(409, 'invalid_state', 'accounts: the secret store has no master password');
      if (!body?.old_password || !body?.new_password) return fail(400, 'invalid_argument', 'faltan old_password o new_password');
      if (secrets.initialized && body.old_password !== master) return wrongPassword();
      if (String(body.new_password).length < MIN_MASTER_PASSWORD) return weakPassword();
      master = String(body.new_password);
      failures = 0;
      secrets.initialized = true;
      secrets.locked = false;
      secretsChanged('unlocked');
      return json({ ok: true });
    }
    if (path === 'links/known') {
      const urls: string[] = Array.isArray(body?.urls) ? body.urls : [];
      if (urls.length > 1000) return fail(400, 'invalid_argument', 'como mucho 1000 enlaces por petición');
      const have = new Set([...[...db.downloads.values()].map((d) => d.url), ...[...db.grabber.values()].map((g) => g.url)]);
      const norm = [...new Set(urls.map((u) => String(u).trim()).filter(Boolean))];
      return json({ known: norm.filter((u) => have.has(u)) });
    }

    if (path === 'accounts' && method === 'GET') return json(clone(db.accounts));
    if (path === 'accounts' && method === 'POST') {
      const req = body as CreateAccountRequest;
      if (!req?.hoster || !req.kind) return fail(400, 'invalid_argument', 'Faltan el hoster o el tipo de cuenta');
      if (secrets.locked) return LOCKED();
      const a: Account = {
        id: `acc-${nextId()}`, hoster: req.hoster, label: req.label || req.hoster, kind: req.kind, username: req.username ?? '', enabled: true,
        allow_rotation: !!req.allow_rotation, status: req.kind === 'oauth' ? 'unknown' : 'ok', premium: false, quota_total: -1, quota_used: -1,
        checked_at: new Date(now()).toISOString(), created_at: new Date(now()).toISOString(), updated_at: new Date(now()).toISOString(),
      };
      if (req.hoster === 'mega' && req.password === '2fa') a.status = 'needs_2fa';
      db.accounts.push(a);
      accountChanged(a);
      return json(clone(a), 201);
    }
    if (path === 'accounts/import-cookies') {
      if (!String(body?.cookies ?? '').trim()) return fail(400, 'invalid_argument', 'No hay cookies en el texto');
      if (secrets.locked) return LOCKED();
      const a: Account = {
        id: `acc-${nextId()}`, hoster: body.hoster, label: body.label || body.hoster, kind: 'cookie', username: '', enabled: true, allow_rotation: false,
        status: 'ok', premium: false, quota_total: -1, quota_used: -1, created_at: new Date(now()).toISOString(), updated_at: new Date(now()).toISOString(),
      };
      db.accounts.push(a);
      accountChanged(a);
      return json(clone(a), 201);
    }
    if ((r = m(/^accounts\/([^/]+)\/check$/))) {
      const a = db.accounts.find((x) => x.id === decodeURIComponent(r![1]));
      if (!a) return fail(404, 'not_found', 'La cuenta no existe');
      a.checked_at = new Date(now()).toISOString();
      if (secrets.locked) {
        // Like the manager: the check is recorded as an error, and 423.
        Object.assign(a, { status: 'error', status_msg: 'El almacén de credenciales está bloqueado: desbloquéalo con la contraseña maestra.' });
        accountChanged(a);
        return LOCKED();
      }
      await new Promise((res) => setTimeout(res, opts.latency === 0 ? 0 : 700));
      if (a.status === 'error' || a.status === 'unknown' || a.status === 'checking') Object.assign(a, { status: 'ok', status_msg: undefined });
      accountChanged(a);
      return json(clone(a));
    }
    if ((r = m(/^accounts\/([^/]+)\/2fa$/))) {
      const a = db.accounts.find((x) => x.id === decodeURIComponent(r![1]));
      if (!a) return fail(404, 'not_found', 'La cuenta no existe');
      if (!/^\d{6}$/.test(String(body?.code ?? ''))) return fail(400, 'invalid_argument', 'El código tiene 6 cifras');
      if (secrets.locked) return LOCKED();
      if (body.code !== '123456') return fail(400, 'invalid_code', 'Código incorrecto (en la demo es 123456)');
      Object.assign(a, { status: 'ok', status_msg: undefined, checked_at: new Date(now()).toISOString(), quota_total: 400e9, quota_used: 12e9 });
      accountChanged(a);
      return json(clone(a));
    }
    if ((r = m(/^accounts\/([^/]+)$/))) {
      const i = db.accounts.findIndex((x) => x.id === decodeURIComponent(r![1]));
      if (i < 0) return fail(404, 'not_found', 'La cuenta no existe');
      if (method === 'DELETE') {
        if (secrets.locked) return LOCKED(); // the secret is deleted first
        const [gone] = db.accounts.splice(i, 1);
        publish({ type: 'account.removed', account_id: gone.id });
        return json({ ok: true });
      }
      if (method === 'PATCH') {
        const { password: _p, api_key: _k, ...meta } = body ?? {};
        if ((_p || _k) && secrets.locked) return LOCKED();
        Object.assign(db.accounts[i], meta, { updated_at: new Date(now()).toISOString() });
        if (meta.enabled === false) db.accounts[i].status = 'disabled';
        else if (meta.enabled === true && db.accounts[i].status === 'disabled') db.accounts[i].status = 'unknown';
        accountChanged(db.accounts[i]);
        return json(clone(db.accounts[i]));
      }
    }
    if (path === 'oauth/google/start') {
      const id = q.get('account_id') ?? '';
      const a = db.accounts.find((x) => x.id === id);
      if (!id) return fail(400, 'invalid_argument', 'falta account_id');
      if (!a) return fail(404, 'not_found', 'accounts: account not found');
      if (a.kind !== 'oauth') return fail(400, 'invalid_argument', `accounts: invalid account: account ${id} is not an OAuth account`);
      const state = `demo${nextId()}`;
      oauthFlows.set(state, id);
      return json({ url: consentURL(state) });
    }
    if (path === 'oauth/google/complete') {
      const raw = String(body?.url ?? '').trim();
      if (!raw) return fail(400, 'invalid_argument', 'falta url');
      let u: URL | null = null;
      try {
        u = new URL(raw);
      } catch {
        u = null;
      }
      const stateErr = () => fail(400, 'invalid_argument', 'accounts: OAuth state is invalid or expired: paste the full address shown by the browser after authorizing');
      if (!u || !u.search) return stateErr();
      if (u.searchParams.get('error')) return fail(400, 'invalid_argument', 'accounts: Google authorization was denied');
      const id = oauthFlows.get(u.searchParams.get('state') ?? '');
      if (!id || !u.searchParams.get('code')) return stateErr();
      if (secrets.locked) return LOCKED();
      oauthFlows.delete(u.searchParams.get('state')!);
      const a = db.accounts.find((x) => x.id === id);
      if (!a) return fail(404, 'not_found', 'accounts: account not found');
      Object.assign(a, { status: 'ok', status_msg: undefined, username: a.username || 'demo@gmail.example', checked_at: new Date(now()).toISOString(), quota_total: 15e9, quota_used: 2e9 });
      accountChanged(a);
      return json({ ok: true });
    }

    if (path === 'egress' && method === 'GET') return json(clone([...db.egress.values()]));
    if (path === 'egress/import') {
      const lines = String(body?.text ?? '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
      let n = 0;
      const errors: { line: number; message: string }[] = [];
      for (const [i, l] of lines.entries()) {
        const mm = /^(?:(socks5h?|socks|https?):\/\/)?(?:[^@]+@)?([\w.-]+):(\d+)/i.exec(l);
        if (!mm) {
          errors.push({ line: i + 1, message: 'formato no reconocido' });
          continue;
        }
        const kind = (mm[1] ?? 'http').toLowerCase().startsWith('socks') ? 'socks5' : 'http';
        const id = `${kind}:${mm[2]}:${mm[3]}`;
        db.egress.set(id, { id, kind, scope: 'per-connection', label: `${mm[2]}:${mm[3]}`, healthy: true, public_ip: mm[2], in_use: 0, checked_at: new Date(now()).toISOString() });
        n++;
      }
      return json({ added: n, skipped: errors.length, errors });
    }
    if (path === 'egress/wireguard') {
      if (!/\[Interface\]/i.test(String(body?.conf ?? ''))) return fail(400, 'invalid_argument', 'El archivo no parece un .conf de WireGuard (falta [Interface])');
      const id = `wg:${String(body?.name || 'tunel').toLowerCase().replace(/[^a-z0-9-]+/g, '-')}`;
      db.egress.set(id, { id, kind: 'wireguard', scope: 'per-connection', label: body?.name || id, healthy: true, public_ip: '203.0.113.99', in_use: 0, checked_at: new Date(now()).toISOString() });
      return json({ id });
    }
    if ((r = m(/^egress\/([^/]+)\/test$/))) {
      const e = db.egress.get(decodeURIComponent(r[1]));
      if (!e) return fail(404, 'not_found', 'La salida no existe');
      await new Promise((res) => setTimeout(res, 600));
      e.checked_at = new Date(now()).toISOString();
      if (e.id === 'socks5:proxy-2') e.last_error = 'conexión rechazada';
      else e.healthy = true;
      return json(clone(e));
    }
    if (path === 'rotation/rotate') {
      const id = String(body?.egress_id ?? '');
      if (id && !db.egress.has(id)) return fail(404, 'not_found', 'La salida no existe');
      rotating.add(id);
      rotationDone = now() + 2500;
      publish({ type: 'rotation', egress_id: id, message: 'started' });
      if (!id) {
        const d = db.egress.get('direct');
        if (d) d.public_ip = `198.51.100.${2 + Math.floor(Math.random() * 200)}`;
      }
      return json({ ok: true });
    }
    if (path === 'settings' && method === 'GET') return json(clone(db.settings));
    if (path === 'settings' && method === 'PUT') {
      const s = body as Settings;
      // Commands are read-only through the API: keep the stored ones.
      if (!s?.download_dir) return fail(400, 'invalid_argument', 'download_dir no puede estar vacío');
      db.settings = { ...s, server: db.settings.server, network: db.settings.network };
      return json(clone(db.settings));
    }
    return fail(404, 'not_found', `Ruta desconocida en el mock: ${method} ${path}`);
  }

  const fetchImpl: FetchLike = async (input, init = {}) => {
    const url = new URL(input, 'http://mock.invalid/');
    const i = url.pathname.indexOf('/api/v1/');
    if (i < 0) return new Response('not found', { status: 404 });
    const path = url.pathname.slice(i + 8);
    const method = (init.method ?? 'GET').toUpperCase();
    let body: unknown;
    if (typeof init.body === 'string' && init.body) body = JSON.parse(init.body);
    if (opts.latency ?? 60) await new Promise((res) => setTimeout(res, opts.latency ?? 60));
    return route(method, path, url.searchParams, body, new Headers(init.headers), init.signal);
  };

  for (const p of db.packages.values()) aggregate(p);
  // The mock forgets everything on reload: keep the appearance the user
  // picked (mirrored in localStorage by the app) so dev:mock behaves like a
  // real server.
  try {
    const a = JSON.parse(localStorage.getItem('adesc.appearance') ?? 'null');
    if (a && typeof a === 'object') db.settings.appearance = { ...db.settings.appearance, ...a };
  } catch {
    /* no storage (tests) */
  }

  let timer: ReturnType<typeof setInterval> | null = null;
  if (opts.tickMs !== 0) {
    const ms = opts.tickMs ?? 500;
    timer = setInterval(() => tick(ms), ms);
  }

  return {
    fetch: fetchImpl,
    tick,
    db,
    oauthStates: () => [...oauthFlows.keys()],
    stop() {
      if (timer) clearInterval(timer);
      for (const c of subs) {
        try {
          c.close();
        } catch {
          /* closed */
        }
      }
      subs.clear();
    },
  };
}

export type { EgressStatus };
