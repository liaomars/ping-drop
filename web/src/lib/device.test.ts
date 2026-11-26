import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  loadNickname,
  saveNickname,
  detectDeviceProfile,
  normalizeRoomCode,
  isValidRoomCode,
  formatRoomCode,
  readRoomCodeFromLocation,
  parseTextSegments,
  syncRoomUrl,
  formatBytes,
  copyText
} from './device'

describe('device utils', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
    localStorage.clear()
  })

  it('normalizes, validates and formats 6-digit room codes', () => {
    expect(normalizeRoomCode('482 917abc')).toBe('482917')
    expect(isValidRoomCode('482917')).toBe(true)
    expect(isValidRoomCode('12345')).toBe(false)
    expect(formatRoomCode('482917')).toBe('482 917')
  })

  it('synchronizes and reads ?room=XXXXXX from address bar', () => {
    syncRoomUrl('482917')
    expect(window.location.search).toBe('?room=482917')
    expect(readRoomCodeFromLocation()).toBe('482917')
    syncRoomUrl(null)
    expect(window.location.search).toBe('')
    expect(readRoomCodeFromLocation()).toBeNull()
  })

  it('persists nickname and detects device profile', () => {
    saveNickname('  MacBook Pro  ')
    expect(loadNickname()).toBe('MacBook Pro')
    const profile = detectDeviceProfile()
    expect(['desktop', 'mobile']).toContain(profile.deviceType)
    expect(profile.os.length).toBeGreaterThan(0)
    expect(profile.browser.length).toBeGreaterThan(0)
  })

  it('formats byte sizes across units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(10 * 1024 * 1024)).toBe('10.0 MB')
  })

  it('splits safe http/https links and neutralizes javascript: schemes', () => {
    const segs = parseTextSegments('看下 https://example.com/a?b=1 和 javascript:alert(1) 谢谢')
    expect(segs).toEqual([
      { kind: 'text', text: '看下 ' },
      { kind: 'link', text: 'https://example.com/a?b=1', href: 'https://example.com/a?b=1' },
      { kind: 'text', text: ' 和 javascript:alert(1) 谢谢' }
    ])
  })

  it('falls back to execCommand when navigator.clipboard fails', async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }
    })
    const execMock = vi.fn().mockReturnValue(true)
    document.execCommand = execMock
    expect(await copyText('hello')).toBe(true)
    expect(execMock).toHaveBeenCalledWith('copy')

    // execCommand 抛异常时仍应通过 finally 清理临时 textarea
    document.execCommand = vi.fn(() => {
      throw new Error('copy blocked')
    })
    expect(await copyText('boom')).toBe(false)
    expect(document.body.querySelectorAll('textarea')).toHaveLength(0)
  })
})
