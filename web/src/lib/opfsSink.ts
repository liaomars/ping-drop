const TEMP_DIR_NAME = 'pingdrop-tmp'

/** OPFS Worker 响应载荷 */
interface WorkerReply {
  id: number
  ok: boolean
  error?: string
  file?: File
  size?: number
}

/** 大文件分片写入接收槽统一抽象 */
export interface ChunkSink {
  readonly storageMode: 'opfs' | 'memory'
  readonly tempName?: string
  write(chunk: Uint8Array): Promise<void>
  finalize(mime: string, expectedSize: number): Promise<string>
  abort(): Promise<void>
}

/** 隐私模式或不支持 OPFS 同步写盘时的纯内存 Blob 降级实现 */
export class MemoryChunkSink implements ChunkSink {
  readonly storageMode = 'memory' as const
  private chunks: Uint8Array[] = []
  private total = 0

  /** 拷贝并暂存单个分片到内存数组 */
  async write(chunk: Uint8Array): Promise<void> {
    const copy = new Uint8Array(chunk.byteLength)
    copy.set(chunk)
    this.chunks.push(copy)
    this.total += copy.byteLength
  }

  /** 校验累计字节数后合并生成本地 ObjectURL */
  async finalize(mime: string, expectedSize: number): Promise<string> {
    if (this.total !== expectedSize) {
      this.chunks = []
      throw new Error(`byte size mismatch: expected ${expectedSize}, got ${this.total}`)
    }
    const blob = new Blob(this.chunks as BlobPart[], { type: mime || 'application/octet-stream' })
    this.chunks = []
    return URL.createObjectURL(blob)
  }

  /** 中止接收并释放已缓存的分片 */
  async abort(): Promise<void> {
    this.chunks = []
    this.total = 0
  }
}

/** 基于 Web Worker + OPFS FileSystemSyncAccessHandle 的零 OOM 流式落盘接收器 */
export class OpfsWorkerSink implements ChunkSink {
  readonly storageMode = 'opfs' as const
  readonly tempName: string
  private readonly worker: Worker
  private seq = 0
  private total = 0
  private closed = false
  private pending = new Map<number, { resolve: (val: { file?: File; size?: number }) => void; reject: (err: Error) => void }>()

  private constructor(worker: Worker, tempName: string) {
    this.worker = worker
    this.tempName = tempName
    this.worker.onmessage = (ev: MessageEvent<WorkerReply>) => {
      const entry = this.pending.get(ev.data.id)
      if (!entry) return
      this.pending.delete(ev.data.id)
      if (ev.data.ok) {
        entry.resolve({ file: ev.data.file, size: ev.data.size })
      } else {
        entry.reject(new Error(ev.data.error ?? 'OPFS worker error'))
      }
    }
    this.worker.onerror = (ev: ErrorEvent) => {
      this.closed = true
      const err = new Error(ev.message || 'OPFS worker error')
      for (const entry of this.pending.values()) {
        entry.reject(err)
      }
      this.pending.clear()
    }
  }

  /** 启动 OPFS Worker 并打开临时落盘文件 */
  static async create(tempName: string): Promise<OpfsWorkerSink> {
    const worker = new Worker(new URL('./opfsWorker.ts', import.meta.url), { type: 'module' })
    const sink = new OpfsWorkerSink(worker, tempName)
    try {
      await sink.call({ cmd: 'open', tempName })
      return sink
    } catch (err) {
      sink.terminateWorker()
      throw err
    }
  }

  /** 将分片通过 Transferable ArrayBuffer 零拷贝转发给 Worker 同步落盘 */
  async write(chunk: Uint8Array): Promise<void> {
    const copy = new Uint8Array(chunk.byteLength)
    copy.set(chunk)
    this.total += copy.byteLength
    await this.call({ cmd: 'write', chunk: copy.buffer }, [copy.buffer])
  }

