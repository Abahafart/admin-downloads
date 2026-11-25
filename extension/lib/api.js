// Daemon API client and shared helpers that need browser APIs (storage,
// badge). Never log the settings object: it contains the API token.
import { ext } from './browser.js';
import {
  apiUrl,
  authHeaders,
  badgeForError,
  badgeForStats,
  errorMessage,
  mergeSettings,
  parseStats,
} from './core.js';

const SETTINGS_KEY = 'settings';
const TIMEOUT_MS = 15000;

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

export async function loadSettings() {
  const got = await ext.storage.local.get(SETTINGS_KEY);
  return mergeSettings(got?.[SETTINGS_KEY]);
}

export async function saveSettings(settings) {
  await ext.storage.local.set({ [SETTINGS_KEY]: mergeSettings(settings) });
}

export function isSettingsChange(changes, area) {
  return area === 'local' && Object.prototype.hasOwnProperty.call(changes, SETTINGS_KEY);
}

// apiRequest calls {daemonUrl}/api/v1{path}. It throws ApiError (status 0 for
// network errors / timeouts) with a Spanish message.
export async function apiRequest(method, path, body, settings) {
  const s = settings ?? (await loadSettings());
  const headers = authHeaders(s.token);
  const init = { method, headers, cache: 'no-store', credentials: 'omit' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), TIMEOUT_MS);
  init.signal = ctl.signal;
  let resp;
  try {
    resp = await fetch(apiUrl(s.daemonUrl, path), init);
  } catch {
    throw new ApiError(0, errorMessage(0));
  } finally {
    clearTimeout(timer);
  }
  const text = await resp.text().catch(() => '');
  if (!resp.ok) throw new ApiError(resp.status, errorMessage(resp.status, text));
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

async function setBadge({ text, color }, title) {
  try {
    await ext.action.setBadgeText({ text });
    await ext.action.setBadgeBackgroundColor({ color });
    await ext.action.setTitle({ title });
  } catch {
    /* action API unavailable (tests, old browsers) */
  }
}

// refreshBadge fetches /stats and shows the active download count. It
// returns the parsed stats, or throws the ApiError after showing "!".
export async function refreshBadge(settings) {
  const s = settings ?? (await loadSettings());
  if (!s.token) {
    await setBadge(badgeForError(), 'AdminDescargas: configura el token en las opciones');
    throw new ApiError(401, errorMessage(401));
  }
  try {
    const stats = parseStats(await apiRequest('GET', '/stats', undefined, s));
    await setBadge(
      badgeForStats(stats),
      `AdminDescargas: ${stats.active} activas, ${stats.queued} en cola`,
    );
    return stats;
  } catch (err) {
    await setBadge(badgeForError(), `AdminDescargas: ${err.message}`);
    throw err;
  }
}

export async function notify(title, message) {
  try {
    await ext.notifications.create({
      type: 'basic',
      iconUrl: ext.runtime.getURL('icons/icon-128.png'),
      title,
      message,
    });
  } catch {
    /* notifications blocked */
  }
}
