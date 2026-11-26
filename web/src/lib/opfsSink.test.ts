import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  createChunkSink,
  checkStorageQuota,
  MemoryChunkSink,
  OpfsWorkerSink,
  cleanupStaleOpfsTempFiles,
  removeOpfsTempEntry,
  triggerBrowserDownload
} from './opfsSink'

interface PostedMessage {
  data: {
    id: number
    cmd: 'open' | 'write' | 'finalize' | 'abort'
    tempName?: string
    chunk?: ArrayBuffer
  }
  transfer?: Transferable[]
}

class MockWorker {
  static instances: MockWorker[] = []
  static nextBehavior: ((worker: MockWorker, msg: PostedMessage['data']) => void) | null = null

  onmessage: ((ev: MessageEvent<{ id: number; ok: boolean; error?: string; file?: File; size?: number }>) => void) | null = null
  onerror: ((ev: ErrorEvent) => void) | null = null
  posted: PostedMessage[] = []
  terminated = false

  constructor(_url: URL, _opts?: WorkerOptions) {
    MockWorker.instances.push(this)
  }

  postMessage(data: PostedMessage['data'], transfer?: Transferable[]): void {
    this.posted.push({ data, transfer })
    if (MockWorker.nextBehavior) {
      MockWorker.nextBehavior(this, data)
    }
  }

  terminate(): void {
    this.terminated = true
  }

  emitMessage(payload: { id: number; ok: boolean; error?: string; file?: File; size?: number }): void {
    this.onmessage?.({ data: payload } as MessageEvent<{ id: number; ok: boolean; error?: string; file?: File; size?: number }>)
  }

  emitError(message: string): void {
    this.onerror?.({ message } as ErrorEvent)
  }
}

