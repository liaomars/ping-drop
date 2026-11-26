const NICKNAME_STORAGE_KEY = 'pingdrop.nickname'

/** 安全的文本渲染分段结构 */
export type TextSegment =
  | { kind: 'text'; text: string }
  | { kind: 'link'; text: string; href: string }

/** 从 localStorage 读取持久化的本机昵称 */
export function loadNickname(): string {
  try {
    return localStorage.getItem(NICKNAME_STORAGE_KEY)?.trim() ?? ''
  } catch {
    return ''
  }
}

/** 保存本机昵称到 localStorage */
export function saveNickname(name: string): void {
  try {
    localStorage.setItem(NICKNAME_STORAGE_KEY, name.trim().slice(0, 32))
  } catch {
    // 忽略无痕模式存储异常
  }
}

/** 检测当前浏览器的设备类型、操作系统与浏览器名称 */
export function detectDeviceProfile(): { deviceType: string; os: string; browser: string } {
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''
  const isMobile = /Android|iPhone|iPad|iPod|Mobile/i.test(ua)

  let os = 'Desktop'
  if (/iPhone|iPad|iPod/i.test(ua)) os = 'iOS'
  else if (/Android/i.test(ua)) os = 'Android'
  else if (/Mac OS X|Macintosh/i.test(ua)) os = 'macOS'
  else if (/Windows/i.test(ua)) os = 'Windows'
  else if (/Linux/i.test(ua)) os = 'Linux'

  let browser = 'Browser'
  if (/Edg\//i.test(ua)) browser = 'Edge'
  else if (/Chrome\//i.test(ua)) browser = 'Chrome'
  else if (/Firefox\//i.test(ua)) browser = 'Firefox'
  else if (/Safari\//i.test(ua)) browser = 'Safari'

  return { deviceType: isMobile ? 'mobile' : 'desktop', os, browser }
}

/** 清洗用户输入中的非数字字符并截断至 6 位 */
export function normalizeRoomCode(raw: string): string {
  return raw.replace(/\D+/g, '').slice(0, 6)
}

/** 判断是否为合法 6 位数字配对码 */
export function isValidRoomCode(code: string): boolean {
  return /^\d{6}$/.test(code)
}

/** 将 6 位配对码格式化为 3+3 分组展示 */
export function formatRoomCode(code: string): string {
  const clean = normalizeRoomCode(code)
  return clean.length === 6 ? `${clean.slice(0, 3)} ${clean.slice(3)}` : clean
}

/** 从 URL query 读取 ?room=XXXXXX */
export function readRoomCodeFromLocation(
  search = typeof window !== 'undefined' ? window.location.search : ''
): string | null {
  const params = new URLSearchParams(search)
  const room = normalizeRoomCode(params.get('room') ?? '')
  return isValidRoomCode(room) ? room : null
}

/** 使用 history.replaceState 将房间号同步至地址栏，防止刷新掉出房间 */
export function syncRoomUrl(code: string | null): void {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  if (code && isValidRoomCode(code)) {
    url.searchParams.set('room', code)
  } else {
    url.searchParams.delete('room')
  }
  window.history.replaceState({}, '', url.pathname + url.search + url.hash)
}

/** 格式化文件大小 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let idx = 0
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024
    idx += 1
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[idx]}`
}

/** 纯函数拆分文本与安全 http(s) 链接节点，拒绝 javascript:/data: 等危险协议 */
export function parseTextSegments(text: string): TextSegment[] {
  const urlRe = /https?:\/\/[^\s<>"'`]+/gi
  const segments: TextSegment[] = []
  let lastIndex = 0

  for (const match of text.matchAll(urlRe)) {
    const idx = match.index ?? 0
    if (idx > lastIndex) {
      segments.push({ kind: 'text', text: text.slice(lastIndex, idx) })
    }
    const rawUrl = match[0]
    try {
      const parsed = new URL(rawUrl)
      if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
        segments.push({ kind: 'link', text: rawUrl, href: parsed.href })
      } else {
        segments.push({ kind: 'text', text: rawUrl })
      }
    } catch {
      segments.push({ kind: 'text', text: rawUrl })
    }
    lastIndex = idx + rawUrl.length
  }

  if (lastIndex < text.length) {
    segments.push({ kind: 'text', text: text.slice(lastIndex) })
  }
  return segments
}

/** 复制文本至系统剪贴板，受限环境下自动降级为 textarea + execCommand */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 继续尝试降级方案
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
