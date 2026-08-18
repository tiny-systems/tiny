// One socket for every live stream.
//
// gRPC-web runs each server-stream on its own HTTP request, and a browser
// allows six connections PER HOST across every tab. The editor holds three
// long-lived streams per page (project or flow, statistics, activity), so two
// open tabs exhausted the pool exactly and every request afterwards — the one
// carrying widget data included — queued forever. The dashboard rendered
// empty cards and the page looked frozen.
//
// A WebSocket is one connection and is not subject to that cap. This client
// carries every stream over a single socket to tiny's /ws endpoint, addressed
// by id. The server sends the same protobuf messages its gRPC handlers
// already produce, so each event is decoded with the generated message class
// and the editor receives exactly what gRPC-web handed it before.
//
// Unary calls stay on gRPC-web: they are short-lived and never hold a
// connection open.

type Decoder<T> = { fromBinary(bytes: Uint8Array): T }

interface Subscription {
  push: (value: unknown) => void
  fail: (err: Error) => void
  finish: () => void
}

const base64ToBytes = (b64: string): Uint8Array => {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export class MuxTransport {
  private url: string
  private socket: WebSocket | null = null
  private opening: Promise<WebSocket> | null = null
  private subs = new Map<string, Subscription>()
  private nextId = 1

  constructor(origin: string) {
    // Same origin as the SPA; ws for http, wss for https.
    this.url = origin.replace(/^http/, 'ws') + '/ws'
  }

  private async connect(): Promise<WebSocket> {
    if (this.socket && this.socket.readyState === WebSocket.OPEN) return this.socket
    if (this.opening) return this.opening

    this.opening = new Promise<WebSocket>((resolve, reject) => {
      const ws = new WebSocket(this.url)
      ws.onopen = () => {
        this.socket = ws
        this.opening = null
        resolve(ws)
      }
      ws.onerror = () => {
        this.opening = null
        reject(new Error('mux socket failed to open'))
      }
      ws.onclose = () => {
        this.socket = null
        // Every subscription dies with the socket. Callers already handle a
        // stream ending — the editor's stores reconnect and re-request a full
        // snapshot — so failing them is the honest signal.
        const subs = [...this.subs.values()]
        this.subs.clear()
        for (const s of subs) s.fail(new Error('mux socket closed'))
      }
      ws.onmessage = (ev) => this.dispatch(ev.data)
    })
    return this.opening
  }

  private dispatch(raw: unknown) {
    if (typeof raw !== 'string') return
    let frame: { id?: string; event?: string; error?: string; end?: boolean }
    try {
      frame = JSON.parse(raw)
    } catch {
      return
    }
    if (!frame.id) return
    const sub = this.subs.get(frame.id)
    if (!sub) return

    if (frame.error) {
      this.subs.delete(frame.id)
      sub.fail(new Error(frame.error))
      return
    }
    if (frame.end) {
      this.subs.delete(frame.id)
      sub.finish()
      return
    }
    if (frame.event) sub.push(base64ToBytes(frame.event))
  }

  // stream opens one multiplexed subscription and yields decoded messages,
  // matching the AsyncIterable shape the editor consumes from connect-web —
  // including honouring an AbortSignal.
  stream<T>(kind: string, req: unknown, decoder: Decoder<T>, signal?: AbortSignal): AsyncIterable<T> {
    const id = String(this.nextId++)
    const queue: Uint8Array[] = []
    let waiting: ((v: IteratorResult<Uint8Array>) => void) | null = null
    let failure: Error | null = null
    let done = false

    const sub: Subscription = {
      push: (value) => {
        const bytes = value as Uint8Array
        if (waiting) {
          const w = waiting
          waiting = null
          w({ value: bytes, done: false })
        } else {
          queue.push(bytes)
        }
      },
      fail: (err) => {
        failure = err
        if (waiting) {
          const w = waiting
          waiting = null
          w({ value: undefined as never, done: true })
        }
      },
      finish: () => {
        done = true
        if (waiting) {
          const w = waiting
          waiting = null
          w({ value: undefined as never, done: true })
        }
      }
    }
    this.subs.set(id, sub)

    const cancel = () => {
      this.subs.delete(id)
      const ws = this.socket
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ id, cancel: true }))
      }
      sub.finish()
    }
    signal?.addEventListener('abort', cancel, { once: true })

    const self = this
    return {
      async *[Symbol.asyncIterator]() {
        try {
          // An already-aborted signal never fires 'abort', so the listener
          // below would never clean up: bail before subscribing.
          if (signal?.aborted) return
          const ws = await self.connect()
          ws.send(JSON.stringify({ id, kind, req }))
          for (;;) {
            if (failure) throw failure
            if (queue.length > 0) {
              yield decoder.fromBinary(queue.shift() as Uint8Array)
              continue
            }
            if (done) return
            const next = await new Promise<IteratorResult<Uint8Array>>((resolve) => {
              waiting = resolve
            })
            if (failure) throw failure
            if (next.done) return
            yield decoder.fromBinary(next.value)
          }
        } finally {
          signal?.removeEventListener('abort', cancel)
          self.subs.delete(id)
          const open = self.socket
          if (open && open.readyState === WebSocket.OPEN && !done) {
            open.send(JSON.stringify({ id, cancel: true }))
          }
        }
      }
    }
  }
}
