// Wire types mirroring the Go DTOs (json tags) in internal/core/types.go,
// internal/core/events.go, internal/accounts/accounts.go and
// internal/rotation/rotation.go. Times are RFC 3339 strings; "omitzero"
// fields may be absent.

/** hosters.QuotaScope string ("" / absent = not a quota). */
export type QuotaScopeWire = 'ip' | 'account' | 'file' | '';

export type State =
  | 'queued' | 'resolving' | 'waiting' | 'downloading' | 'verifying'
  | 'postprocessing' | 'completed' | 'failed' | 'paused';

export const ACTIVE_STATES: readonly State[] = ['resolving', 'waiting', 'downloading', 'verifying', 'postprocessing'];

export interface Checksum {
  algo?: string;
  value?: string;
  [k: string]: unknown;
}

export interface Download {
  id: number;
  package_id: number;
  hoster: string;
  url: string;
  file_id?: string;
  name: string;
  rel_path?: string;
  dest_path: string;
  state: State;
  size: number; // -1 unknown
  done: number;
  speed: number;
  eta: number; // seconds, -1 unknown
  connections: number;
  resumable?: boolean;
  checksum?: Checksum;
  error?: string;
  /** hosters.Kind string: quota, needs_auth, not_found, expired, temporary, fatal. */
  error_kind?: string;
  /** Quota scope while waiting on / failed by a quota (core.Download.QuotaScope). */
  quota_scope?: QuotaScopeWire;
  attempts: number;
  egress_id?: string;
  public_ip?: string;
  account_id?: string;
  waiting_until?: string;
  priority: number;
  created_at: string;
  started_at?: string;
  completed_at?: string;
}

export interface Package {
  id: number;
  name: string;
  dest_dir: string;
  source_url?: string;
  hoster?: string;
  is_folder: boolean;
  state: State;
  size: number;
  done: number;
  speed: number;
  eta: number;
  files_total: number;
  files_done: number;
  files_failed: number;
  priority: number;
  exists_policy: string;
  created_at: string;
}

export type GrabberState = 'pending' | 'resolving' | 'ready' | 'error' | 'unsupported';

export interface GrabberFile {
  index: number;
  name: string;
  rel_path?: string;
  size: number;
  checksum?: Checksum;
  known?: boolean;
}

export interface GrabberEntry {
  id: number;
  url: string;
  hoster?: string;
  state: GrabberState;
  error?: string;
  package_name: string;
  is_folder: boolean;
  files: GrabberFile[] | null;
  total_size: number;
  source?: string;
  added_at: string;
}

export interface AddLinksRequest {
  text?: string;
  urls?: string[];
  source?: string;
  auto_confirm?: boolean;
  dest_dir?: string;
  package_name?: string;
}

export interface ConfirmItem {
  entry_id: number;
  package_name?: string;
  include?: string[];
  exclude?: string[];
  file_indexes?: number[];
}

export type ExistsPolicy = 'skip' | 'overwrite' | 'rename';

export interface ConfirmRequest {
  items: ConfirmItem[];
  dest_dir?: string;
  exists_policy?: ExistsPolicy | '';
  paused?: boolean;
  priority?: number;
}

export interface Stats {
  speed: number;
  active: number;
  queued: number;
  waiting: number;
  failed: number;
  completed: number;
  grabber_size: number;
  paused: boolean;
}

export type EventType =
  | 'download.added' | 'download.updated' | 'download.removed' | 'progress'
  | 'package.added' | 'package.updated' | 'package.removed'
  | 'linkgrabber.added' | 'linkgrabber.updated' | 'linkgrabber.removed'
  | 'egress.updated' | 'rotation' | 'stats' | 'notice'
  /** account.updated carries `account` (never secrets); account.removed carries `account_id`. */
  | 'account.updated' | 'account.removed'
  /** The secret store was locked or unlocked (`message`: "locked" | "unlocked"): refetch GET /secrets. */
  | 'secrets.updated'
  /** Sent by the API when it cannot replay after Last-Event-ID: refetch everything. */
  | 'resync';

export interface Progress {
  id: number;
  package_id: number;
  state: State;
  done: number;
  size: number;
  speed: number;
  eta: number;
  connections: number;
  egress_id?: string;
  public_ip?: string;
  waiting_until?: string;
  /** Set while waiting on a quota. */
  quota_scope?: QuotaScopeWire;
}

export type NoticeLevel = 'info' | 'success' | 'warning' | 'error';

export interface CoreEvent {
  seq: number;
  type: EventType;
  time: string;
  id?: number;
  download?: Download;
  package?: Package;
  linkgrabber?: GrabberEntry;
  progress?: Progress[];
  stats?: Stats;
  /** account.* events. */
  account?: Account;
  account_id?: string;
  egress_id?: string;
  level?: NoticeLevel;
  message?: string;
}

// accounts.Account
export type AccountKind = 'password' | 'cookie' | 'oauth' | 'apikey';
export type AccountStatus =
  | 'unknown' | 'ok' | 'invalid' | 'expired' | 'quota' | 'needs_2fa' | 'error' | 'disabled' | 'checking';

export interface Account {
  id: string;
  hoster: string;
  label: string;
  kind: AccountKind;
  username: string;
  enabled: boolean;
  allow_rotation: boolean;
  status: AccountStatus;
  status_msg?: string;
  premium: boolean;
  premium_until?: string;
  quota_total: number; // -1 unknown
  quota_used: number;
  checked_at?: string;
  created_at: string;
  updated_at: string;
}

