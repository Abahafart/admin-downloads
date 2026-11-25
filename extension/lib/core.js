// Pure logic of the AdminDescargas extension. No browser APIs here: this
// module is imported by the background worker, the options page, the popup
// and by the Node tests (node --test extension/test).

export const DEFAULT_DAEMON_URL = 'http://127.0.0.1:7979';
export const API_PREFIX = '/api/v1';
export const SOURCE = 'extension';
export const MENU_TITLE = 'Descargar con AdminDescargas';

export const DEFAULT_EXTENSIONS = [
  'zip', 'rar', '7z', 'tar', 'gz', 'xz', 'bz2', 'zst', 'iso', 'img', 'dmg',
  'exe', 'msi', 'apk', 'deb', 'rpm', 'appimage',
  'mkv', 'mp4', 'avi', 'mov', 'webm', 'm4v', 'flac', 'mp3', 'm4a',
  'pdf', 'epub', 'cbz', 'cbr', '001',
];

export const DEFAULT_SETTINGS = Object.freeze({
  daemonUrl: DEFAULT_DAEMON_URL,
  token: '',
  autoConfirm: false,
  capture: Object.freeze({
    enabled: false,
    extensions: DEFAULT_EXTENSIONS,
    minSizeMB: 1,
    includeHosts: [],
    excludeHosts: [],
    skipIncognito: true,
  }),
});

// Supported hosters for cookie export. `id` is the Go plugin name
// (hosters.Hoster.Name()) sent as "hoster" to /accounts/import-cookies.
export const HOSTERS = Object.freeze([
  { id: 'mega', label: 'MEGA', domains: ['mega.nz', 'mega.io', 'mega.co.nz'] },
  {
    id: 'terabox',
    label: 'TeraBox',
    domains: [
      'terabox.com', '1024terabox.com', 'teraboxapp.com', 'freeterabox.com',
      '4funbox.com', 'mirrobox.com', 'nephobox.com', 'terabox.app',
      '1024tera.com', 'teraboxlink.com', 'terasharelink.com', 'momerybox.com',
      'tibibox.com',
    ],
  },
  { id: 'mediafire', label: 'MediaFire', domains: ['mediafire.com'] },
  { id: 'gdrive', label: 'Google Drive', domains: ['google.com', 'googleusercontent.com'], sensitive: true },
  { id: 'pixeldrain', label: 'Pixeldrain', domains: ['pixeldrain.com'] },
  { id: 'gofile', label: 'Gofile', domains: ['gofile.io'] },
  { id: 'onefichier', label: '1fichier', domains: ['1fichier.com'] },
  { id: 'dropbox', label: 'Dropbox', domains: ['dropbox.com'] },
  { id: 'onedrive', label: 'OneDrive', domains: ['onedrive.live.com', 'live.com', '1drv.ms'], sensitive: true },
  { id: 'yandex', label: 'Yandex Disk', domains: ['disk.yandex.com', 'disk.yandex.ru', 'yandex.com', 'yandex.ru'], sensitive: true },
]);

export function hosterById(id) {
  return HOSTERS.find((h) => h.id === id) || null;
}

// Match patterns for a hoster ("*.d" also matches d itself).
export function hosterOrigins(hoster) {
  return hoster.domains.map((d) => `*://*.${d}/*`);
}

// ---------------------------------------------------------------- URLs

// normalizeDaemonUrl validates and canonicalizes the daemon URL typed by the
// user: http(s) only, no credentials/query/fragment, no trailing slash and no
// trailing /api/v1. Throws Error with a Spanish message on invalid input.
export function normalizeDaemonUrl(raw) {
  let s = String(raw ?? '').trim();
  if (s === '') return DEFAULT_DAEMON_URL;
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) s = 'http://' + s;
  let u;
  try {
    u = new URL(s);
  } catch {
    throw new Error('La URL del daemon no es válida');
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    throw new Error('La URL del daemon debe empezar por http:// o https://');
  }
  if (u.username || u.password) {
    throw new Error('La URL del daemon no puede llevar usuario ni contraseña');
  }
  let path = u.pathname.replace(/\/+$/, '');
  if (path.toLowerCase().endsWith(API_PREFIX)) path = path.slice(0, -API_PREFIX.length);
  path = path.replace(/\/+$/, '');
  return u.origin + path;
}

