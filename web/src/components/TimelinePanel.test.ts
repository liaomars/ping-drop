import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TimelinePanel from './TimelinePanel.vue'

describe('TimelinePanel.vue', () => {
  it('renders image preview from authenticated ObjectURL, file card, and requires confirm before clear', async () => {
    const wrapper = mount(TimelinePanel, {
      props: {
        space: { kind: 'room', code: '123456', sharedTimelineEnabled: true },
        maxTimelineFileMB: 20,
        blobPreviewUrls: { b1: 'blob:auth-preview' },
        timeline: [
          {
            id: 't1',
            kind: 'text',
            senderId: 'p1',
            senderName: 'Mac',
            text: 'hello https://example.com',
            createdAt: 1000,
            expiresAt: 2000
          },
          {
            id: 't2',
            kind: 'image',
            senderId: 'p1',
            senderName: 'Mac',
            blobId: 'b1',
            fileName: 'pic.png',
            mime: 'image/png',
            size: 1024,
            createdAt: 1000,
            expiresAt: 2000
          },
          {
            id: 't3',
            kind: 'file',
            senderId: 'p1',
            senderName: 'Mac',
            blobId: 'b2',
            fileName: 'photo.heic',
            mime: 'application/octet-stream',
            size: 2048,
            createdAt: 1000,
            expiresAt: 2000
          }
        ]
      }
    })

    expect(wrapper.get('img').attributes('src')).toBe('blob:auth-preview')
    expect(wrapper.text()).toContain('photo.heic')

    const link = wrapper.get('a')
    expect(link.attributes('href')).toBe('https://example.com/')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(link.attributes('target')).toBe('_blank')

    // 点击清空先弹出二次确认，确认后才触发 clear-timeline
    await wrapper.get('[data-testid="timeline-clear-btn"]').trigger('click')
    expect(wrapper.emitted('clear-timeline')).toBeUndefined()
    await wrapper.get('[data-testid="timeline-clear-confirm"]').trigger('click')
    expect(wrapper.emitted('clear-timeline')).toHaveLength(1)
  })

  it('safely renders javascript: text without creating links and supports disabled room CTA', async () => {
    const wrapper = mount(TimelinePanel, {
      props: {
        space: { kind: 'room', code: '123456', sharedTimelineEnabled: true },
        maxTimelineFileMB: 20,
        blobPreviewUrls: {},
        timeline: [
          {
            id: 't-xss',
            kind: 'text',
            senderId: 'p1',
            senderName: 'Mac',
            text: 'bad javascript:alert(1)',
            createdAt: 1000,
            expiresAt: 2000
          }
        ]
      }
    })

    expect(wrapper.findAll('a')).toHaveLength(0)
    expect(wrapper.text()).toContain('bad javascript:alert(1)')

    await wrapper.get('[data-testid="timeline-copy-btn"]').trigger('click')
    expect(wrapper.emitted('copy-text')?.[0]).toEqual(['bad javascript:alert(1)'])

    await wrapper.setProps({
      space: { kind: 'lan', sharedTimelineEnabled: false }
    })
    await wrapper.get('[data-testid="timeline-enable-room-btn"]').trigger('click')
    expect(wrapper.emitted('create-room')).toHaveLength(1)
  })
})
