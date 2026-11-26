import { describe, it, expect, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import RoomBar from './RoomBar.vue'

const toDataURLMock = vi.hoisted(() =>
  vi
    .fn<(text: string, opts?: { width?: number; margin?: number }) => Promise<string>>()
    .mockResolvedValue('data:image/png;base64,stub')
)

vi.mock('qrcode', () => ({
  default: { toDataURL: toDataURLMock }
}))

describe('RoomBar.vue', () => {
  it('formats 6-digit code as XXX XXX and sanitizes non-digit join input', async () => {
    const wrapper = mount(RoomBar, {
      props: {
        self: { id: 'p1', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
        wsStatus: 'open',
        roomModalOpen: true
      }
    })

    expect(wrapper.text()).toContain('482 917')
    const input = wrapper.get('[data-testid="room-code-input"]')
    await input.setValue('12a 34b 56')
    await wrapper.get('[data-testid="room-join-submit"]').trigger('click')
    expect(wrapper.emitted('join-room')?.[0]).toEqual(['123456'])
  })

  it('disables join submit for incomplete codes and emits room/nickname/copy actions', async () => {
    const wrapper = mount(RoomBar, {
      props: {
        self: { id: 'p1', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
        wsStatus: 'open',
        roomModalOpen: true
      }
    })

    const input = wrapper.get('[data-testid="room-code-input"]')
    const submitBtn = wrapper.get('[data-testid="room-join-submit"]')
    await input.setValue('12a34')
    expect((submitBtn.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.get('[data-testid="copy-invite-btn"]').trigger('click')
    expect(wrapper.emitted('copy-text')?.[0]).toEqual([`${window.location.origin}/?room=482917`])

    await wrapper.get('[data-testid="return-lan-btn"]').trigger('click')
    expect(wrapper.emitted('return-lan')).toHaveLength(1)

    await wrapper.get('[data-testid="create-room-btn"]').trigger('click')
    expect(wrapper.emitted('create-room')).toHaveLength(1)

    // 昵称编辑支持 IME 组合态防误触
    await wrapper.get('[data-testid="edit-name-btn"]').trigger('click')
    const nameInput = wrapper.get('[data-testid="name-edit-input"]')
    await nameInput.setValue('NewMac')
    await nameInput.trigger('keydown', { key: 'Enter', isComposing: true })
    expect(wrapper.emitted('update:nickname')).toBeUndefined()

    await nameInput.trigger('keydown', { key: 'Enter', isComposing: false })
    expect(wrapper.emitted('update:nickname')?.[0]).toEqual(['NewMac'])
  })

  it('clears stale QR image immediately when space.code changes before new QR resolves', async () => {
    toDataURLMock.mockResolvedValueOnce('data:image/png;base64,first')

    const wrapper = mount(RoomBar, {
      props: {
        self: { id: 'p1', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
        wsStatus: 'open',
        roomModalOpen: true
      }
    })

    await flushPromises()
    expect(wrapper.get('img[alt="配对二维码"]').attributes('src')).toBe('data:image/png;base64,first')

    let resolveSecond!: (url: string) => void
    toDataURLMock.mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          resolveSecond = resolve
        })
    )

    await wrapper.setProps({
      space: { kind: 'room', code: '999888', sharedTimelineEnabled: true }
    })

    expect(wrapper.find('img[alt="配对二维码"]').exists()).toBe(false)

    resolveSecond('data:image/png;base64,second')
    await flushPromises()
    expect(wrapper.get('img[alt="配对二维码"]').attributes('src')).toBe('data:image/png;base64,second')
  })
})
