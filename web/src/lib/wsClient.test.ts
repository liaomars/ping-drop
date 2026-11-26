import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { WsClient, type WsStatus } from './wsClient'

class MockSocket {
  static instances: MockSocket[] = []
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(public readonly url: string) {
    MockSocket.instances.push(this)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.()
  }

  send(data: string): void {
    this.sent.push(data)
  }

  emitMessage(data: string): void {
    this.onmessage?.({ data })
  }

  close(): void {
    if (this.readyState === 3) {
      return
    }
    this.readyState = 3
    this.onclose?.()
  }
}

function setVisibility(state: DocumentVisibilityState): void {
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => state
  })
}

describe('WsClient', () => {
  beforeEach(() => {
    MockSocket.instances = []
    vi.useFakeTimers()
    setVisibility('visible')
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('sends hello on open, heartbeats every 25s, and reconnects with exponential backoff up to 15s', () => {
    const onEvent = vi.fn()
    const statuses: WsStatus[] = []
    const client = new WsClient({
      url: 'ws://localhost/ws',
      getHello: () => ({ deviceType: 'desktop', os: 'macOS', browser: 'Chrome', roomCode: '482917' }),
      onEvent,
      onStatus: (status) => statuses.push(status),
      WebSocketImpl: MockSocket as unknown as typeof WebSocket
    })

    client.connect()
    expect(MockSocket.instances).toHaveLength(1)
    expect(statuses).toEqual(['connecting'])

    const s1 = MockSocket.instances[0]
    s1.open()
    expect(statuses).toEqual(['connecting', 'open'])
    expect(JSON.parse(s1.sent[0])).toMatchObject({
      type: 'hello',
      payload: { roomCode: '482917' }
    })

    // 25s 心跳
    vi.advanceTimersByTime(25_000)
    expect(JSON.parse(s1.sent[1])).toEqual({ type: 'ping', payload: {} })

    // 断线后按 1s -> 2s -> 4s -> 8s -> 15s 退避重连
    const expectedDelays = [1_000, 2_000, 4_000, 8_000, 15_000, 15_000]
    for (let i = 0; i < expectedDelays.length; i++) {
      const sock = MockSocket.instances[i]
      sock.close()
      vi.advanceTimersByTime(expectedDelays[i] - 1)
      expect(MockSocket.instances).toHaveLength(i + 1)
      vi.advanceTimersByTime(1)
      expect(MockSocket.instances).toHaveLength(i + 2)
    }

    client.close()
    expect(statuses[statuses.length - 1]).toBe('closed')
  })

  it('dispatches valid JSON events to onEvent and ignores malformed frames', () => {
    const onEvent = vi.fn()
    const client = new WsClient({
      url: 'ws://localhost/ws',
      getHello: () => ({ deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }),
      onEvent,
      WebSocketImpl: MockSocket as unknown as typeof WebSocket
    })

    client.connect()
    const s1 = MockSocket.instances[0]
    s1.open()

    s1.emitMessage(JSON.stringify({ type: 'welcome', payload: { peerId: 'p1' } }))
    s1.emitMessage('not-json')
    s1.emitMessage(JSON.stringify({ payload: { missingType: true } }))

    expect(onEvent).toHaveBeenCalledTimes(1)
    expect(onEvent).toHaveBeenCalledWith('welcome', { peerId: 'p1' })

    client.close()
  })

  it('sends envelopes only while open and stops reconnecting after close', () => {
    const client = new WsClient({
      url: 'ws://localhost/ws',
      getHello: () => ({ deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }),
      onEvent: vi.fn(),
      WebSocketImpl: MockSocket as unknown as typeof WebSocket
    })

    expect(client.send('dm.send', { text: 'hi' })).toBe(false)

    client.connect()
    const s1 = MockSocket.instances[0]
    expect(client.send('dm.send', { text: 'hi' })).toBe(false)

    s1.open()
    expect(client.send('dm.send', { text: 'hi' })).toBe(true)
    expect(JSON.parse(s1.sent[1])).toEqual({ type: 'dm.send', payload: { text: 'hi' } })

    client.close()
    expect(client.send('dm.send', { text: 'after-close' })).toBe(false)
    vi.advanceTimersByTime(30_000)
    expect(MockSocket.instances).toHaveLength(1)
  })

  it('reconnects immediately and resets backoff when returning to foreground', () => {
    const client = new WsClient({
      url: 'ws://localhost/ws',
      getHello: () => ({ deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }),
      onEvent: vi.fn(),
      WebSocketImpl: MockSocket as unknown as typeof WebSocket
    })

    client.connect()
    const s1 = MockSocket.instances[0]
    s1.open()

    // 触发一次断线进入 1s 退避，并推进到 2s 退避阶段
    s1.close()
    vi.advanceTimersByTime(1_000)
    expect(MockSocket.instances).toHaveLength(2)
    MockSocket.instances[1].close()

    // 切到后台再切回前台应立即重连并重置退避
    setVisibility('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(MockSocket.instances).toHaveLength(2)

    setVisibility('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(MockSocket.instances).toHaveLength(3)

    // 再次断线时退避应从 1s 重新开始
    MockSocket.instances[2].close()
    vi.advanceTimersByTime(999)
    expect(MockSocket.instances).toHaveLength(3)
    vi.advanceTimersByTime(1)
    expect(MockSocket.instances).toHaveLength(4)

    // 手动关闭后切回前台不应再触发重连
    client.close()
    setVisibility('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(MockSocket.instances).toHaveLength(4)
  })
})
