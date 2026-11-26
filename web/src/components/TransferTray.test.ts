import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TransferTray from './TransferTray.vue'

describe('TransferTray.vue', () => {
  it('shows channel badge and persistent save button on completed incoming transfer', async () => {
    const wrapper = mount(TransferTray, {
      props: {
        isRoom: true,
        autoAcceptInRoom: false,
        transfers: [
          {
            transferId: 'tx-1',
            direction: 'receive',
            peerId: 'p2',
            peerName: 'Phone',
            files: [{ name: 'video.mp4', size: 1024, mime: 'video/mp4' }],
            channel: 'relay',
            state: 'completed',
            currentFileIndex: 0,
            bytesTransferred: 1024,
            totalBytes: 1024,
            speedBps: 0,
            receivedFiles: [
              {
                fileIndex: 0,
                name: 'video.mp4',
                size: 1024,
                mime: 'video/mp4',
                objectUrl: 'blob:saved-1',
                storageMode: 'opfs'
              }
            ]
          }
        ]
      }
    })

    expect(wrapper.text()).toContain('服务器中转')
    expect(wrapper.text()).toContain('OPFS')
    expect(wrapper.find('[data-testid="memory-warning-tx-1-0"]').exists()).toBe(false)
    const saveBtn = wrapper.get('[data-testid="save-file-0"]')
    expect(saveBtn.text()).toContain('保存 / 重新下载')
    await saveBtn.trigger('click')
    expect(wrapper.emitted('download-file')?.[0]).toEqual(['blob:saved-1', 'video.mp4'])
    // 点击保存后按钮持续保留，直到用户主动点击关闭
    expect(wrapper.find('[data-testid="save-file-0"]').exists()).toBe(true)

    await wrapper.get('[data-testid="dismiss-transfer-tx-1"]').trigger('click')
    expect(wrapper.emitted('dismiss')?.[0]).toEqual(['tx-1'])
  })

  it('shows P2P badge, memory storage mode, and memory warning whenever storageMode is memory regardless of size, while omitting it for opfs', async () => {
    const largeBytes = 25 * 1024 * 1024
    const wrapper = mount(TransferTray, {
      props: {
        isRoom: false,
        autoAcceptInRoom: false,
        transfers: [
          {
            transferId: 'tx-opfs-large',
            direction: 'receive',
            peerId: 'p2',
            peerName: 'iPhone',
            files: [{ name: 'archive.zip', size: largeBytes, mime: 'application/zip' }],
            channel: 'webrtc',
            state: 'completed',
            currentFileIndex: 0,
            bytesTransferred: largeBytes,
            totalBytes: largeBytes,
            speedBps: 0,
            receivedFiles: [
              {
                fileIndex: 0,
                name: 'archive.zip',
                size: largeBytes,
                mime: 'application/zip',
                objectUrl: 'blob:opfs-large',
                storageMode: 'opfs'
              }
            ]
          },
          {
            transferId: 'tx-mem-small',
            direction: 'receive',
            peerId: 'p3',
            peerName: 'Pixel',
            files: [{ name: 'note.txt', size: 512, mime: 'text/plain' }],
            channel: 'webrtc',
            state: 'completed',
            currentFileIndex: 0,
            bytesTransferred: 512,
            totalBytes: 512,
            speedBps: 0,
            receivedFiles: [
              {
                fileIndex: 0,
                name: 'note.txt',
                size: 512,
                mime: 'text/plain',
                objectUrl: 'blob:mem-small',
                storageMode: 'memory'
              }
            ]
          }
        ]
      }
    })

    expect(wrapper.text()).toContain('P2P 直连')
    expect(wrapper.text()).toContain('内存')
    expect(wrapper.text()).toContain('OPFS')
    expect(wrapper.find('[data-testid="memory-warning-tx-opfs-large-0"]').exists()).toBe(false)
    const memWarning = wrapper.find('[data-testid="memory-warning-tx-mem-small-0"]')
    expect(memWarning.exists()).toBe(true)
    expect(memWarning.text()).toContain('不支持 OPFS')
  })

  it('supports accept, reject, cancel and autoAcceptInRoom toggle', async () => {
    const wrapper = mount(TransferTray, {
      props: {
        isRoom: true,
        autoAcceptInRoom: false,
        transfers: [
          {
            transferId: 'tx-pending',
            direction: 'receive',
            peerId: 'p2',
            peerName: 'Phone',
            files: [{ name: 'a.bin', size: 100, mime: 'application/octet-stream' }],
            channel: 'webrtc',
            state: 'pending',
            currentFileIndex: 0,
            bytesTransferred: 0,
            totalBytes: 100,
            speedBps: 0,
            receivedFiles: []
          },
          {
            transferId: 'tx-sending',
            direction: 'send',
            peerId: 'p2',
            peerName: 'Phone',
            files: [{ name: 'b.bin', size: 100, mime: 'application/octet-stream' }],
            channel: 'relay',
            state: 'transferring',
            currentFileIndex: 0,
            bytesTransferred: 50,
            totalBytes: 100,
            speedBps: 25,
            receivedFiles: []
          }
        ]
      }
    })

    expect(wrapper.text()).toContain('直连受限，已切换服务器中转')
    expect(wrapper.text()).toContain('50%')
    await wrapper.get('#auto-accept-room-checkbox').setValue(true)
    expect(wrapper.emitted('update:autoAcceptInRoom')?.[0]).toEqual([true])

    const buttons = wrapper.findAll('button')
    await buttons.find((b) => b.text() === '接收')?.trigger('click')
    await buttons.find((b) => b.text() === '拒绝')?.trigger('click')
    await buttons.find((b) => b.text() === '取消')?.trigger('click')
    expect(wrapper.emitted('accept')?.[0]).toEqual(['tx-pending'])
    expect(wrapper.emitted('reject')?.[0]).toEqual(['tx-pending'])
    expect(wrapper.emitted('cancel')?.[0]).toEqual(['tx-sending'])
  })
})