describe('opfsSink', () => {
  const originalWorker = globalThis.Worker

  beforeEach(() => {
    vi.restoreAllMocks()
    MockWorker.instances = []
    MockWorker.nextBehavior = null
    URL.createObjectURL = vi.fn(() => 'blob:chunk-url')
  })

  afterEach(() => {
    globalThis.Worker = originalWorker
  })

  it('falls back to MemoryChunkSink when Worker/OPFS is unavailable and verifies size', async () => {
    const sink = await createChunkSink('tx-1', 0, 'demo.bin', { forceMemory: true })
    expect(sink.storageMode).toBe('memory')

    await sink.write(new Uint8Array([1, 2, 3]))
    await sink.write(new Uint8Array([4, 5]))
    const url = await sink.finalize('application/octet-stream', 5)
    expect(url).toBe('blob:chunk-url')

    const badSink = new MemoryChunkSink()
    await badSink.write(new Uint8Array([1, 2]))
    await expect(badSink.finalize('application/octet-stream', 10)).rejects.toThrow('byte size mismatch')
  })

  it('checks storage quota via navigator.storage.estimate', async () => {
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        estimate: vi.fn().mockResolvedValue({ quota: 1000, usage: 400 }),
        getDirectory: vi.fn().mockResolvedValue({
          removeEntry: vi.fn().mockResolvedValue(undefined)
        })
      }
    })

    expect(await checkStorageQuota(500)).toEqual({ ok: true, availableBytes: 600 })
    expect(await checkStorageQuota(700)).toEqual({ ok: false, availableBytes: 600 })
    await expect(cleanupStaleOpfsTempFiles()).resolves.toBeUndefined()
  })

  it('streams chunks through OpfsWorkerSink and finalizes into ObjectURL', async () => {
    globalThis.Worker = MockWorker as unknown as typeof Worker
    const removeEntry = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        getDirectory: vi.fn().mockResolvedValue({
          getDirectoryHandle: vi.fn().mockResolvedValue({ removeEntry })
        })
      }
    })

    const file = new File([new Uint8Array([1, 2, 3, 4])], 'demo.bin')
    MockWorker.nextBehavior = (worker, msg) => {
      queueMicrotask(() => {
        if (msg.cmd === 'finalize') {
          worker.emitMessage({ id: msg.id, ok: true, file, size: 4 })
        } else {
          worker.emitMessage({ id: msg.id, ok: true })
        }
      })
    }

    const sink = await createChunkSink('tx/1', 2, 'my file?.bin')
    expect(sink.storageMode).toBe('opfs')
    expect(sink.tempName).toBe('tx_1-2-my_file_.bin')
    expect(sink).toBeInstanceOf(OpfsWorkerSink)

    const worker = MockWorker.instances[0]
    expect(worker.posted[0].data).toEqual({ id: 1, cmd: 'open', tempName: 'tx_1-2-my_file_.bin' })

    await sink.write(new Uint8Array([1, 2, 3, 4]))
    expect(worker.posted[1].data.cmd).toBe('write')
    expect(worker.posted[1].transfer).toHaveLength(1)

    const url = await sink.finalize('application/pdf', 4)
    expect(url).toBe('blob:chunk-url')
    expect(worker.terminated).toBe(true)
    expect(removeEntry).not.toHaveBeenCalled()

    await removeOpfsTempEntry(sink.tempName!)
    expect(removeEntry).toHaveBeenCalledWith('tx_1-2-my_file_.bin')
  })

  it('removes OPFS temp entry and rejects when finalized size mismatches', async () => {
    globalThis.Worker = MockWorker as unknown as typeof Worker
    const removeEntry = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        getDirectory: vi.fn().mockResolvedValue({
          getDirectoryHandle: vi.fn().mockResolvedValue({ removeEntry })
        })
      }
    })

    const file = new File([new Uint8Array([1, 2])], 'demo.bin')
    MockWorker.nextBehavior = (worker, msg) => {
      queueMicrotask(() => {
        if (msg.cmd === 'finalize') {
          worker.emitMessage({ id: msg.id, ok: true, file, size: 2 })
        } else {
          worker.emitMessage({ id: msg.id, ok: true })
        }
      })
    }

    const sink = await OpfsWorkerSink.create('tx-mismatch')
    await sink.write(new Uint8Array([1, 2]))
    await expect(sink.finalize('application/octet-stream', 10)).rejects.toThrow('byte size mismatch: expected 10, got 2')
    expect(MockWorker.instances[0].terminated).toBe(true)
    expect(removeEntry).toHaveBeenCalledWith('tx-mismatch')
  })

  it('rejects pending promises when worker.onerror fires and falls back on open error', async () => {
    globalThis.Worker = MockWorker as unknown as typeof Worker
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        getDirectory: vi.fn().mockResolvedValue({
          getDirectoryHandle: vi.fn().mockResolvedValue({ removeEntry: vi.fn().mockResolvedValue(undefined) })
        })
      }
    })

    // First test: worker.onerror during write rejects pending promise
    MockWorker.nextBehavior = (worker, msg) => {
      if (msg.cmd === 'open') {
        queueMicrotask(() => worker.emitMessage({ id: msg.id, ok: true }))
      } else if (msg.cmd === 'write') {
        queueMicrotask(() => worker.emitError('worker crashed'))
      }
    }

    const sink = await OpfsWorkerSink.create('tx-crash')
    await expect(sink.write(new Uint8Array([9, 9]))).rejects.toThrow('worker crashed')
    await expect(sink.abort()).resolves.toBeUndefined()
    expect(MockWorker.instances[0].terminated).toBe(true)

    // Second test: worker.onerror during open causes createChunkSink to fall back to MemoryChunkSink
    MockWorker.nextBehavior = (worker) => {
      queueMicrotask(() => worker.emitError('opfs blocked'))
    }
    const fallbackSink = await createChunkSink('tx-2', 0, 'secret.bin')
    expect(fallbackSink.storageMode).toBe('memory')
    expect(MockWorker.instances[1].terminated).toBe(true)
  })

  it('aborts OpfsWorkerSink cleanly and cleans up temp entry', async () => {
    globalThis.Worker = MockWorker as unknown as typeof Worker
    const removeEntry = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        getDirectory: vi.fn().mockResolvedValue({
          getDirectoryHandle: vi.fn().mockResolvedValue({ removeEntry })
        })
      }
    })

    MockWorker.nextBehavior = (worker, msg) => {
      queueMicrotask(() => worker.emitMessage({ id: msg.id, ok: true }))
    }

    const sink = await OpfsWorkerSink.create('tx-abort')
    await sink.abort()
    const worker = MockWorker.instances[0]
    expect(worker.posted.map((p) => p.data.cmd)).toEqual(['open', 'abort'])
    expect(worker.terminated).toBe(true)
    expect(removeEntry).toHaveBeenCalledWith('tx-abort')
  })

  it('triggers browser download via temporary anchor element', () => {
    const clickSpy = vi.fn()
    const appendSpy = vi.spyOn(document.body, 'appendChild')
    const removeSpy = vi.spyOn(document.body, 'removeChild')
    const origCreateElement = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tagName: string) => {
      const el = origCreateElement(tagName)
      if (tagName === 'a') {
        el.click = clickSpy
      }
      return el
    })

    triggerBrowserDownload('blob:demo', 'report.pdf')
    expect(clickSpy).toHaveBeenCalledTimes(1)
    expect(appendSpy).toHaveBeenCalledTimes(1)
    expect(removeSpy).toHaveBeenCalledTimes(1)
    const anchor = appendSpy.mock.calls[0][0] as HTMLAnchorElement
    expect(anchor.getAttribute('href')).toBe('blob:demo')
    expect(anchor.getAttribute('download')).toBe('report.pdf')
  })
})
