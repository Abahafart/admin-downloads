// Server-Sent Events over fetch streaming (EventSource cannot send an
// Authorization header). Reconnects with backoff and Last-Event-ID.
import type { Api } from './client';
import type { CoreEvent } from './types';

export interface SSEMessage {
  id: string;
  event: string;
  data: string;
}

/** Incremental parser for the text/event-stream format (WHATWG spec). */
export class SSEParser {
  private buf = '';
  private data: string[] = [];
  private event = '';
  /** Last id seen (persists across messages, per spec). */
  lastId = '';
  /** Reconnection time requested by the server (retry:), ms. */
  retry: number | null = null;

  constructor(private readonly onMessage: (m: SSEMessage) => void) {}

  push(chunk: string): void {
    this.buf += chunk;
    let i: number;
    while ((i = this.buf.search(/\r\n|\r|\n/)) >= 0) {
      const line = this.buf.slice(0, i);
      const nl = this.buf[i] === '\r' && this.buf[i + 1] === '\n' ? 2 : 1;
      // A lone trailing \r may be the first half of \r\n: wait for more.
      if (this.buf[i] === '\r' && i + 1 === this.buf.length) break;
      this.buf = this.buf.slice(i + nl);
      this.line(line);
    }
  }

  private line(line: string): void {
    if (line === '') {
      if (this.data.length) this.onMessage({ id: this.lastId, event: this.event || 'message', data: this.data.join('\n') });
      this.data = [];
      this.event = '';
      return;
    }
    if (line.startsWith(':')) return;
    const c = line.indexOf(':');
    const field = c < 0 ? line : line.slice(0, c);
    let value = c < 0 ? '' : line.slice(c + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    switch (field) {
      case 'data':
        this.data.push(value);
        break;
      case 'event':
        this.event = value;
        break;
      case 'id':
        if (!value.includes('\0')) this.lastId = value;
        break;
      case 'retry':
        if (/^\d+$/.test(value)) this.retry = Number(value);
        break;
    }
  }
}

export interface StreamHandlers {
  onEvent(ev: CoreEvent): void;
  /** Called after every successful (re)connection, before any event. */
  onOpen(reconnect: boolean): void;
  onDown(err: unknown): void;
}

/** Keeps GET /api/v1/events open, reconnecting with Last-Event-ID. */
export class EventStream {
  lastEventId = '';
  private ctrl: AbortController | null = null;
  private stopped = false;
  private delay = 1000;
  private opened = false;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly api: Api,
    private readonly h: StreamHandlers,
    private readonly maxDelay = 15000,
  ) {}

  start(): void {
    this.stopped = false;
    void this.loop();
  }

  stop(): void {
    this.stopped = true;
    this.ctrl?.abort();
    if (this.timer) clearTimeout(this.timer);
  }

  private async loop(): Promise<void> {
    while (!this.stopped) {
      this.ctrl = new AbortController();
      const parser = new SSEParser((m) => {
        this.lastEventId = m.id;
        let ev: CoreEvent;
        try {
          ev = JSON.parse(m.data) as CoreEvent;
        } catch {
          return;
        }
        if (!ev.type && m.event !== 'message') ev.type = m.event as CoreEvent['type'];
        this.h.onEvent(ev);
      });
      parser.lastId = this.lastEventId;
      try {
        const headers: Record<string, string> = { Accept: 'text/event-stream' };
        if (this.lastEventId) headers['Last-Event-ID'] = this.lastEventId;
        const r = await this.api.raw('events', { headers, signal: this.ctrl.signal, cache: 'no-store' });
        if (r.status === 401) {
          this.api.onUnauthorized?.();
          this.stopped = true;
          return;
        }
        if (!r.ok || !r.body) throw new Error(`events: HTTP ${r.status}`);
        this.h.onOpen(this.opened);
        this.opened = true;
        this.delay = 1000;
        const reader = r.body.getReader();
        const dec = new TextDecoder();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          parser.push(dec.decode(value, { stream: true }));
        }
        throw new Error('events: stream closed');
      } catch (err) {
        if (this.stopped) return;
        this.h.onDown(err);
        const wait = parser.retry ?? this.delay;
        this.delay = Math.min(this.delay * 2, this.maxDelay);
        await new Promise<void>((res) => (this.timer = setTimeout(res, wait)));
      }
    }
  }
}
