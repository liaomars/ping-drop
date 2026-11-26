import { describe, it, expect, vi, beforeEach } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import App from './App.vue'
import type { SessionState } from './stores/session'

interface MockStore {
  state: SessionState
  start: ReturnType<typeof vi.fn>
  destroy: ReturnType<typeof vi.fn>
  updateNickname: ReturnType<typeof vi.fn>
  createRoom: ReturnType<typeof vi.fn>
  joinRoom: ReturnType<typeof vi.fn>
  returnToLan: ReturnType<typeof vi.fn>
  postTimelineText: ReturnType<typeof vi.fn>
  uploadTimelineFile: ReturnType<typeof vi.fn>
  deleteTimelineItem: ReturnType<typeof vi.fn>
  clearTimeline: ReturnType<typeof vi.fn>
  downloadTimelineBlob: ReturnType<typeof vi.fn>
  sendDM: ReturnType<typeof vi.fn>
  sendFiles: ReturnType<typeof vi.fn>
  acceptTransfer: ReturnType<typeof vi.fn>
  rejectTransfer: ReturnType<typeof vi.fn>
  cancelTransfer: ReturnType<typeof vi.fn>
  dismissTransfer: ReturnType<typeof vi.fn>
  triggerSavedFileDownload: ReturnType<typeof vi.fn>
  selectPeer: ReturnType<typeof vi.fn>
  showToast: ReturnType<typeof vi.fn>
}

let activeStore: MockStore

