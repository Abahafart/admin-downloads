// User-facing names for hosters, egress kinds and account fields.
import type { AccountKind, AccountStatus, EgressKind, EgressStatus } from '../api/types';

const HOSTERS: Record<string, string> = {
  mega: 'MEGA',
  gdrive: 'Google Drive',
  mediafire: 'MediaFire',
  terabox: 'TeraBox',
  generic: 'HTTP',
  pixeldrain: 'Pixeldrain',
  gofile: 'Gofile',
  dropbox: 'Dropbox',
  onedrive: 'OneDrive',
  onefichier: '1fichier',
  yandex: 'Yandex Disk',
};

export function hosterName(id: string | undefined): string {
  if (!id) return '—';
  return HOSTERS[id] ?? id.charAt(0).toUpperCase() + id.slice(1);
}

/** Hosters offered in Cuentas, with the credential kinds each supports. */
export const ACCOUNT_HOSTERS: { id: string; kinds: AccountKind[] }[] = [
  { id: 'mega', kinds: ['password'] },
  { id: 'gdrive', kinds: ['oauth', 'apikey'] },
  { id: 'terabox', kinds: ['cookie'] },
  { id: 'mediafire', kinds: ['cookie', 'password'] },
  { id: 'pixeldrain', kinds: ['apikey'] },
  { id: 'onefichier', kinds: ['apikey'] },
  { id: 'gofile', kinds: ['apikey'] },
  { id: 'dropbox', kinds: ['oauth', 'cookie'] },
  { id: 'onedrive', kinds: ['cookie'] },
  { id: 'yandex', kinds: ['cookie'] },
];

export const KIND_NAMES: Record<AccountKind, string> = {
  password: 'Usuario y contraseña',
  cookie: 'Cookies',
  oauth: 'Cuenta de Google (OAuth)',
  apikey: 'Clave de API',
};

export const ACCOUNT_STATUS: Record<AccountStatus, string> = {
  unknown: 'Sin comprobar',
  ok: 'Funciona',
  invalid: 'Credenciales incorrectas',
  expired: 'Caducada',
  quota: 'Cuota agotada',
  needs_2fa: 'Falta el código 2FA',
  error: 'Error al comprobar',
  disabled: 'Desactivada',
  checking: 'Comprobando…',
};

export const EGRESS_KIND: Record<EgressKind, string> = {
  direct: 'Directa',
  http: 'Proxy HTTP',
  socks5: 'Proxy SOCKS5',
  tor: 'Tor',
  wireguard: 'WireGuard',
  router: 'Router',
  systemvpn: 'VPN del sistema',
};

/** Guesses the kind from an egress id ("wg:x", "socks5:..."), for unknown ids. */
export function kindFromId(id: string): EgressKind | undefined {
  const p = id.split(/[:/]/)[0].toLowerCase();
  if (p === 'direct') return 'direct';
  if (p === 'tor') return 'tor';
  if (p === 'wg' || p === 'wireguard') return 'wireguard';
  if (p === 'socks5' || p === 'socks') return 'socks5';
  if (p === 'http' || p === 'https') return 'http';
  if (p === 'router') return 'router';
  if (p === 'vpn' || p === 'systemvpn') return 'systemvpn';
  return undefined;
}

/** "Directa", "Tor", "WireGuard", "Proxy SOCKS5"... ("" when no egress). */
export function egressName(id: string | undefined, list: Record<string, EgressStatus> | EgressStatus[]): string {
  if (!id) return '';
  const e = Array.isArray(list) ? list.find((x) => x.id === id) : list[id];
  const kind = e?.kind ?? kindFromId(id);
  return kind ? EGRESS_KIND[kind] : (e?.label ?? id);
}