  /** 刷盘关闭句柄、校验最终字节数并返回带 MIME 的本地 ObjectURL */
  async finalize(mime: string, expectedSize: number): Promise<string> {
    try {
      const res = await this.call({ cmd: 'finalize' })
      if ((res.size ?? this.total) !== expectedSize || !res.file) {
        await removeOpfsTempEntry(this.tempName)
        throw new Error(`byte size mismatch: expected ${expectedSize}, got ${res.size ?? this.total}`)
      }
      const typedBlob = res.file.slice(0, res.file.size, mime || 'application/octet-stream')
      return URL.createObjectURL(typedBlob)
    } finally {
      this.terminateWorker()
    }
  }

  /** 中止落盘、终止 Worker 并清理 OPFS 临时文件 */
  async abort(): Promise<void> {
    try {
      if (!this.closed) {
        await this.call({ cmd: 'abort' })
      }
    } finally {
      this.terminateWorker()
      await removeOpfsTempEntry(this.tempName)
    }
  }

  private terminateWorker(): void {
    this.closed = true
    this.worker.terminate()
  }

  private call(payload: Record<string, unknown>, transfer: Transferable[] = []): Promise<{ file?: File; size?: number }> {
    if (this.closed) {
      return Promise.reject(new Error('OPFS worker terminated'))
    }
    const id = ++this.seq
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      try {
        this.worker.postMessage({ id, ...payload }, transfer)
      } catch (err) {
        this.pending.delete(id)
        reject(err instanceof Error ? err : new Error(String(err)))
      }
    })
  }
}

/** 清理上次异常退出或未关闭标签页遗留的 OPFS 临时目录 */
export async function cleanupStaleOpfsTempFiles(): Promise<void> {
  try {
    const root = await navigator.storage?.getDirectory?.()
    if (root) {
      await root.removeEntry(TEMP_DIR_NAME, { recursive: true }).catch(() => undefined)
    }
  } catch {
    // 忽略无痕模式或不支持 OPFS 的环境
  }
}

/** 接收前校验浏览器可用存储配额 */
export async function checkStorageQuota(requiredBytes: number): Promise<{ ok: boolean; availableBytes: number | null }> {
  try {
    if (!navigator.storage?.estimate) {
      return { ok: true, availableBytes: null }
    }
    const est = await navigator.storage.estimate()
    if (typeof est.quota === 'number' && typeof est.usage === 'number') {
      const avail = Math.max(0, est.quota - est.usage)
      return { ok: avail >= requiredBytes, availableBytes: avail }
    }
  } catch {
    // 忽略配额查询失败
  }
  return { ok: true, availableBytes: null }
}

/** 创建优先 OPFS Worker、隐私模式或无支持时降级 MemoryChunkSink 的接收槽 */
export async function createChunkSink(
  transferId: string,
  fileIndex: number,
  fileName: string,
  opts: { forceMemory?: boolean } = {}
): Promise<ChunkSink> {
  if (!opts.forceMemory && typeof Worker !== 'undefined' && typeof navigator !== 'undefined' && Boolean(navigator.storage?.getDirectory)) {
    try {
      const safeTransferId = transferId.replace(/[^a-zA-Z0-9._-]/g, '_')
      const safeFileName = fileName.replace(/[^a-zA-Z0-9._-]/g, '_')
      const safeName = `${safeTransferId}-${fileIndex}-${safeFileName}`
      return await OpfsWorkerSink.create(safeName)
    } catch {
      // OPFS 在无痕/受限环境抛错时降级回内存 Blob
    }
  }
  return new MemoryChunkSink()
}

/** 通过临时 <a download> 触发浏览器保存文件 */
export function triggerBrowserDownload(objectUrl: string, fileName: string): void {
  const a = document.createElement('a')
  a.href = objectUrl
  a.download = fileName
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

/** 删除指定的 OPFS 临时落盘文件（静默忽略无痕模式或不存在的情况） */
export async function removeOpfsTempEntry(tempName: string): Promise<void> {
  if (!tempName) return
  try {
    const root = await navigator.storage?.getDirectory?.()
    if (!root) return
    const dir = await root.getDirectoryHandle(TEMP_DIR_NAME, { create: false })
    await dir.removeEntry(tempName).catch(() => undefined)
  } catch {
    // 忽略
  }
}
