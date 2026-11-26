/** 时间线条目类型 */
export type ItemKind = 'text' | 'image' | 'file'

/** 空间类型 */
export type SpaceKind = 'lan' | 'room'

/** 服务端标准业务错误码 */
export type ErrorCode =
  | 'INVALID_PAYLOAD'
  | 'ROOM_NOT_FOUND'
  | 'ROOM_LIMIT_REACHED'
  | 'RATE_LIMITED'
  | 'TIMELINE_DISABLED'
  | 'SPACE_QUOTA_EXCEEDED'
  | 'PEER_NOT_FOUND'
  | 'PAYLOAD_TOO_LARGE'

/** rtc.signal 透传信号类型 */
export type SignalType =
  | 'file.offer'
  | 'file.accept'
  | 'file.reject'
  | 'file.cancel'
  | 'rtc.offer'
  | 'rtc.answer'
  | 'rtc.ice'
  | 'relay.start'

/** 服务端下发的容量与中转限额 */
export interface Limits {
  maxTimelineFileMB: number
  maxRelayMB: number
}

/** 空间内的在线设备 */
export interface PeerInfo {
  id: string
  name: string
  deviceType: string
  os: string
  browser: string
}

/** 空间摘要状态 */
export interface SpaceSummary {
  kind: SpaceKind
  code?: string
  sharedTimelineEnabled: boolean
}

/** 共享时间线条目 */
export interface TimelineItem {
  id: string
  kind: ItemKind
  senderId: string
  senderName: string
  text?: string
  blobId?: string
  fileName?: string
  mime?: string
  size?: number
  createdAt: number
  expiresAt: number
}

/** 文件邀请中的单文件元数据 */
export interface FileMeta {
  name: string
  size: number
  mime: string
}

/** 点对点私聊消息 */
export interface DMMessage {
  id: string
  fromPeerId: string
  fromName: string
  text: string
  createdAt: number
  outgoing?: boolean
}

/** 服务端握手成功响应载荷 */
export interface WelcomePayload {
  peerId: string
  sessionToken: string
  self: PeerInfo
  iceServers: RTCIceServer[]
  limits: Limits
}

/** 服务端下发空间全量快照载荷 */
export interface SpaceStatePayload {
  space: SpaceSummary
  peers: PeerInfo[]
  timeline: TimelineItem[]
}

/** 转发给对端的 WebRTC 与文件信令事件载荷 */
export interface RTCSignalEventPayload {
  fromPeerId: string
  signalType: SignalType
  transferId: string
  data?: unknown
}
