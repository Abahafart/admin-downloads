// The single application store (Svelte 5 runes): data from REST snapshots
// + SSE events, connection state, UI state shared by every style
// (selection, filter, search, overlays) and every action. Styles only read
// from `app` and call its methods; they never talk to the API directly.
import { Api, ApiError, discoverConnection, type Connection, type FetchLike } from '../api/client';
import { EventStream } from '../api/sse';
import type {
  Account, AccountPatch, AddLinksRequest, ClipboardStatus, ConfirmRequest, CoreEvent, CreateAccountRequest, Download,
  DownloadAction, EgressStatus, GrabberEntry, Package, SecretsStatus, Settings,
} from '../api/types';
import { applyEvent, emptyData, replaceAll, type DataState, type Effects } from './reducer';
import { buildItems, countBuckets, dkey, filterItems, parseKey, pkey, type Item } from './rows';
import { statusOf, type Bucket } from './status';
import { egressName, hosterName } from './labels';
import { router, type ScreenId } from '../router.svelte';
import { setAppearance, type Appearance } from '../../theme/runtime.svelte';

export type ConnStatus = 'connecting' | 'login' | 'ready' | 'offline';

export interface Toast {
  id: number;
  level: 'info' | 'success' | 'warning' | 'error';
  message: string;
}

export interface UIState {
  search: string;
  filter: Bucket;
  /** Selected row key ("p:<id>" | "d:<id>"), the target of p/r/R/i. */
  selected: string | null;
  /** Folder packages expanded in the Descargas view, by package id. */
  expanded: Record<number, boolean>;
  commandOpen: boolean;
  helpOpen: boolean;
  /** Pending delete confirmation. */
  confirmDelete: { keys: string[] } | null;
}

/** Result of an action whose errors are shown inline by its form (vault, OAuth paste). */
export type ActionResult = { ok: true } | { ok: false; status: number; code: string; message: string; retryAfter?: number };

/** A Google OAuth consent opened from Cuentas, waiting for the redirect (or the pasted URL). */
export interface PendingOAuth {
  accountId: string;
  label: string;
  /** Google consent page of this flow (also offered as a link). */
  url: string;
  /** The browser blocked the pop-up: the user opens the link by hand. */
  blocked: boolean;
}

let toastSeq = 0;

export class AppStore {
  data = $state<DataState>(emptyData());
  settings = $state<Settings | null>(null);
  /** Last public IP per egress id from GET /ip (the egress list also has them). */
  ipByEgress = $state<Record<string, string>>({});
  /**
   * Clipboard monitor (desktop app only: GET/PUT /__adesc/clipboard; in the
   * mock it is emulated with settings.clipboard; in a browser it does not
   * exist and `available` is false).
   */
  clipboard = $state<{ available: boolean; enabled: boolean; action: string; backend: string; error: string }>({
    available: false, enabled: false, action: 'ask', backend: '', error: '',
  });
  private clipboardRaw: Record<string, unknown> = {};
  status = $state<ConnStatus>('connecting');
  /** SSE stream currently open. */
  live = $state(false);
  error = $state('');
  now = $state(Date.now());
  toasts = $state<Toast[]>([]);
  ui = $state<UIState>({
    search: '', filter: 'todas', selected: null, expanded: {}, commandOpen: false, helpOpen: false, confirmDelete: null,
  });
  connection = $state<Connection | null>(null);
  /** Google consent in progress (Cuentas shows the "paste the final URL" field). */
  oauth = $state<PendingOAuth | null>(null);

  api: Api | null = null;
  private stream: EventStream | null = null;
  private buffering: CoreEvent[] | null = null;
  private egressTimer: ReturnType<typeof setTimeout> | null = null;
  private resyncTimer: ReturnType<typeof setTimeout> | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private secretsTimer: ReturnType<typeof setTimeout> | null = null;
  private accountsTimer: ReturnType<typeof setTimeout> | null = null;

  // ---------- derived view data (shared by every style)

