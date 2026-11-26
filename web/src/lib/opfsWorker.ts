/** OPFS 同步写入句柄最小接口，兼容各浏览器 Worker 上下文声明差异 */
interface SyncAccessHandleLike {
  write(buffer: Uint8Array, options?: { at?: number }): number
  flush(): void
  close(): void
  getSize(): number
}

/** 主线程发往 OPFS Worker 的指令载荷 */
interface WorkerRequest {
  id: number
  cmd: 'open' | 'write' | 'finalize' | 'abort'
  tempName?: string
  chunk?: ArrayBuffer
}

/** OPFS Worker 返回主线程的响应载荷 */
interface WorkerResponse {
  id: number
  ok: boolean
  error?: string
  file?: File
  size?: number
}

/** DedicatedWorkerGlobalScope 最小类型视图，规避 DOM 与 WebWorker lib 并存时的重载冲突 */
interface WorkerScopeLike {
  onmessage: ((ev: MessageEvent<WorkerRequest>) => void | Promise<void>) | null
  postMessage(message: WorkerResponse, transfer?: Transferable[]): void
}

const TEMP_DIR_NAME = 'pingdrop-tmp'
const workerSelf = self as unknown as WorkerScopeLike

let handle: SyncAccessHandleLike | null = null
let activeName = ''
let offset = 0

async function getTempDir(): Promise<FileSystemDirectoryHandle> {
  const root = await navigator.storage.getDirectory()
  return root.getDirectoryHandle(TEMP_DIR_NAME, { create: true })
}

workerSelf.onmessage = async (ev: MessageEvent<WorkerRequest>) => {
  const msg = ev.data
  try {
    if (msg.cmd === 'open') {
      const dir = await getTempDir()
      activeName = msg.tempName ?? `part-${Date.now()}`
      const fileHandle = await dir.getFileHandle(activeName, { create: true })
      const withSync = fileHandle as unknown as {
        createSyncAccessHandle?: () => Promise<SyncAccessHandleLike>
      }
      if (typeof withSync.createSyncAccessHandle !== 'function') {
        throw new Error('OPFS createSyncAccessHandle unsupported')
      }
      handle = await withSync.createSyncAccessHandle()
      offset = 0
      workerSelf.postMessage({ id: msg.id, ok: true })
      return
    }

    if (msg.cmd === 'write') {
      if (!handle || !msg.chunk) {
        throw new Error('OPFS handle not opened')
      }
      const view = new Uint8Array(msg.chunk)
      const written = handle.write(view, { at: offset })
      offset += written
      workerSelf.postMessage({ id: msg.id, ok: true })
      return
    }

    if (msg.cmd === 'finalize') {
      if (!handle) {
        throw new Error('OPFS handle not opened')
      }
      handle.flush()
      const finalSize = handle.getSize()
      handle.close()
      handle = null
      const dir = await getTempDir()
      const fileHandle = await dir.getFileHandle(activeName)
      const file = await fileHandle.getFile()
      workerSelf.postMessage({ id: msg.id, ok: true, file, size: finalSize })
      return
    }

    if (msg.cmd === 'abort') {
      if (handle) {
        handle.close()
        handle = null
      }
      if (activeName) {
        const dir = await getTempDir()
        await dir.removeEntry(activeName).catch(() => undefined)
      }
      workerSelf.postMessage({ id: msg.id, ok: true })
    }
  } catch (err) {
    workerSelf.postMessage({
      id: msg.id,
      ok: false,
      error: err instanceof Error ? err.message : String(err)
    })
  }
}
