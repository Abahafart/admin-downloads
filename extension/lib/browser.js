// Tiny WebExtension shim: Firefox exposes the promise-based `browser`
// namespace, Chrome MV3 returns promises from `chrome.*` when no callback is
// given. Everything in the extension uses `ext`.
export const ext = globalThis.browser ?? globalThis.chrome;