  /** Every top-level item (folder packages + loose downloads), sorted. */
  items = $derived.by(() => buildItems(this.data.downloads, this.data.packages));
  /** Counts per filter bucket over all items. */
  counts = $derived.by(() => countBuckets(this.items));
  /** Items after the current filter and search. */
  visible = $derived.by(() => filterItems(this.items, this.ui.filter, this.ui.search, hosterName));
  grabberEntries = $derived.by(() => Object.values(this.data.grabber).sort((a, b) => a.id - b.id));
  /** Accounts, oldest first (like GET /accounts), live through account.* events. */
  accounts = $derived.by(() =>
    Object.values(this.data.accounts).sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.id.localeCompare(b.id)),
  );
  /** Secret store status (null: unknown or the server has no secret store API). */
  secrets = $derived<SecretsStatus | null>(this.data.secrets);
  /** The vault waits for its master password: credentials cannot be read or saved. */
  vaultLocked = $derived(!!this.data.secrets?.locked);
  egressList = $derived.by(() => Object.values(this.data.egress));
  totalSpeed = $derived.by(() => {
    if (this.data.stats.speed) return this.data.stats.speed;
    let s = 0;
    for (const d of Object.values(this.data.downloads)) if (d.state === 'downloading') s += d.speed;
    return s;
  });
  activeCount = $derived.by(() => Object.values(this.data.downloads).filter((d) => d.state === 'downloading').length);
  /** The exit most active downloads use right now ("Directa" when idle). */
  currentEgress = $derived.by(() => {
    const n = new Map<string, number>();
    for (const d of Object.values(this.data.downloads)) {
      if (d.state === 'downloading' && d.egress_id) n.set(d.egress_id, (n.get(d.egress_id) ?? 0) + 1);
    }
    let best = '';
    let max = 0;
    for (const [id, c] of n) if (c > max) [best, max] = [id, c];
    return best;
  });
  currentEgressLabel = $derived.by(() => egressName(this.currentEgress || 'direct', this.data.egress) || 'Directa');
  clipboardEnabled = $derived.by(() => this.clipboard.available && this.clipboard.enabled);
  /** Public IP of the exit in use (or the direct one). */
  publicIP = $derived.by(() => {
    const id = this.currentEgress || 'direct';
    const direct = this.ipByEgress.direct || this.data.egress.direct?.public_ip;
    return this.ipByEgress[id] || this.data.egress[id]?.public_ip || direct || Object.values(this.ipByEgress).find(Boolean) || '';
  });
  isDesktop = $derived.by(() => this.connection?.mode === 'desktop');

  // ---------- lifecycle

  /**
   * Discovers the connection (desktop binding or same origin) and loads
   * everything. `fetchImpl` is window.fetch, or the mock in VITE_MOCK mode.
   */
  async start(fetchImpl: FetchLike, opts: { mock?: boolean } = {}): Promise<void> {
    const page = typeof location !== 'undefined' ? location.href : 'http://localhost/';
    this.connection = opts.mock ? { mode: 'mock', baseURL: new URL('./', page).href } : await discoverConnection(fetchImpl, page);
    this.api = new Api(this.connection, fetchImpl);
    this.api.onUnauthorized = () => this.needLogin();
    setInterval(() => (this.now = Date.now()), 1000);
    if (this.connection.mode === 'desktop') this.desktopHooks();
    await this.boot();
  }

  /** Desktop app: window background follows the theme; Wails navigation events. */
  private desktopHooks(): void {
    if (typeof document === 'undefined') return;
    document.documentElement.addEventListener('adesc-appearance', (e) => {
      const d = (e as CustomEvent<{ estilo: string; tema: string; esquema: string }>).detail;
      void this.api?.desktopAppearance({ estilo: d.estilo, tema: d.tema, esquema: d.esquema }).catch(() => {});
    });
    const rt = (window as unknown as { runtime?: { EventsOn?: (n: string, cb: (screen: string) => void) => void } }).runtime;
    rt?.EventsOn?.('adesc:navigate', (screen) => router.go(screen as ScreenId));
  }

  private needLogin(): void {
    this.stream?.stop();
    this.stream = null;
    this.live = false;
    this.status = 'login';
  }

  /**
   * Web mode: GET /auth/session recovers the CSRF token after a reload and
   * tells whether a password exists. Without one (loopback daemon), an empty
   * login opens the session. Returns false when the login screen is needed.
   */
  private async ensureSession(): Promise<boolean> {
    if (!this.api || this.connection?.mode !== 'web') return true;
    let s;
    try {
      s = await this.api.session();
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return true; // older API without sessions
      throw e;
    }
    if (s?.csrf) this.api.setCsrf(s.csrf);
    if (s?.authenticated) return true;
    if (s && !s.password_required) {
      await this.api.login('');
      return true;
    }
    return false;
  }

  private async boot(): Promise<void> {
    if (!this.api) return;
    this.status = 'connecting';
    try {
      if (!(await this.ensureSession())) {
        this.status = 'login';
        return;
      }
      await this.resync();
      await Promise.allSettled([this.loadSettings()]);
      await this.loadClipboard();
      // GET /ip probes every egress (can take seconds): never block on it.
      void this.loadIP();
      this.status = 'ready';
      this.error = '';
      this.openStream();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        this.status = 'login';
        return;
      }
      this.status = 'offline';
      this.error = e instanceof Error ? e.message : String(e);
      if (this.retryTimer) clearTimeout(this.retryTimer);
      this.retryTimer = setTimeout(() => void this.boot(), 3000);
    }
  }

  async login(password: string): Promise<void> {
    if (!this.api) return;
    await this.api.login(password);
    await this.boot();
  }

  private openStream(): void {
    if (!this.api || this.stream) return;
    this.stream = new EventStream(this.api, {
      onOpen: () => {
        this.live = true;
        void this.resync().catch(() => {});
      },
      onDown: () => {
        this.live = false;
      },
      onEvent: (ev) => {
        if (this.buffering) this.buffering.push(ev);
        else this.handle(ev);
      },
    });
    this.stream.start();
  }

  /** Applies one event (exposed for tests and the mock). */
  handle(ev: CoreEvent): void {
    this.effects(applyEvent(this.data, ev));
    this.oauthDone(ev);
  }

  /** The local OAuth callback reached the daemon: the pending consent is over. */
  private oauthDone(ev: CoreEvent): void {
    if (this.oauth && ev.type === 'account.updated' && ev.account?.id === this.oauth.accountId && ev.account.status === 'ok') {
      this.oauth = null;
      this.toast('Cuenta de Google conectada', 'success');
    }
  }

  private effects(fx: Effects): void {
    if (fx.resync && !this.resyncTimer) {
      this.resyncTimer = setTimeout(() => {
        this.resyncTimer = null;
        void this.resync().catch(() => {});
      }, 300);
    }
    if (fx.refetchEgress && !this.egressTimer) {
      this.egressTimer = setTimeout(() => {
        this.egressTimer = null;
        void this.loadEgress().catch(() => {});
      }, 250);
    }
    if (fx.refetchIP) void this.loadIP();
    if (fx.refetchSecrets && !this.secretsTimer) {
      this.secretsTimer = setTimeout(() => {
        this.secretsTimer = null;
        void this.loadSecrets();
      }, 150);
    }
    if (fx.refetchAccounts && !this.accountsTimer) {
      this.accountsTimer = setTimeout(() => {
        this.accountsTimer = null;
        void this.loadAccounts().catch(() => {});
      }, 300);
    }
    if (fx.notice) this.toast(fx.notice.message, fx.notice.level);
  }

  /** Reloads every snapshot; events arriving meanwhile are replayed after. */
  async resync(): Promise<void> {
    const api = this.api;
    if (!api) return;
    this.buffering = this.buffering ?? [];
    try {
      const [downloads, packages, grabber, stats, egress, accounts, secrets] = await Promise.all([
        api.listDownloads(),
        api.listPackages(),
        api.linkgrabber(),
        api.stats(),
        api.egress().catch(() => [] as EgressStatus[]),
        api.accounts().catch(() => null),
        this.fetchSecrets(api),
      ]);
      replaceAll(this.data.downloads, downloads);
      replaceAll(this.data.packages, packages);
      replaceAll(this.data.grabber, grabber);
      replaceAll(this.data.egress, egress);
      if (accounts) replaceAll(this.data.accounts, accounts);
      if (secrets !== undefined) this.data.secrets = secrets;
      if (stats) this.data.stats = stats;
    } finally {
      const buf = this.buffering ?? [];
      this.buffering = null;
      const seq = this.data.lastSeq;
      this.data.lastSeq = 0; // the snapshot is the new baseline
      for (const ev of buf) {
        const fx = applyEvent(this.data, ev);
        fx.resync = false;
        this.effects(fx);
      }
      if (!buf.length) this.data.lastSeq = seq;
    }
  }

  async loadAccounts(): Promise<void> {
    if (this.api) replaceAll(this.data.accounts, (await this.api.accounts()) ?? []);
  }
  /** GET /secrets: the status, null when the server has no secret store API, undefined on other errors (keep the last one). */
  private async fetchSecrets(api: Api): Promise<SecretsStatus | null | undefined> {
    try {
      return (await api.secrets()) ?? null;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 501 || e.status === 404)) return null;
      return undefined;
    }
  }
  async loadSecrets(): Promise<void> {
    if (!this.api) return;
    const s = await this.fetchSecrets(this.api);
    if (s !== undefined) this.data.secrets = s;
  }
  async loadEgress(): Promise<void> {
    if (this.api) replaceAll(this.data.egress, await this.api.egress());
  }
  async loadIP(): Promise<void> {
    if (!this.api) return;
    try {
      const r = await this.api.publicIP();
      const next: Record<string, string> = {};
      for (const e of r?.egresses ?? []) if (e.public_ip) next[e.id] = e.public_ip;
      if (!r?.egresses && (r?.ip || r?.public_ip)) next.direct = (r.ip ?? r.public_ip)!;
      this.ipByEgress = next;
    } catch {
      /* optional */
    }
  }
  async loadClipboard(): Promise<void> {
    const mode = this.connection?.mode;
    if (mode === 'desktop' && this.api) {
      try {
        const c = await this.api.desktopClipboard();
        if (c) this.applyClipboard(c);
      } catch (e) {
        this.clipboard = { available: false, enabled: false, action: 'ask', backend: '', error: e instanceof Error ? e.message : '' };
      }
    } else if (mode === 'mock') {
      const c = this.settings?.clipboard;
      this.clipboard = { available: true, enabled: !!c?.enabled, action: c?.action ?? 'ask', backend: 'demo', error: '' };
    } else {
      this.clipboard = { available: false, enabled: false, action: 'ask', backend: '', error: '' };
    }
  }
  private applyClipboard(c: ClipboardStatus): void {
    const { available, backend, error, ...cfg } = c;
    this.clipboardRaw = cfg;
    this.clipboard = {
      available: !!available,
      enabled: !!(c.enabled ?? c.Enabled),
      action: (c.action ?? c.Action ?? 'ask') as string,
      backend: backend ?? '',
      error: error ?? '',
    };
  }
  async loadSettings(): Promise<void> {
    if (!this.api) return;
    const s = await this.api.getSettings();
    this.settings = s ?? {};
    const a = s?.appearance;
    if (a && (a.estilo || a.tema || a.acento !== undefined)) setAppearance(a as Partial<Appearance>);
  }

  // ---------- toasts

  toast(message: string, level: Toast['level'] = 'info'): void {
    if (!message) return;
    const t: Toast = { id: ++toastSeq, level, message };
    this.toasts.push(t);
    setTimeout(() => this.dismiss(t.id), level === 'error' ? 8000 : 4500);
  }
  dismiss(id: number): void {
    const i = this.toasts.findIndex((t) => t.id === id);
    if (i >= 0) this.toasts.splice(i, 1);
  }

  /** Runs an action, turning errors into toasts. Returns false on error. */
  async run<T>(fn: (api: Api) => Promise<T>, ok?: string): Promise<T | undefined> {
    if (!this.api) return undefined;
    try {
      const r = await fn(this.api);
      if (ok) this.toast(ok, 'success');
      return r;
    } catch (e) {
      this.noteLocked(e);
      if (!(e instanceof ApiError && e.status === 401)) this.toast(e instanceof Error ? e.message : String(e), 'error');
      return undefined;
    }
  }

  /** 423 Locked: the vault was locked meanwhile; show it now and refresh the status. */
  private noteLocked(e: unknown): void {
    if (!(e instanceof ApiError && e.status === 423)) return;
    if (this.data.secrets) this.data.secrets.locked = true;
    void this.loadSecrets();
  }

  /** Like run() but returns the error for the form to show it inline (no toast). */
  async attempt(fn: (api: Api) => Promise<unknown>): Promise<ActionResult> {
    if (!this.api) return { ok: false, status: 0, code: 'network', message: 'No hay conexión con el servicio de descargas' };
    try {
      await fn(this.api);
      return { ok: true };
    } catch (e) {
      this.noteLocked(e);
      if (e instanceof ApiError) return { ok: false, status: e.status, code: e.code, message: e.message, retryAfter: e.retryAfter };
      return { ok: false, status: 0, code: 'error', message: e instanceof Error ? e.message : String(e) };
    }
  }

  // ---------- lookups

  download(id: number): Download | undefined {
    return this.data.downloads[id];
  }
  pkg(id: number): Package | undefined {
    return this.data.packages[id];
  }
  childrenOf(packageId: number): Download[] {
    return Object.values(this.data.downloads)
      .filter((d) => d.package_id === packageId)
      .sort((a, b) => (a.rel_path || a.name).localeCompare(b.rel_path || b.name, 'es', { numeric: true }));
  }
  egressLabel(id: string | undefined): string {
    if (!id) return '';
    const name = egressName(id, this.data.egress);
    return this.data.rotating[id] || this.data.rotating[''] ? `${name} → rotando` : name;
  }

  // ---------- selection (keyboard row selection)

  select(key: string | null): void {
    this.ui.selected = key;
  }
  isSelected(key: string): boolean {
    return this.ui.selected === key;
  }
  toggleExpanded(packageId: number, open?: boolean): void {
    this.ui.expanded[packageId] = open ?? !this.ui.expanded[packageId];
  }
  isExpanded(packageId: number): boolean {
    return !!this.ui.expanded[packageId] || (!!this.ui.search.trim() && !!this.data.packages[packageId]);
  }
  setFilter(b: Bucket): void {
    this.ui.filter = b;
  }

  // ---------- download / package actions

  private split(keys: string[]): { d: number[]; p: number[] } {
    const out = { d: [] as number[], p: [] as number[] };
    for (const k of keys) {
      const r = parseKey(k);
      if (r?.kind === 'download') out.d.push(r.id);
      else if (r?.kind === 'package') out.p.push(r.id);
    }
    return out;
  }

  async act(action: DownloadAction, keys: string[], deleteFiles = false): Promise<void> {
    const { d, p } = this.split(keys);
    await this.run(async (api) => {
      if (d.length) await api.downloadAction(action, d, deleteFiles);
      if (p.length) await api.packageAction(action, p, deleteFiles);
    });
  }
  pause(...keys: string[]) {
    return this.act('pause', keys);
  }
  resume(...keys: string[]) {
    return this.act('resume', keys);
  }
  restart(...keys: string[]) {
    return this.act('restart', keys);
  }
  retry(...keys: string[]) {
    return this.act('retry', keys);
  }
  /** Asks for confirmation (shared dialog) before deleting. */
  askDelete(...keys: string[]): void {
    if (keys.length) this.ui.confirmDelete = { keys };
  }
  async remove(keys: string[], deleteFiles: boolean): Promise<void> {
    this.ui.confirmDelete = null;
    await this.act('delete', keys, deleteFiles);
    if (this.ui.selected && keys.includes(this.ui.selected)) this.ui.selected = null;
  }
  retryFailed() {
    return this.run((api) => api.retryFailed(), 'Reintentando las descargas fallidas');
  }
  pauseAll() {
    return this.run((api) => api.pauseAll());
  }
  resumeAll() {
    return this.run((api) => api.resumeAll());
  }
  togglePauseAll() {
    return this.data.stats.paused ? this.resumeAll() : this.pauseAll();
  }
  rescan(packageId: number) {
    return this.run((api) => api.rescanPackage(packageId), 'Buscando archivos nuevos en la carpeta');
  }

  /** State of the item behind a row key. */
  stateOf(key: string): Download['state'] | undefined {
    const r = parseKey(key);
    if (!r) return undefined;
    return r.kind === 'download' ? this.data.downloads[r.id]?.state : this.data.packages[r.id]?.state;
  }

  /** p: pause or resume the selected item. */
  async togglePause(key = this.ui.selected): Promise<void> {
    if (!key) return this.toast('Selecciona una descarga primero', 'info');
    const st = this.stateOf(key);
    if (st === 'paused') await this.resume(key);
    else if (st === 'completed') this.toast('La descarga ya está completada');
    else if (st === 'failed') await this.retry(key);
    else if (st) await this.pause(key);
  }

  /** i: rotate the selected download's egress, or a global rotation. */
  async rotate(egressId?: string): Promise<void> {
    let id = egressId;
    if (id === undefined) {
      const r = parseKey(this.ui.selected);
      if (r?.kind === 'download') id = this.data.downloads[r.id]?.egress_id;
      if (id === 'direct') id = '';
    }
    await this.run((api) => api.rotate(id ?? ''), id ? `Rotando ${egressName(id, this.data.egress)}` : 'Rotando la IP');
  }

  // ---------- linkgrabber

  async addLinks(text: string, extra: Partial<AddLinksRequest> = {}): Promise<GrabberEntry[] | undefined> {
    const t = text.trim();
    if (!t) return undefined;
    const r = await this.run((api) => api.addLinks({ text: t, source: this.connection?.mode === 'desktop' ? 'desktop' : 'api', ...extra }));
    if (r) {
      for (const e of r) this.data.grabber[e.id] = e;
      if (r.length === 0) this.toast('No se encontraron enlaces en el texto', 'warning');
      else {
        this.toast(r.length === 1 ? 'Enlace añadido al LinkGrabber' : `${r.length} enlaces añadidos al LinkGrabber`, 'success');
        if (!extra.auto_confirm) router.go('linkgrabber');
      }
    }
    return r;
  }
  async confirmGrabber(req: ConfirmRequest): Promise<boolean> {
    const r = await this.run((api) => api.confirm(req));
    if (!r) return false;
    for (const id of req.items.map((i) => i.entry_id)) delete this.data.grabber[id];
    this.toast(r.length === 1 ? `Paquete «${r[0].name}» en la cola` : `${r.length} paquetes en la cola`, 'success');
    for (const p of r) {
      this.data.packages[p.id] = p;
      if (p.is_folder) this.ui.expanded[p.id] = true;
    }
    return true;
  }
  async removeGrabber(ids: number[]): Promise<void> {
    const r = await this.run((api) => api.removeGrabber(ids));
    if (r !== undefined) for (const id of ids) delete this.data.grabber[id];
  }

  // ---------- accounts

  private upsertAccount(a: Account | undefined): void {
    if (a?.id) this.data.accounts[a.id] = a;
  }
  async createAccount(req: CreateAccountRequest): Promise<Account | undefined> {
    const a = await this.run((api) => api.createAccount(req), 'Cuenta añadida');
    this.upsertAccount(a);
    return a;
  }
  async updateAccount(id: string, patch: AccountPatch): Promise<void> {
    this.upsertAccount(await this.run((api) => api.updateAccount(id, patch)));
  }
  async deleteAccount(id: string): Promise<void> {
    const r = await this.run((api) => api.deleteAccount(id), 'Cuenta eliminada');
    if (r !== undefined) delete this.data.accounts[id];
  }
  async checkAccount(id: string): Promise<void> {
    const acc = this.data.accounts[id];
    if (acc) acc.status = 'checking';
    const a = await this.run((api) => api.checkAccount(id));
    if (a) this.upsertAccount(a);
    else await this.loadAccounts().catch(() => {});
  }
  async submit2FA(id: string, code: string): Promise<void> {
    this.upsertAccount(await this.run((api) => api.submit2FA(id, code)));
  }
  async importCookies(hoster: string, label: string, cookies: string): Promise<Account | undefined> {
    const a = await this.run((api) => api.importCookies({ hoster, label, cookies }), 'Cookies importadas');
    this.upsertAccount(a);
    return a;
  }
  /**
   * Starts Google OAuth: opens the consent page in a new window. Cuentas then
   * offers to paste the final address (daemons the browser cannot reach).
   */
  async googleOAuth(accountId: string): Promise<void> {
    const r = await this.run((api) => api.googleOAuthStart(accountId));
    if (!r?.url) return;
    // No 'noopener' here: with it window.open() always returns null and a
    // blocked pop-up could not be told apart. The opener is cleared below.
    const w = typeof window !== 'undefined' ? window.open(r.url, '_blank') : null;
    if (w) {
      try {
        w.opener = null;
      } catch {
        /* cross-origin already */
      }
    }
    // Never navigate this page away: Cuentas keeps the link and the field
    // for the final address.
    this.oauth = { accountId, label: this.data.accounts[accountId]?.label ?? 'Google', url: r.url, blocked: !w };
    this.toast(w ? 'Da permiso en la ventana de Google y vuelve aquí' : 'El navegador bloqueó la ventana de Google: ábrela con el enlace de Cuentas', w ? 'info' : 'warning');
  }
  /** Remote daemon: finishes OAuth with the address the browser showed after authorizing. */
  async completeGoogleOAuth(url: string): Promise<ActionResult> {
    const res = await this.attempt((api) => api.googleOAuthComplete(url.trim()));
    if (res.ok) {
      this.oauth = null;
      this.toast('Cuenta de Google conectada', 'success');
      await this.loadAccounts().catch(() => {});
    }
    return res;
  }
  cancelOAuth(): void {
    this.oauth = null;
  }

  // ---------- secret store (vault)

  /** Opens the vault (the first unlock of a new vault sets the master password). */
  async unlockSecrets(password: string): Promise<ActionResult> {
    const res = await this.attempt(async (api) => {
      const st = await api.unlockSecrets(password);
      if (st && typeof st === 'object') this.data.secrets = st;
    });
    if (res.ok) {
      this.toast('Credenciales desbloqueadas', 'success');
      await this.loadAccounts().catch(() => {});
    }
    return res;
  }
  async lockSecrets(): Promise<void> {
    const st = await this.run((api) => api.lockSecrets(), 'Credenciales bloqueadas');
    if (st && typeof st === 'object') this.data.secrets = st;
    else await this.loadSecrets();
  }
  async changeMasterPassword(oldPassword: string, newPassword: string): Promise<ActionResult> {
    const res = await this.attempt((api) => api.changeMasterPassword(oldPassword, newPassword));
    if (res.ok) {
      this.toast('Contraseña maestra cambiada', 'success');
      await this.loadSecrets();
    }
    return res;
  }

  /** Links among `urls` the daemon already has (queue, history or LinkGrabber); [] on error. */
  async knownLinks(urls: string[]): Promise<string[]> {
    if (!this.api || !urls.length) return [];
    try {
      return (await this.api.knownLinks(urls))?.known ?? [];
    } catch {
      return [];
    }
  }

  // ---------- network

  async importProxies(text: string): Promise<boolean> {
    const r = await this.run((api) => api.importProxies(text));
    if (r === undefined) return false;
    const added = r?.added ?? 0;
    const parts = [added === 1 ? '1 proxy añadido' : `${added} proxies añadidos`];
    if (r?.skipped) parts.push(`${r.skipped} omitidos`);
    this.toast(parts.join(', '), added ? 'success' : 'warning');
    for (const e of (r?.errors ?? []).slice(0, 3)) this.toast(e.line ? `Línea ${e.line}: ${e.message}` : e.message, 'warning');
    await this.loadEgress().catch(() => {});
    return added > 0;
  }
  async addWireGuard(name: string, conf: string): Promise<boolean> {
    const r = await this.run((api) => api.addWireGuard(name, conf), 'Túnel WireGuard añadido');
    if (r !== undefined) await this.loadEgress().catch(() => {});
    return r !== undefined;
  }
  async testEgress(id: string): Promise<void> {
    const e = await this.run((api) => api.testEgress(id));
    if (e?.id) this.data.egress[e.id] = e;
    else await this.loadEgress().catch(() => {});
  }

  // ---------- settings

  async saveSettings(patch: Settings, ok = 'Ajustes guardados'): Promise<boolean> {
    const base = ($state.snapshot(this.settings) ?? {}) as Record<string, unknown>;
    const next = mergeDeep(base, patch as Record<string, unknown>) as Settings;
    const r = await this.run((api) => api.putSettings(next), ok || undefined);
    if (r === undefined) return false;
    this.settings = r && typeof r === 'object' && Object.keys(r).length ? r : next;
    return true;
  }
  /** Applies instantly (and mirrors to localStorage), then saves on the server. */
  async setAppearance(a: Partial<Appearance>): Promise<void> {
    const next = setAppearance(a);
    if (this.settings) await this.saveSettings({ appearance: { ...next } }, '');
  }
  /** Turns the clipboard monitor on/off and/or changes its action. */
  async setClipboard(enabled: boolean, action?: string): Promise<void> {
    const msg = enabled ? 'Vigilando el portapapeles' : 'Portapapeles en pausa';
    const act = action ?? this.clipboard.action;
    if (this.connection?.mode === 'desktop') {
      const c = await this.run((api) => api.setDesktopClipboard({ ...this.clipboardRaw, Enabled: enabled, Action: act, enabled, action: act }), msg);
      if (c) this.applyClipboard(c);
    } else if (this.connection?.mode === 'mock') {
      if (await this.saveSettings({ clipboard: { ...(this.settings?.clipboard ?? {}), enabled, action: act as 'ask' } }, msg)) await this.loadClipboard();
    } else {
      this.toast('El monitor del portapapeles solo existe en la app de escritorio', 'info');
    }
  }
  /** Desktop: native folder dialog. Returns the path or null (cancelled / not desktop). */
  async pickFolder(): Promise<string | null> {
    if (!this.isDesktop) return null;
    const r = await this.run((api) => api.pickDownloadFolder());
    return r?.path ?? null;
  }

  // ---------- helpers for styles

  /** The item behind the selected row (for detail panels). */
  get selectedItem(): Item | undefined {
    const r = parseKey(this.ui.selected);
    if (!r) return undefined;
    if (r.kind === 'package') {
      const pkg = this.data.packages[r.id];
      return pkg ? { kind: 'package', key: pkey(r.id), pkg, children: this.childrenOf(r.id) } : undefined;
    }
    const dl = this.data.downloads[r.id];
    return dl ? { kind: 'download', key: dkey(r.id), dl, parent: this.data.packages[dl.package_id] } : undefined;
  }

  statusOf(d: Download) {
    return statusOf(d, this.now);
  }

  go(screen: ScreenId, params?: Record<string, string>): void {
    router.go(screen, params);
  }
}

function isObj(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v);
}

export function mergeDeep(target: Record<string, unknown>, patch: Record<string, unknown>): Record<string, unknown> {
  for (const [k, v] of Object.entries(patch)) {
    if (isObj(v) && isObj(target[k])) mergeDeep(target[k] as Record<string, unknown>, v);
    else target[k] = v;
  }
  return target;
}

export const app = new AppStore();