function makeStore(): MockStore {
  const state = reactive<SessionState>({
    wsStatus: 'open',
    self: { id: 'p-self', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
    sessionToken: 'tok-1',
    iceServers: [],
    limits: { maxTimelineFileMB: 20, maxRelayMB: 100 },
    space: { kind: 'lan', sharedTimelineEnabled: false },
    peers: [
      { id: 'p-self', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      { id: 'p-peer', name: 'Phone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
    ],
    timeline: [],
    blobPreviewUrls: {},
    dmsByPeer: {},
    transfers: [],
    selectedPeerId: null,
    autoAcceptInRoom: false,
    roomModalOpen: false,
    toast: null
  })

  return {
    state,
    start: vi.fn(),
    destroy: vi.fn(),
    updateNickname: vi.fn(),
    createRoom: vi.fn(),
    joinRoom: vi.fn(),
    returnToLan: vi.fn(),
    postTimelineText: vi.fn(() => true),
    uploadTimelineFile: vi.fn(async () => true),
    deleteTimelineItem: vi.fn(),
    clearTimeline: vi.fn(),
    downloadTimelineBlob: vi.fn(),
    sendDM: vi.fn(() => true),
    sendFiles: vi.fn(),
    acceptTransfer: vi.fn(),
    rejectTransfer: vi.fn(),
    cancelTransfer: vi.fn(),
    dismissTransfer: vi.fn(),
    triggerSavedFileDownload: vi.fn(),
    selectPeer: vi.fn((id: string | null) => {
      state.selectedPeerId = id
    }),
    showToast: vi.fn()
  }
}

vi.mock('./stores/session', () => ({
  createSessionStore: () => activeStore
}))

vi.mock('qrcode', () => ({
  default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,stub') }
}))

function makeDropPayload(files: File[]): { dataTransfer: { files: File[] } } {
  return { dataTransfer: { files } }
}

describe('App.vue', () => {
  beforeEach(() => {
    activeStore = makeStore()
  })

  it('handles global drop fallback and prevents double-drop from targeted panels', async () => {
    const wrapper = mount(App)
    const root = wrapper.get('[data-testid="app-root"]')
    const smallFile = new File(['abc'], 'small.txt', { type: 'text/plain' })
    const bigFile = new File([new Uint8Array(21 * 1024 * 1024)], 'big.zip', { type: 'application/zip' })

    // 1. 无选中设备且共享时间线关闭时，全局拖拽提示选择设备或开启 6 位房间
    await root.trigger('drop', makeDropPayload([smallFile]))
    expect(activeStore.sendFiles).not.toHaveBeenCalled()
    expect(activeStore.uploadTimelineFile).not.toHaveBeenCalled()
    expect(activeStore.showToast).toHaveBeenCalledTimes(1)

    // 2. 无选中设备且共享时间线开启时：<=20MB 上传时间线，>20MB 提示选择在线设备直传
    activeStore.showToast.mockClear()
    activeStore.state.space = { kind: 'room', code: '123456', sharedTimelineEnabled: true }
    await wrapper.vm.$nextTick()

    await root.trigger('drop', makeDropPayload([smallFile]))
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledTimes(1)
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledWith(smallFile)

    await root.trigger('drop', makeDropPayload([bigFile]))
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledTimes(1)
    expect(activeStore.showToast).toHaveBeenCalledTimes(1)

    // 3. 拖放到 TimelinePanel 时应 stopPropagation，避免触发 root 二次处理
    activeStore.uploadTimelineFile.mockClear()
    await wrapper.get('[data-testid="timeline-panel"]').trigger('drop', makeDropPayload([smallFile]))
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledTimes(1)

    // 4. 拖放到 DeviceRadar 设备卡片时只调用一次 sendFiles
    await wrapper.get('[data-testid="peer-card-p-peer"]').trigger('drop', makeDropPayload([bigFile]))
    expect(activeStore.sendFiles).toHaveBeenCalledTimes(1)
    expect(activeStore.sendFiles).toHaveBeenCalledWith('p-peer', [bigFile])

    // 5. 已选中设备时：全局拖拽直接发给 selectedPeer，且 DirectPanel 拖拽不二次冒泡
    activeStore.sendFiles.mockClear()
    activeStore.state.selectedPeerId = 'p-peer'
    await wrapper.vm.$nextTick()

    await root.trigger('drop', makeDropPayload([bigFile]))
    expect(activeStore.sendFiles).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="direct-panel"]').trigger('drop', makeDropPayload([smallFile]))
    expect(activeStore.sendFiles).toHaveBeenCalledTimes(2)
  })

  it('renders mobile quick input bar ONLY when activeTab === timeline && sharedTimelineEnabled', async () => {
    const wrapper = mount(App)

    // 初始 activeTab=radar 且 sharedTimelineEnabled=false
    expect(wrapper.find('[data-testid="mobile-quick-bar"]').exists()).toBe(false)

    // 切到 timeline 但 sharedTimelineEnabled 仍为 false
    await wrapper.get('[data-testid="tab-timeline"]').trigger('click')
    expect(wrapper.find('[data-testid="mobile-quick-bar"]').exists()).toBe(false)

    // 开启房间时间线后显示
    activeStore.state.space = { kind: 'room', code: '482917', sharedTimelineEnabled: true }
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="mobile-quick-bar"]').exists()).toBe(true)

    const mobileInput = wrapper.get('[data-testid="mobile-quick-input"]')
    await mobileInput.setValue('hi mobile')
    await wrapper.get('[data-testid="mobile-quick-submit"]').trigger('click')
    expect(activeStore.postTimelineText).toHaveBeenCalledWith('hi mobile')

    // 切回 radar Tab 时隐藏
    await wrapper.get('[data-testid="tab-radar"]').trigger('click')
    expect(wrapper.find('[data-testid="mobile-quick-bar"]').exists()).toBe(false)
  })

  it('resets DirectPanel unsent draft when switching selected peer', async () => {
    activeStore.state.peers.push({
      id: 'p-peer-2',
      name: 'iPad',
      deviceType: 'mobile',
      os: 'iPadOS',
      browser: 'Safari'
    })
    activeStore.state.selectedPeerId = 'p-peer'
    const wrapper = mount(App)

    const dmInput = wrapper.get('#dm-text-input')
    await dmInput.setValue('unsent draft')
    expect((dmInput.element as HTMLInputElement).value).toBe('unsent draft')

    activeStore.state.selectedPeerId = 'p-peer-2'
    await wrapper.vm.$nextTick()

    expect(wrapper.get('[data-testid="direct-panel"]').text()).toContain('iPad')
    expect((wrapper.get('#dm-text-input').element as HTMLInputElement).value).toBe('')
  })

  it('awaits eligible timeline uploads before showing oversized file toast on mixed global drop', async () => {
    activeStore.state.space = { kind: 'room', code: '123456', sharedTimelineEnabled: true }
    let resolveUpload!: (ok: boolean) => void
    activeStore.uploadTimelineFile.mockImplementation(
      () =>
        new Promise<boolean>((resolve) => {
          resolveUpload = (ok: boolean) => {
            activeStore.showToast('已添加到共享时间线', 'success')
            resolve(ok)
          }
        })
    )

    const wrapper = mount(App)
    const root = wrapper.get('[data-testid="app-root"]')
    const smallFile = new File(['abc'], 'small.txt', { type: 'text/plain' })
    const bigFile = new File([new Uint8Array(21 * 1024 * 1024)], 'big.zip', { type: 'application/zip' })

    await root.trigger('drop', makeDropPayload([smallFile, bigFile]))
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledTimes(1)
    expect(activeStore.uploadTimelineFile).toHaveBeenCalledWith(smallFile)
    expect(activeStore.showToast).not.toHaveBeenCalled()

    resolveUpload(true)
    await flushPromises()

    expect(activeStore.showToast).toHaveBeenCalledTimes(2)
    expect(activeStore.showToast.mock.calls[0]).toEqual(['已添加到共享时间线', 'success'])
    expect(activeStore.showToast.mock.calls[1]).toEqual([
      expect.stringContaining('超出时间线'),
      'error'
    ])
  })
})
