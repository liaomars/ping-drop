/** WebSocket 客户端连接状态 */
export type WsStatus = 'connecting' | 'open' | 'closed'

/** WsClient 初始化配置选项 */
export interface WsClientOptions {
  /** WebSocket 服务端地址 */
  url: string
  /** 建连成功时用于生成 hello 握手载荷的回调 */
  getHello: () => { name?: string; deviceType: string; os: string; browser: string; roomCode?: string }
  /** 收到合法服务端事件信封时的分发回调 */
  onEvent: (type: string, payload: unknown) => void
  /** 连接状态变更回调 */
  onStatus?: (status: WsStatus) => void
  /** 可选自定义 WebSocket 构造函数，便于单元测试注入 Mock */
  WebSocketImpl?: typeof WebSocket
}

const HEARTBEAT_MS = 25_000
const BACKOFF_SCHEDULE_MS = [1_000, 2_000, 4_000, 8_000, 15_000]

/**
 * 封装 WebSocket 建连握手、25 秒心跳、指数退避重连与页面切回前台自动恢复。
 */
export class WsClient {
  private readonly opts: WsClientOptions
  private readonly SocketCtor: typeof WebSocket
  private ws: WebSocket | null = null
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private backoffIndex = 0
  private manuallyClosed = false
  private readonly visibilityHandler = (): void => {
    if (document.visibilityState === 'visible' && !this.manuallyClosed && (!this.ws || this.ws.readyState !== 1)) {
      this.clearReconnectTimer()
      this.backoffIndex = 0
      this.openSocket()
    }
  }

  /** 创建 WsClient 实例 */
  constructor(opts: WsClientOptions) {
    this.opts = opts
    this.SocketCtor = opts.WebSocketImpl ?? WebSocket
  }

  /** 启动连接并监听浏览器可见性变化 */
  connect(): void {
    this.manuallyClosed = false
    this.clearReconnectTimer()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.visibilityHandler)
      document.addEventListener('visibilitychange', this.visibilityHandler)
    }
    this.openSocket()
  }

  /** 发送标准 JSON 事件信封；未处于 open 状态时返回 false */
  send(type: string, payload: unknown = {}): boolean {
    if (!this.ws || this.ws.readyState !== 1) {
      return false
    }
    this.ws.send(JSON.stringify({ type, payload }))
    return true
  }

  /** 主动关闭连接并停止重连与心跳 */
  close(): void {
    this.manuallyClosed = true
    this.clearHeartbeat()
    this.clearReconnectTimer()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.visibilityHandler)
    }
    const sock = this.ws
    this.ws = null
    sock?.close()
    this.opts.onStatus?.('closed')
  }

  private openSocket(): void {
    this.clearHeartbeat()
    const prev = this.ws
    this.opts.onStatus?.('connecting')
    const sock = new this.SocketCtor(this.opts.url)
    this.ws = sock
    if (prev && prev !== sock) {
      prev.close()
    }

    sock.onopen = () => {
      if (this.ws !== sock || this.manuallyClosed) {
        return
      }
      this.backoffIndex = 0
      this.opts.onStatus?.('open')
      sock.send(JSON.stringify({ type: 'hello', payload: this.opts.getHello() }))
      this.startHeartbeat()
    }

    sock.onmessage = (ev) => {
      if (this.ws !== sock) {
        return
      }
      try {
        const parsed = JSON.parse(String(ev.data)) as { type?: unknown; payload?: unknown }
        if (typeof parsed.type === 'string' && parsed.type.length > 0) {
          this.opts.onEvent(parsed.type, parsed.payload)
        }
      } catch {
        // 忽略非法帧
      }
    }

    sock.onerror = () => {
      // 交由 onclose 统一触发退避重连
    }

    sock.onclose = () => {
      if (this.ws !== sock) {
        return
      }
      this.clearHeartbeat()
      this.ws = null
      this.opts.onStatus?.('closed')
      if (!this.manuallyClosed) {
        this.scheduleReconnect()
      }
    }
  }

  private startHeartbeat(): void {
    this.clearHeartbeat()
    this.heartbeatTimer = setInterval(() => {
      this.send('ping', {})
    }, HEARTBEAT_MS)
  }

  private clearHeartbeat(): void {
    if (this.heartbeatTimer !== null) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  private scheduleReconnect(): void {
    this.clearReconnectTimer()
    const delay = BACKOFF_SCHEDULE_MS[Math.min(this.backoffIndex, BACKOFF_SCHEDULE_MS.length - 1)]
    this.backoffIndex = Math.min(this.backoffIndex + 1, BACKOFF_SCHEDULE_MS.length - 1)
    this.reconnectTimer = setTimeout(() => {
      if (!this.manuallyClosed) {
        this.openSocket()
      }
    }, delay)
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }
}
