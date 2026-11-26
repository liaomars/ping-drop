import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createSessionStore } from './session'

const mockCheckStorageQuota = vi.fn()

vi.mock('../lib/opfsSink', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/opfsSink')>()
  return {
    ...actual,
    checkStorageQuota: (...args: Parameters<typeof actual.checkStorageQuota>) => mockCheckStorageQuota(...args)
  }
})

describe('session store', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
    let seq = 0
    URL.createObjectURL = vi.fn(() => `blob:preview-${++seq}`)
    URL.revokeObjectURL = vi.fn()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    mockCheckStorageQuota.mockReset()
    mockCheckStorageQuota.mockResolvedValue({ ok: true, availableBytes: 100_000_000 })
  })

  it('handles welcome, space.state, room URL sync and idempotent timeline deduplication', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Blob(['img']), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    expect(store.state.sessionToken).toBe('tok-self')
    expect(store.state.limits.maxRelayMB).toBe(100)

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    expect(window.location.search).toBe('?room=482917')
    expect(store.state.peers).toHaveLength(2)

    const imgItem = {
      id: 'item-1',
      kind: 'image',
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-1',
      fileName: 'shot.png',
      mime: 'image/png',
      size: 3,
      createdAt: 1000,
      expiresAt: 2000
    }
    store.handleServerEvent('timeline.item', { item: imgItem })
    store.handleServerEvent('timeline.item', { item: imgItem }) // 幂等去重
    expect(store.state.timeline).toHaveLength(1)

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-1']).toBe('blob:preview-1')
    })

    store.handleServerEvent('timeline.deleted', { itemId: 'item-1' })
    expect(store.state.timeline).toHaveLength(0)
    expect(store.state.blobPreviewUrls['blob-1']).toBeUndefined()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.destroy()
  })

  it('revokes and clears ObjectURLs on timeline.cleared, space.state switch, and destroy', async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response(new Blob(['img']), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [],
      timeline: [
        {
          id: 'item-1',
          kind: 'image',
          senderId: 'p-self',
          senderName: 'Mac Chrome',
          blobId: 'blob-1',
          fileName: 'a.png',
          mime: 'image/png',
          size: 3,
          createdAt: 1000,
          expiresAt: 2000
        }
      ]
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-1']).toBe('blob:preview-1')
    })

    store.handleServerEvent('timeline.cleared', {})
    expect(store.state.timeline).toHaveLength(0)
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.handleServerEvent('timeline.item', {
      item: {
        id: 'item-2',
        kind: 'image',
        senderId: 'p-self',
        senderName: 'Mac Chrome',
        blobId: 'blob-2',
        fileName: 'b.png',
        mime: 'image/png',
        size: 3,
        createdAt: 1001,
        expiresAt: 2001
      }
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-2']).toBe('blob:preview-2')
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [],
      timeline: []
    })
    expect(window.location.search).toBe('')
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-2')

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '123456', sharedTimelineEnabled: true },
      peers: [],
      timeline: [
        {
          id: 'item-3',
          kind: 'image',
          senderId: 'p-self',
          senderName: 'Mac Chrome',
          blobId: 'blob-3',
          fileName: 'c.png',
          mime: 'image/png',
          size: 3,
          createdAt: 1002,
          expiresAt: 2002
        }
      ]
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-3']).toBe('blob:preview-3')
    })

    store.destroy()
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-3')
  })

  it('checks storage quota and warns via toast before accepting incoming transfer, and revokes received URL on dismissTransfer', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-1',
      data: {
        files: [{ name: 'big.bin', size: 3, mime: 'application/octet-stream' }]
      }
    })

    expect(store.state.transfers).toHaveLength(1)
    expect(store.state.transfers[0].state).toBe('pending')

    mockCheckStorageQuota.mockResolvedValueOnce({ ok: false, availableBytes: 100 })
    await store.acceptIncoming('tx-1')

    expect(mockCheckStorageQuota).toHaveBeenCalledWith(3)
    expect(store.state.toast).not.toBeNull()
    expect(store.state.toast?.kind).toBe('error')
    expect(store.state.toast?.message).toContain('存储空间不足')
    expect(store.state.transfers[0].state).toBe('connecting')

    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'relay.start',
      transferId: 'tx-1',
      data: {}
    })

    await vi.waitFor(() => {
      expect(store.state.transfers[0].state).toBe('completed')
    })

    const receivedUrl = store.state.transfers[0].receivedFiles[0]?.objectUrl
    expect(receivedUrl).toBe('blob:preview-1')

    store.dismissTransfer('tx-1')
    expect(store.state.transfers).toHaveLength(0)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.destroy()
  })

  it('handles peer lifecycle, dm.text, ROOM_NOT_FOUND URL reset, and uploadTimelineFile guards', async () => {
    const uploadedItem = {
      id: 'item-up-1',
      kind: 'file',
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-up-1',
      fileName: 'doc.txt',
      mime: 'text/plain',
      size: 5,
      createdAt: 1000,
      expiresAt: 2000
    }
    const fetcher = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ item: uploadedItem }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      })
    )
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 1, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '654321', sharedTimelineEnabled: true },
      peers: [{ id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }],
      timeline: []
    })
    expect(window.location.search).toBe('?room=654321')

    const otherPeer = { id: 'p-2', name: 'Pixel', deviceType: 'mobile', os: 'Android', browser: 'Chrome' }
    store.handleServerEvent('peer.joined', { peer: otherPeer })
    store.selectPeer('p-2')
    expect(store.state.peers).toHaveLength(2)
    expect(store.state.selectedPeerId).toBe('p-2')

    store.handleServerEvent('peer.updated', { peer: { ...otherPeer, name: 'Pixel 9' } })
    expect(store.state.peers.find((p) => p.id === 'p-2')?.name).toBe('Pixel 9')

    store.handleServerEvent('dm.text', {
      id: 'dm-1',
      fromPeerId: 'p-2',
      fromName: 'Pixel 9',
      text: 'hi',
      createdAt: 1234
    })
    expect(store.state.dmsByPeer['p-2']).toHaveLength(1)

    store.handleServerEvent('peer.left', { peerId: 'p-2' })
    expect(store.state.selectedPeerId).toBeNull()
    expect(store.state.peers).toHaveLength(1)

    const hugeFile = new File([new Uint8Array(2 * 1024 * 1024)], 'huge.bin', { type: 'application/octet-stream' })
    expect(await store.uploadTimelineFile(hugeFile)).toBe(false)
    expect(store.state.toast?.kind).toBe('error')

    const okFile = new File(['hello'], 'doc.txt', { type: 'text/plain' })
    expect(await store.uploadTimelineFile(okFile)).toBe(true)
    expect(store.state.timeline).toHaveLength(1)
    expect(store.state.toast?.kind).toBe('success')

    store.handleServerEvent('error', { code: 'ROOM_NOT_FOUND', message: '房间不存在' })
    expect(window.location.search).toBe('')
    expect(store.state.toast?.kind).toBe('error')

    store.destroy()
  })
})
