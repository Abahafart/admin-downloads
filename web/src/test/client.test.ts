// API error texts: the accounts package answers some errors with internal
// English sentinels ("accounts: ..."); the UI shows them in Spanish.
import { describe, expect, it } from 'vitest';
import { Api, ApiError, localizeError, parseRetryAfter } from '../lib/api/client';

describe('localizeError', () => {
  it('translates the vault and OAuth sentinels', () => {
    expect(localizeError(423, 'locked', 'accounts: secret store is locked')).toMatch(/^Las credenciales están bloqueadas/);
    expect(localizeError(423, 'locked', 'accounts: save credentials: accounts: secret store is locked')).toMatch(/bloqueadas/);
    expect(localizeError(403, 'wrong_password', 'accounts: wrong master password')).toBe('La contraseña maestra no es correcta.');
    expect(localizeError(400, 'invalid_argument', 'accounts: the master password must have at least 8 characters')).toBe(
      'La contraseña maestra debe tener al menos 8 caracteres.',
    );
    expect(localizeError(400, 'invalid_argument', 'accounts: OAuth state is invalid or expired: paste the full address')).toMatch(/caducó/);
    expect(localizeError(400, 'invalid_argument', 'accounts: invalid OAuth redirect URL: "http://nas:7979/x": Google only accepts loopback')).toMatch(/127\.0\.0\.1/);
  });

  it('keeps messages that are already for users', () => {
    expect(localizeError(400, 'invalid_argument', 'falta password')).toBe('falta password');
    expect(localizeError(403, 'wrong_password', 'Contraseña maestra incorrecta (demo)')).toBe('Contraseña maestra incorrecta (demo)');
    expect(localizeError(400, 'invalid_argument', 'accounts: something new')).toBe('something new');
  });

  it('rate limits show the wait', () => {
    expect(localizeError(429, 'rate_limited', 'demasiados intentos', 12)).toBe('Demasiados intentos fallidos. Espera 12 s antes de volver a intentarlo.');
    expect(localizeError(429, 'rate_limited', 'demasiados intentos')).toBe('demasiados intentos');
  });

  it('parses Retry-After seconds and dates', () => {
    expect(parseRetryAfter('30')).toBe(30);
    expect(parseRetryAfter(null)).toBeUndefined();
    expect(parseRetryAfter('Wed, 21 Oct 2015 07:28:10 GMT', Date.parse('Wed, 21 Oct 2015 07:28:00 GMT'))).toBe(10);
    expect(parseRetryAfter('pronto')).toBeUndefined();
  });

  it('ApiError carries the status, code and Retry-After of the response', async () => {
    const api = new Api({ mode: 'web', baseURL: 'http://x/' }, async () =>
      new Response(JSON.stringify({ error: { code: 'rate_limited', message: 'demasiados intentos fallidos' } }), {
        status: 429,
        headers: { 'Content-Type': 'application/json', 'Retry-After': '7' },
      }),
    );
    const err = await api.unlockSecrets('x').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 429, code: 'rate_limited', retryAfter: 7 });
    expect(err.message).toMatch(/Espera 7 s/);
  });
});
