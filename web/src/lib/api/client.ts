// Typed REST client for /api/v1 (PLAN.md section 7). Two auth modes:
//  - desktop: GET /__adesc/connection on the page origin returns
//    {baseURL, token}; requests carry Authorization: Bearer <token>.
//  - web: same origin, HttpOnly session cookie + X-CSRF-Token header on
//    mutating requests; 401 -> login screen (POST /api/v1/auth/login).
import type {
  Account, AccountPatch, AddLinksRequest, ApiErrorBody, ClipboardStatus, ConfirmRequest, CreateAccountRequest,
  Download, DownloadAction, EgressStatus, GrabberEntry, ImportResult, KnownLinksResponse, Package, PublicIPResponse, SecretsStatus,
  SessionInfo, Settings, State, Stats,
} from './types';
import { MIN_MASTER_PASSWORD } from './types';

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface Connection {
  mode: 'desktop' | 'web' | 'mock';
  /** Absolute URL ending in "/" under which "api/v1/..." lives. */
  baseURL: string;
  token?: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    /** Seconds from the Retry-After header (429), if any. */
    readonly retryAfter?: number,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/** Parses Retry-After (delta-seconds or an HTTP date) into seconds. */
export function parseRetryAfter(v: string | null | undefined, now = Date.now()): number | undefined {
  if (!v) return undefined;
  const t = v.trim();
  if (/^\d+$/.test(t)) return Number(t);
  const at = Date.parse(t);
  return Number.isNaN(at) ? undefined : Math.max(0, Math.ceil((at - now) / 1000));
}

/** Spanish texts for the accounts package's English sentinel errors ("accounts: ..."). */
const ACCOUNTS_ERRORS: [RegExp, string][] = [
  [/master password must have at least/, `La contraseña maestra debe tener al menos ${MIN_MASTER_PASSWORD} caracteres.`],
  [/wrong master password/, 'La contraseña maestra no es correcta.'],
  [/secret store is locked/, 'Las credenciales están bloqueadas: desbloquéalas con la contraseña maestra (en Cuentas) para usar o guardar cuentas.'],
  [/has no master password/, 'Este almacén de credenciales (llavero del sistema) no usa contraseña maestra.'],
  [/vault file is corrupted/, 'El archivo cifrado de credenciales está dañado o fue modificado.'],
  [/secret not found/, 'No hay credenciales guardadas para esta cuenta.'],
  [/unrecognized cookie format/, 'No se reconoce el formato de las cookies (usa cookies.txt de Netscape o JSON).'],
  [/no cookies for this hoster/, 'El texto no tiene cookies de este hoster.'],
  [/cookies for this hoster have expired/, 'Todas las cookies de este hoster han caducado: expórtalas de nuevo.'],
  [/OAuth client is not configured/, 'Falta configurar el cliente OAuth de Google (client_id y client_secret de hosters.gdrive en config.toml).'],
  [/OAuth state is invalid or expired/, 'La autorización de Google caducó o no es de esta conexión. Vuelve a pulsar «Conectar con Google» y pega la dirección completa.'],
  [/authorization was denied/, 'Google denegó la autorización.'],
  [/Drive permission was not granted/, 'No se concedió el permiso de Google Drive: márcalo en la pantalla de Google.'],
  [/invalid OAuth redirect URL/, 'Google solo vuelve a direcciones locales (127.0.0.1 o localhost): abre AdminDescargas con una de ellas (por ejemplo, con un túnel SSH) para conectar la cuenta.'],
  [/account not found/, 'La cuenta no existe.'],
  [/account checks are not supported/, 'Este hoster no permite comprobar cuentas.'],
  [/2FA code must have/, 'El código de verificación tiene de 6 a 8 cifras.'],
];

/**
 * User-facing Spanish text for an API error. The API already answers most
 * errors in Spanish; the accounts package's sentinels ("accounts: ...") and
 * the vault codes are translated here. 429 shows the Retry-After wait.
 */
export function localizeError(status: number, code: string, message: string, retryAfter?: number): string {
  if (code === 'rate_limited' || status === 429) {
    const base = 'Demasiados intentos fallidos.';
    return retryAfter ? `${base} Espera ${retryAfter} s antes de volver a intentarlo.` : message || `${base} Espera un poco antes de volver a intentarlo.`;
  }
  const internal = message.includes('accounts: ');
  if (internal) for (const [re, es] of ACCOUNTS_ERRORS) if (re.test(message)) return es;
  if (code === 'locked' && (internal || !message)) return ACCOUNTS_ERRORS[2][1];
  if (code === 'wrong_password' && (internal || !message)) return ACCOUNTS_ERRORS[1][1];
  return internal ? message.replace(/accounts: /g, '') : message;
}

const CSRF_KEY = 'adesc.csrf';

function withSlash(u: string): string {
  return u.endsWith('/') ? u : u + '/';
}

/** Discovers how to reach the API (desktop binding vs same origin). */
export async function discoverConnection(fetchImpl: FetchLike, pageURL: string): Promise<Connection> {
  const origin = new URL(pageURL).origin;
  try {
    const r = await fetchImpl(origin + '/__adesc/connection', { credentials: 'omit', cache: 'no-store' });
    if (r.ok && (r.headers.get('content-type') ?? '').includes('json')) {
      const j = (await r.json()) as { baseURL?: string; token?: string };
      if (j && typeof j.baseURL === 'string' && j.baseURL) {
        return { mode: 'desktop', baseURL: withSlash(new URL(j.baseURL, pageURL).href), token: j.token };
      }
    }
  } catch {
    /* not the desktop app */
  }
  return { mode: 'web', baseURL: withSlash(new URL('./', pageURL).href) };
}

function readCookie(name: string): string {
  if (typeof document === 'undefined') return '';
  const m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
  return m ? decodeURIComponent(m[1]) : '';
}

export class Api {
  /** Called on 401 so the app can show the login screen. */
  onUnauthorized: (() => void) | null = null;
  private csrf = '';

  constructor(
    readonly conn: Connection,
    private readonly fetchImpl: FetchLike,
  ) {
    try {
      this.csrf = sessionStorage.getItem(CSRF_KEY) ?? '';
    } catch {
      this.csrf = '';
    }
  }

  url(path: string, query?: Record<string, string | number | undefined>): string {
    const u = new URL('api/v1/' + path.replace(/^\//, ''), this.conn.baseURL);
    for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined && v !== '') u.searchParams.set(k, String(v));
    return u.href;
  }

  headers(mutating: boolean, json: boolean): Record<string, string> {
    const h: Record<string, string> = { Accept: 'application/json' };
    if (json) h['Content-Type'] = 'application/json';
    if (this.conn.token) h.Authorization = `Bearer ${this.conn.token}`;
    else if (mutating) {
      const t = this.csrf || readCookie('adesc_csrf') || readCookie('csrf_token');
      if (t) h['X-CSRF-Token'] = t;
    }
    return h;
  }

  get credentials(): RequestCredentials {
    return this.conn.token ? 'omit' : 'same-origin';
  }

  /** Low-level fetch with auth; used by the SSE stream too. */
  raw(path: string, init: RequestInit & { query?: Record<string, string | number | undefined> } = {}): Promise<Response> {
    const method = (init.method ?? 'GET').toUpperCase();
    const headers = { ...this.headers(method !== 'GET', init.body !== undefined), ...(init.headers as Record<string, string>) };
    return this.fetchImpl(this.url(path, init.query), { ...init, method, headers, credentials: this.credentials });
  }

  async request<T>(method: string, path: string, body?: unknown, query?: Record<string, string | number | undefined>): Promise<T> {
    let r: Response;
    try {
      r = await this.raw(path, { method, query, body: body === undefined ? undefined : JSON.stringify(body) });
    } catch (e) {
      throw new ApiError(0, 'network', 'No hay conexión con el servicio de descargas');
    }
    const text = await r.text();
    let data: unknown = undefined;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = text;
      }
    }
    if (!r.ok) {
      const e = (data as ApiErrorBody | undefined)?.error;
      const code = typeof e === 'object' && e?.code ? e.code : `http_${r.status}`;
      let msg = typeof e === 'string' ? e : (e?.message ?? `Error ${r.status}`);
      if (r.status === 401) {
        if (path !== 'auth/login') this.onUnauthorized?.();
        if (!e) msg = 'Hay que iniciar sesión';
      }
      const retryAfter = parseRetryAfter(r.headers?.get('Retry-After'));
      throw new ApiError(r.status, code, localizeError(r.status, code, msg, retryAfter), retryAfter);
    }
    return data as T;
  }

  // --- auth
  setCsrf(token: string): void {
    this.csrf = token;
    try {
      sessionStorage.setItem(CSRF_KEY, token);
    } catch {
      /* ignore */
    }
  }
  async login(password: string): Promise<void> {
    const r = await this.request<{ csrf?: string }>('POST', 'auth/login', { password });
    this.setCsrf(r?.csrf ?? '');
  }
  /** Public: whether we are logged in, the CSRF token after a reload, and if a password exists. */
  session() {
    return this.request<SessionInfo>('GET', 'auth/session');
  }

  // --- downloads
  listDownloads(f: { state?: State; package_id?: number; q?: string } = {}) {
    return this.request<Download[]>('GET', 'downloads', undefined, f);
  }
  getDownload(id: number) {
    return this.request<Download>('GET', `downloads/${id}`);
  }
  downloadAction(action: DownloadAction, ids: number[], deleteFiles = false) {
    return this.request<unknown>('POST', `downloads/${action}`, { ids, delete_files: deleteFiles });
  }
  retryFailed() {
    return this.request<unknown>('POST', 'downloads/retry-failed');
  }

  // --- packages
  listPackages() {
    return this.request<Package[]>('GET', 'packages');
  }
  getPackage(id: number) {
    return this.request<{ package: Package; downloads: Download[] }>('GET', `packages/${id}`);
  }
  packageAction(action: DownloadAction, ids: number[], deleteFiles = false) {
    return this.request<unknown>('POST', `packages/${action}`, { ids, delete_files: deleteFiles });
  }
  rescanPackage(id: number) {
    return this.request<unknown>('POST', `packages/${id}/rescan`);
  }
  pauseAll() {
    return this.request<unknown>('POST', 'pause-all');
  }
  resumeAll() {
    return this.request<unknown>('POST', 'resume-all');
  }
  stats() {
    return this.request<Stats>('GET', 'stats');
  }

  // --- linkgrabber
  addLinks(req: AddLinksRequest) {
    return this.request<GrabberEntry[]>('POST', 'links', req);
  }
  linkgrabber() {
    return this.request<GrabberEntry[]>('GET', 'linkgrabber');
  }
  confirm(req: ConfirmRequest) {
    return this.request<Package[]>('POST', 'linkgrabber/confirm', req);
  }
  removeGrabber(ids: number[]) {
    return this.request<unknown>('POST', 'linkgrabber/remove', { ids });
  }

  // --- accounts
  accounts() {
    return this.request<Account[]>('GET', 'accounts');
  }
  createAccount(req: CreateAccountRequest) {
    return this.request<Account>('POST', 'accounts', req);
  }
  updateAccount(id: string, patch: AccountPatch) {
    return this.request<Account>('PATCH', `accounts/${encodeURIComponent(id)}`, patch);
  }
  deleteAccount(id: string) {
    return this.request<unknown>('DELETE', `accounts/${encodeURIComponent(id)}`);
  }
  checkAccount(id: string) {
    return this.request<Account>('POST', `accounts/${encodeURIComponent(id)}/check`);
  }
  submit2FA(id: string, code: string) {
    return this.request<Account>('POST', `accounts/${encodeURIComponent(id)}/2fa`, { code });
  }
  importCookies(req: { hoster: string; label: string; cookies: string }) {
    return this.request<Account>('POST', 'accounts/import-cookies', req);
  }
  googleOAuthStart(accountId?: string) {
    return this.request<{ url: string }>('GET', 'oauth/google/start', undefined, { account_id: accountId });
  }
  /** Finishes Google OAuth from the final address the browser showed (remote daemons). */
  googleOAuthComplete(url: string) {
    return this.request<{ ok: boolean }>('POST', 'oauth/google/complete', { url });
  }

  // --- secret store (vault)
  secrets() {
    return this.request<SecretsStatus>('GET', 'secrets');
  }
  /** Opens the vault; the first unlock of a new vault chooses the master password. */
  unlockSecrets(password: string) {
    return this.request<SecretsStatus>('POST', 'secrets/unlock', { password });
  }
  lockSecrets() {
    return this.request<SecretsStatus>('POST', 'secrets/lock');
  }
  changeMasterPassword(oldPassword: string, newPassword: string) {
    return this.request<{ ok: boolean }>('POST', 'secrets/password', { old_password: oldPassword, new_password: newPassword });
  }

  /** The (normalized) links among `urls` already queued, downloaded or in the LinkGrabber. */
  knownLinks(urls: string[]) {
    return this.request<KnownLinksResponse>('POST', 'links/known', { urls });
  }

  // --- network
  egress() {
    return this.request<EgressStatus[]>('GET', 'egress');
  }
  importProxies(text: string) {
    return this.request<ImportResult>('POST', 'egress/import', { text });
  }
  addWireGuard(name: string, conf: string) {
    return this.request<unknown>('POST', 'egress/wireguard', { name, conf });
  }
  testEgress(id: string) {
    return this.request<EgressStatus>('POST', `egress/${encodeURIComponent(id)}/test`);
  }
  rotate(egressId = '') {
    return this.request<unknown>('POST', 'rotation/rotate', { egress_id: egressId });
  }
  publicIP() {
    return this.request<PublicIPResponse>('GET', 'ip');
  }

  // --- settings
  getSettings() {
    return this.request<Settings>('GET', 'settings');
  }
  putSettings(s: Settings) {
    return this.request<Settings>('PUT', 'settings', s);
  }

  // --- desktop app endpoints (same origin as the page, never baseURL; only
  // in Connection.mode === 'desktop'; see cmd/adesc-desktop/README.md)
  async desktop<T>(method: string, path: string, body?: unknown): Promise<T | null> {
    const origin = typeof location !== 'undefined' ? location.origin : '';
    const r = await this.fetchImpl(`${origin}/__adesc/${path}`, {
      method,
      credentials: 'omit',
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (r.status === 204) return null;
    const data = (await r.json().catch(() => null)) as T | ApiErrorBody | null;
    if (!r.ok) {
      const e = (data as ApiErrorBody | null)?.error;
      throw new ApiError(r.status, 'desktop', typeof e === 'string' ? e : (e?.message ?? `Error ${r.status}`));
    }
    return data as T;
  }
  desktopClipboard() {
    return this.desktop<ClipboardStatus>('GET', 'clipboard');
  }
  setDesktopClipboard(cfg: Record<string, unknown>) {
    return this.desktop<ClipboardStatus>('PUT', 'clipboard', cfg);
  }
  desktopAppearance(a: { estilo: string; tema: string; esquema: string }) {
    return this.desktop<unknown>('POST', 'appearance', a);
  }
  /** Native folder dialog; null when cancelled. */
  pickDownloadFolder() {
    return this.desktop<{ path: string }>('POST', 'dialog/download-folder');
  }
}
