import { describe, expect, it } from 'vitest';
import { SSEParser, type SSEMessage } from '../lib/api/sse';

function parse(chunks: string[]) {
  const out: SSEMessage[] = [];
  const p = new SSEParser((m) => out.push(m));
  for (const c of chunks) p.push(c);
  return { out, p };
}

describe('SSEParser', () => {
  it('parses id / event / data and survives arbitrary chunk splits', () => {
    const text = 'id: 7\nevent: progress\ndata: {"seq":7}\n\n: comment\nid: 8\nevent: stats\ndata: {"a":\ndata: 1}\n\n';
    for (let i = 1; i < text.length; i++) {
      const { out } = parse([text.slice(0, i), text.slice(i)]);
      expect(out).toEqual([
        { id: '7', event: 'progress', data: '{"seq":7}' },
        { id: '8', event: 'stats', data: '{"a":\n1}' },
      ]);
    }
  });

  it('handles CRLF, retry and keeps the last id', () => {
    const { out, p } = parse(['retry: 2500\r\nid: 3\r\ndata: x\r', '\n\r\ndata: y\r\n\r\n']);
    expect(p.retry).toBe(2500);
    expect(out).toEqual([
      { id: '3', event: 'message', data: 'x' },
      { id: '3', event: 'message', data: 'y' },
    ]);
    expect(p.lastId).toBe('3');
  });

  it('ignores events without data', () => {
    const { out } = parse(['event: ping\n\n']);
    expect(out).toEqual([]);
  });
});
