/**
 * 鉴权时间线 Blob URL 缓存管理器：
 * 通过 Bearer sessionToken 拉取受空间隔离保护的图片与文件并管理本地 ObjectURL 生命周期。
 */
export class BlobUrlCache {
  private readonly cache = new Map<string, string>()
  private readonly inflight = new Map<string, Promise<string>>()
  private readonly revokedEpochs = new Map<string, number>()
  private readonly fetcher: typeof fetch
  private epoch = 0

  /** 创建 BlobUrlCache 实例，可注入自定义 fetch 以便测试 */
  constructor(fetcher: typeof fetch = fetch.bind(window)) {
    this.fetcher = fetcher
  }

  /** 拉取或命中缓存获取指定 blobId 的本地 ObjectURL */
  async fetchObjectUrl(blobId: string, sessionToken: string, download = false): Promise<string> {
    const key = download ? `${blobId}:dl` : blobId
    const existing = this.cache.get(key)
    if (existing) {
      return existing
    }
    const pending = this.inflight.get(key)
    if (pending) {
      return pending
    }

    const reqEpoch = this.epoch
    const keyRevokeGen = this.revokedEpochs.get(blobId) ?? 0
    const isInvalidated = (): boolean =>
      this.epoch !== reqEpoch || (this.revokedEpochs.get(blobId) ?? 0) !== keyRevokeGen

    const req = (async () => {
      const qs = download ? '?download=1' : ''
      const res = await this.fetcher(`/api/blobs/${encodeURIComponent(blobId)}${qs}`, {
        headers: { Authorization: `Bearer ${sessionToken}` }
      })
      if (!res.ok) {
        throw new Error(`blob fetch failed with ${res.status}`)
      }
      const blob = await res.blob()
      const objectUrl = URL.createObjectURL(blob)
      if (isInvalidated()) {
        URL.revokeObjectURL(objectUrl)
        throw new Error('blob fetch aborted')
      }
      this.cache.set(key, objectUrl)
      return objectUrl
    })().finally(() => {
      if (this.inflight.get(key) === req) {
        this.inflight.delete(key)
      }
    })

    this.inflight.set(key, req)
    return req
  }

  /** 释放单个 blobId 关联的 ObjectURL 并将进行中的拉取标记为失效 */
  revoke(blobId: string): void {
    this.revokedEpochs.set(blobId, (this.revokedEpochs.get(blobId) ?? 0) + 1)
    for (const key of [blobId, `${blobId}:dl`]) {
      this.inflight.delete(key)
      const url = this.cache.get(key)
      if (url) {
        URL.revokeObjectURL(url)
        this.cache.delete(key)
      }
    }
  }

  /** 清空所有缓存、递增代次并释放全部 ObjectURL */
  clear(): void {
    this.epoch += 1
    this.revokedEpochs.clear()
    for (const url of this.cache.values()) {
      URL.revokeObjectURL(url)
    }
    this.cache.clear()
    this.inflight.clear()
  }
}
