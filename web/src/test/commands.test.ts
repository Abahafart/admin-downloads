import { describe, expect, it } from 'vitest';
import { hasLinks, parseCommand, suggest } from '../lib/commands';
import { isTypingTarget } from '../lib/keyboard';

describe('command bar', () => {
  it('matches the longest command name', () => {
    expect(parseCommand('/reintentar fallidas')?.cmd.name).toBe('/reintentar fallidas');
    expect(parseCommand('/pausa')?.cmd.name).toBe('/pausa');
    expect(parseCommand('rotar')?.cmd.name).toBe('/rotar');
    expect(parseCommand('/tema claro')).toMatchObject({ arg: 'claro' });
    expect(parseCommand('/nada')).toBeNull();
  });
  it('suggests while typing and detects pasted links', () => {
    expect(suggest('/re').map((c) => c.name)).toEqual(expect.arrayContaining(['/reanudar', '/reintentar fallidas', '/red']));
    expect(hasLinks('mira https://mega.nz/file/abc#k y más')).toBe(true);
    expect(suggest('https://mega.nz/x')).toEqual([]);
  });
});

describe('shortcuts are ignored while typing', () => {
  const el = (tagName: string, type?: string, isContentEditable = false) => ({ tagName, type, isContentEditable }) as unknown as EventTarget;
  it('text fields count as typing, checkboxes and buttons do not', () => {
    expect(isTypingTarget(el('INPUT', 'text'))).toBe(true);
    expect(isTypingTarget(el('INPUT', 'search'))).toBe(true);
    expect(isTypingTarget(el('TEXTAREA'))).toBe(true);
    expect(isTypingTarget(el('DIV', undefined, true))).toBe(true);
    expect(isTypingTarget(el('INPUT', 'checkbox'))).toBe(false);
    expect(isTypingTarget(el('BUTTON'))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});