export function apiUrl(daemonUrl, path) {
  const p = path.startsWith('/') ? path : '/' + path;
  return normalizeDaemonUrl(daemonUrl) + API_PREFIX + p;
}

// originPattern returns the host permission pattern for the daemon (match
// patterns ignore the port).
export function originPattern(daemonUrl) {
  const u = new URL(normalizeDaemonUrl(daemonUrl));
  return `${u.protocol}//${u.hostname}/*`;
}

export function isLocalDaemon(daemonUrl) {
  const h = new URL(normalizeDaemonUrl(daemonUrl)).hostname;
  return h === '127.0.0.1' || h === 'localhost' || h === '[::1]';
}

export function isHttpUrl(s) {
  try {
    const u = new URL(s);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch {
    return false;
  }
}

export function authHeaders(token) {
  const h = { Accept: 'application/json' };
  if (token) h.Authorization = `Bearer ${token}`;
  return h;
}

// buildLinksPayload turns a context-menu click into a POST /links body.
// Priority: link > media src > selection > page. Returns null if nothing
// usable was clicked.
export function buildLinksPayload(info, opts = {}) {
  const body = (extra) => {
    const b = { ...extra, source: SOURCE };
    if (opts.autoConfirm) b.auto_confirm = true;
    return b;
  };
  if (info.linkUrl && isHttpUrl(info.linkUrl)) return body({ urls: [info.linkUrl] });
  if (info.srcUrl && isHttpUrl(info.srcUrl)) return body({ urls: [info.srcUrl] });
  const sel = String(info.selectionText ?? '').trim();
  if (sel) return body({ text: sel });
  if (info.pageUrl && isHttpUrl(info.pageUrl)) return body({ urls: [info.pageUrl] });
  return null;
}

// ---------------------------------------------------------------- rules

// parseList splits user input on commas, whitespace and new lines.
export function parseList(text) {
  if (Array.isArray(text)) return text.map((s) => String(s).trim()).filter(Boolean);
  return String(text ?? '')
    .split(/[\s,;]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function parseExtensions(text) {
  const seen = new Set();
  for (let e of parseList(text)) {
    e = e.toLowerCase().replace(/^\*?\.+/, '');
    if (e) seen.add(e);
  }
  return [...seen];
}

function isRegexRule(rule) {
  return rule.length > 2 && rule.startsWith('/') && rule.endsWith('/');
}

// invalidHostRules returns the rules that are malformed regular expressions.
export function invalidHostRules(rules) {
  return parseList(rules).filter((r) => {
    if (!isRegexRule(r)) return false;
    try {
      new RegExp(r.slice(1, -1), 'i');
      return false;
    } catch {
      return true;
    }
  });
}

// hostMatches: "example.com" matches example.com and its subdomains
// ("*.example.com" and ".example.com" are accepted too); "/regex/" is tested
// case-insensitively against the host name.
export function hostMatches(host, rule) {
  host = String(host).toLowerCase();
  rule = String(rule).trim();
  if (!rule) return false;
  if (isRegexRule(rule)) {
    try {
      return new RegExp(rule.slice(1, -1), 'i').test(host);
    } catch {
      return false;
    }
  }
  const r = rule.toLowerCase().replace(/^\*?\.+/, '');
  return host === r || host.endsWith('.' + r);
}

export function hostMatchesAny(host, rules) {
  return parseList(rules).some((r) => hostMatches(host, r));
}

// fileExtension returns the lowercase extension of a file name or path
// (both separators), or "".
export function fileExtension(name) {
  const base = String(name ?? '').split(/[\\/]/).pop() || '';
  const i = base.lastIndexOf('.');
  if (i <= 0 || i === base.length - 1) return '';
  return base.slice(i + 1).toLowerCase();
}

export function extensionFromUrl(url) {
  try {
    const u = new URL(url);
    let p = u.pathname;
    try {
      p = decodeURIComponent(p);
    } catch {
      /* keep raw */
    }
    return fileExtension(p);
  } catch {
    return '';
  }
}

const MIME_EXT = {
  'application/zip': 'zip',
  'application/x-zip-compressed': 'zip',
  'application/vnd.rar': 'rar',
  'application/x-rar-compressed': 'rar',
  'application/x-7z-compressed': '7z',
  'application/x-iso9660-image': 'iso',
  'application/x-msdownload': 'exe',
  'application/vnd.android.package-archive': 'apk',
  'application/pdf': 'pdf',
  'video/mp4': 'mp4',
  'video/x-matroska': 'mkv',
  'video/webm': 'webm',
  'audio/mpeg': 'mp3',
};

function itemExtension(item) {
  return (
    fileExtension(item.filename) ||
    extensionFromUrl(item.finalUrl || item.url) ||
    MIME_EXT[String(item.mime ?? '').toLowerCase().split(';')[0].trim()] ||
    ''
  );
}

function itemSize(item) {
  for (const v of [item.totalBytes, item.fileSize]) {
    if (typeof v === 'number' && v > 0) return v;
  }
  return -1;
}

// shouldCapture decides whether a browser download (a downloads.DownloadItem)
// is taken over by the daemon. It returns {capture, reason, url}.
//   settings: the stored settings (capture rules + daemonUrl)
//   ownId:    this extension's id (downloads we restarted ourselves are skipped)
export function shouldCapture(item, settings, ownId) {
  const rules = { ...DEFAULT_SETTINGS.capture, ...(settings?.capture || {}) };
  const url = item.finalUrl || item.url || '';
  const no = (reason) => ({ capture: false, reason, url });
  if (!rules.enabled) return no('disabled');
  if (!isHttpUrl(url)) return no('scheme');
  if (ownId && item.byExtensionId === ownId) return no('own');
  if (rules.skipIncognito && item.incognito) return no('incognito');
  if (item.state && item.state !== 'in_progress') return no('state');
  let host;
  try {
    host = new URL(url).hostname;
    const daemon = new URL(normalizeDaemonUrl(settings?.daemonUrl));
    if (new URL(url).origin === daemon.origin) return no('daemon');
  } catch {
    return no('url');
  }
  if (hostMatchesAny(host, rules.excludeHosts)) return no('excluded');
  const include = parseList(rules.includeHosts);
  if (include.length && !hostMatchesAny(host, include)) return no('not-included');
  const exts = parseExtensions(rules.extensions);
  if (exts.length) {
    const ext = itemExtension(item);
    const ok = exts.includes(ext) || (exts.includes('001') && /^\d{3}$/.test(ext));
    if (!ok) return no('extension');
  }
  const min = Number(rules.minSizeMB) || 0;
  const size = itemSize(item);
  if (min > 0 && size >= 0 && size < min * 1024 * 1024) return no('size');
  return { capture: true, reason: 'match', url };
}

// mergeSettings fills missing keys with defaults (stored settings may come
// from an older version).
export function mergeSettings(stored) {
  const s = stored && typeof stored === 'object' ? stored : {};
  return {
    ...DEFAULT_SETTINGS,
    ...s,
    capture: { ...DEFAULT_SETTINGS.capture, ...(s.capture || {}) },
  };
}

// ---------------------------------------------------------------- cookies

function cookieKey(c) {
  return `${c.domain}\t${c.path}\t${c.name}\t${c.storeId ?? ''}\t${c.partitionKey ? JSON.stringify(c.partitionKey) : ''}`;
}

export function dedupeCookies(cookies) {
  const m = new Map();
  for (const c of cookies) m.set(cookieKey(c), c);
  return [...m.values()];
}

function clean(v) {
  // Tabs and new lines would break the line-oriented format.
  return String(v ?? '').replace(/[\t\r\n]/g, ' ');
}

// toNetscape serializes cookies (cookies.Cookie objects) as a Netscape
// cookies.txt file, the format accounts.Secret.Cookies accepts. HttpOnly
// cookies use the "#HttpOnly_" prefix (curl / yt-dlp convention).
export function toNetscape(cookies) {
  const lines = [
    '# Netscape HTTP Cookie File',
    '# Exportado por la extensión de AdminDescargas. No lo compartas.',
    '',
  ];
  for (const c of cookies) {
    let domain = clean(c.domain);
    const includeSub = !c.hostOnly;
    if (includeSub && !domain.startsWith('.')) domain = '.' + domain;
    if (!includeSub) domain = domain.replace(/^\.+/, '');
    const expiry = c.session || !c.expirationDate ? 0 : Math.floor(c.expirationDate);
    lines.push(
      [
        (c.httpOnly ? '#HttpOnly_' : '') + domain,
        includeSub ? 'TRUE' : 'FALSE',
        clean(c.path || '/'),
        c.secure ? 'TRUE' : 'FALSE',
        String(expiry),
        clean(c.name),
        clean(c.value),
      ].join('\t'),
    );
  }
  return lines.join('\n') + '\n';
}

// ---------------------------------------------------------------- API data

export function parseStats(json) {
  const s = json && typeof json === 'object' ? (json.stats && typeof json.stats === 'object' ? json.stats : json) : {};
  const n = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
  return {
    speed: n(s.speed),
    active: n(s.active),
    queued: n(s.queued),
    waiting: n(s.waiting),
    failed: n(s.failed),
    completed: n(s.completed),
    grabberSize: n(s.grabber_size),
    paused: Boolean(s.paused),
  };
}

export const BADGE_OK_COLOR = '#2563eb';
export const BADGE_ERROR_COLOR = '#d93025';

export function badgeForStats(stats) {
  const a = stats.active;
  return { text: a > 0 ? (a > 999 ? '999+' : String(a)) : '', color: BADGE_OK_COLOR };
}

export function badgeForError() {
  return { text: '!', color: BADGE_ERROR_COLOR };
}

// countAdded returns how many LinkGrabber entries the daemon created, or null
// if the response does not say.
export function countAdded(resp) {
  if (Array.isArray(resp)) return resp.length;
  if (resp && Array.isArray(resp.entries)) return resp.entries.length;
  if (resp && typeof resp.count === 'number') return resp.count;
  return null;
}

// errorMessage builds a Spanish message for a failed API call. bodyText is
// the response body (JSON {"error": "..."} or text); it is shortened.
export function errorMessage(status, bodyText) {
  let detail = '';
  if (bodyText) {
    try {
      const j = JSON.parse(bodyText);
      detail = String(j.error || j.message || '');
    } catch {
      detail = String(bodyText);
    }
    detail = detail.trim().slice(0, 200);
  }
  let base;
  switch (status) {
    case 0:
      base = 'No se pudo conectar con AdminDescargas. ¿Está el daemon en marcha y la URL es correcta?';
      break;
    case 401:
      base = 'Token incorrecto o ausente: revisa el token en las opciones.';
      break;
    case 403:
      base = 'El daemon rechazó la petición (403). Añade el origen de la extensión a server.allowed_origins.';
      break;
    case 404:
      base = 'El daemon no conoce este endpoint (404). ¿Versión antigua?';
      break;
    default:
      base = `Error del daemon (HTTP ${status}).`;
  }
  return detail ? `${base} ${detail}` : base;
}

export function formatSpeed(bps) {
  if (!bps || bps <= 0) return '0 B/s';
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let i = 0;
  let v = bps;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