/**
 * POST /api/v1/accounts body. Assumed shape (the API agent owns it): the
 * metadata plus the one secret relevant to `kind`.
 */
export interface CreateAccountRequest {
  hoster: string;
  label: string;
  kind: AccountKind;
  username?: string;
  password?: string;
  api_key?: string;
  cookies?: string;
  allow_rotation?: boolean;
  enabled?: boolean;
}

export type AccountPatch = Partial<Pick<Account, 'label' | 'username' | 'enabled' | 'allow_rotation'>> & {
  password?: string;
  api_key?: string;
};

/** GET /api/v1/secrets (accounts.StoreStatus). */
export interface SecretsStatus {
  /** "keyring" (OS keyring), "vault" (encrypted file with a master password) or "memory". */
  backend: 'keyring' | 'vault' | 'memory' | string;
  /** True for the vault (it has a master password). */
  lockable: boolean;
  /** False while a new vault has no master password yet: the first unlock chooses it. */
  initialized: boolean;
  locked: boolean;
  /** Spanish explanation of a degraded choice (e.g. keyring no longer available). */
  warning?: string;
}

/** accounts.MinMasterPasswordLen. */
export const MIN_MASTER_PASSWORD = 8;

/** POST /api/v1/links/known result. */
export interface KnownLinksResponse {
  known: string[];
}

// rotation.EgressStatus
export type EgressKind = 'direct' | 'http' | 'socks5' | 'tor' | 'wireguard' | 'router' | 'systemvpn';
export type EgressScope = 'per-connection' | 'global';

export interface EgressStatus {
  id: string;
  kind: EgressKind;
  scope: EgressScope;
  label?: string;
  healthy: boolean;
  public_ip?: string;
  in_use: number;
  last_error?: string;
  checked_at?: string;
  /** hoster -> RFC 3339 time until which the egress is over quota. */
  exhausted?: Record<string, string>;
}

/** One entry of GET /api/v1/ip (api.EgressIP). */
export interface EgressIP {
  id: string;
  kind: EgressKind;
  public_ip: string;
  error?: string;
}

/** GET /api/v1/ip (api.IPResponse); `ip` tolerated for older servers. */
export interface PublicIPResponse {
  egresses?: EgressIP[];
  ip?: string;
  public_ip?: string;
}

/** POST /api/v1/egress/import result (api.ImportResult). */
export interface ImportResult {
  added: number;
  skipped: number;
  errors?: { line: number; message: string }[];
  ids?: string[];
}

/** GET /api/v1/auth/session (api.SessionInfo), public. */
export interface SessionInfo {
  authenticated: boolean;
  method?: 'token' | 'cookie' | '';
  csrf?: string;
  expires_at?: string;
  /** false: no Web UI password (loopback); POST /auth/login with "" opens a session. */
  password_required: boolean;
}

/**
 * GET|PUT /__adesc/clipboard (desktop app only). The desktop embeds
 * config.ClipboardConfig without json tags, so its keys may arrive
 * capitalized (Enabled, Action...): read both.
 */
export interface ClipboardStatus {
  available: boolean;
  backend?: string;
  error?: string;
  enabled?: boolean;
  action?: 'ask' | 'add' | 'linkgrabber';
  Enabled?: boolean;
  Action?: 'ask' | 'add' | 'linkgrabber';
  [k: string]: unknown;
}

export interface RouterInfo {
  enabled?: boolean;
  method?: string;
  script?: string[];
}

export interface SystemVPNInfo {
  name?: string;
  connect?: string[];
  rotate?: string[];
  disconnect?: string[];
}

/**
 * GET|PUT /api/v1/settings (api.Settings). PUT replaces the editable part
 * (send the whole object back); `server` and `network` are read-only (router
 * and VPN commands are only editable in config.toml). Fields are optional so
 * the UI tolerates partial responses.
 */
export interface Settings {
  download_dir?: string;
  downloads?: {
    max_active?: number;
    max_per_hoster?: number;
    max_per_egress?: number;
    conns_per_file?: number;
    speed_limit?: number;
    per_download_speed_limit?: number;
    max_retries?: number;
    resume_on_start?: boolean;
    exists_policy?: ExistsPolicy | string;
    [k: string]: unknown;
  };
  rotation?: { enabled?: boolean; strategy?: string; global_rotation?: boolean; [k: string]: unknown };
  clipboard?: { enabled?: boolean; action?: 'ask' | 'add' | 'linkgrabber'; [k: string]: unknown };
  postprocess?: { verify_checksums?: boolean; extract?: boolean; passwords?: string[]; delete_archives?: boolean };
  desktop?: { close_to_tray?: boolean; autostart?: boolean; notifications?: boolean; start_hidden?: boolean };
  appearance?: { estilo?: string; tema?: string; acento?: string };
  /** Read-only. */
  server?: { listen?: string; password_set?: boolean; tls?: boolean; allowed_origins?: string[]; allowed_hosts?: string[] };
  /** Read-only summary of the egress configuration. */
  network?: {
    direct?: boolean;
    tor_enabled?: boolean;
    proxies?: number;
    wireguard?: string[];
    router?: RouterInfo;
    system_vpn?: SystemVPNInfo[];
    health_interval?: string;
  };
  restart_required?: boolean;
  [k: string]: unknown;
}

export interface ApiErrorBody {
  error?: { code?: string; message?: string } | string;
}

export type DownloadAction = 'pause' | 'resume' | 'restart' | 'retry' | 'delete';
