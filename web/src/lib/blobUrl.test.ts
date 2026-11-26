import { describe, it, expect, vi, beforeEach } from 'vitest'
import { BlobUrlCache } from './blobUrl'

describe('BlobUrlCache', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    URL.createObjectURL = vi.fn(() => 'blob:mock-1')
    URL.revokeObjectURL = vi.fn()
  })

  it('fetches blob with Bearer sessionToken and revokes ObjectURL on delete/clear', async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(() => Promise.resolve(new Response(new Blob(['ok']), { status: 200 })))
    const cache = new BlobUrlCache(fetcher)

    const url = await cache.fetchObjectUrl('blob-123', 'tok-abc')
    expect(url).toBe('blob:mock-1')
    expect(fetcher).toHaveBeenCalledWith('/api/blobs/blob-123', {
      headers: { Authorization: 'Bearer tok-abc' }
    })

    // 命中缓存不重复发起请求
    const cached = await cache.fetchObjectUrl('blob-123', 'tok-abc')
    expect(cached).toBe('blob:mock-1')
    expect(fetcher).toHaveBeenCalledTimes(1)

    cache.revoke('blob-123')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock-1')

    // 拉取下载版本并通过 clear() 统一释放
    URL.createObjectURL = vi.fn(() => 'blob:mock-2')
    const dlUrl = await cache.fetchObjectUrl('blob-456', 'tok-abc', true)
    expect(dlUrl).toBe('blob:mock-2')
    expect(fetcher).toHaveBeenLastCalledWith('/api/blobs/blob-456?download=1', {
      headers: { Authorization: 'Bearer tok-abc' }
    })

    cache.clear()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock-2')
  })

  it('deduplicates concurrent inflight fetches and throws on non-ok response', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(new Response(new Blob(['ok']), { status: 200 }))
      .mockResolvedValueOnce(new Response('forbidden', { status: 403 }))
    const cache = new BlobUrlCache(fetcher)

    const [u1, u2] = await Promise.all([
      cache.fetchObjectUrl('blob-concurrent', 'tok-1'),
      cache.fetchObjectUrl('blob-concurrent', 'tok-1')
    ])
    expect(u1).toBe('blob:mock-1')
    expect(u2).toBe('blob:mock-1')
    expect(fetcher).toHaveBeenCalledTimes(1)

    await expect(cache.fetchObjectUrl('blob-fail', 'tok-1')).rejects.toThrow(
      'blob fetch failed with 403'
    )
  })

  it('rejects inflight fetch, revokes created ObjectURL, and skips caching when revoked or cleared mid-flight', async () => {
    let seq = 0
    URL.createObjectURL = vi.fn(() => `blob:race-${++seq}`)

    let resolveReq1!: (res: Response) => void
    let resolveReq2!: (res: Response) => void
    let resolveReq3!: (res: Response) => void
    let resolveReq4!: (res: Response) => void

    const fetcher = vi
      .fn()
      .mockImplementationOnce(() => new Promise<Response>((res) => { resolveReq1 = res }))
      .mockImplementationOnce(() => new Promise<Response>((res) => { resolveReq2 = res }))
      .mockImplementationOnce(() => new Promise<Response>((res) => { resolveReq3 = res }))
      .mockImplementationOnce(() => new Promise<Response>((res) => { resolveReq4 = res }))

    const cache = new BlobUrlCache(fetcher)

    // 1. revoke() 发生时老请求仍在途中；紧接着再次请求同一 blobId 不应复用被撤销的 inflight，也不得被老请求 finally 删除
    const p1 = cache.fetchObjectUrl('blob-race', 'tok-1')
    cache.revoke('blob-race')

    const p2 = cache.fetchObjectUrl('blob-race', 'tok-1')
    expect(fetcher).toHaveBeenCalledTimes(2)

    resolveReq1(new Response(new Blob(['stale']), { status: 200 }))
    await expect(p1).rejects.toThrow()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:race-1')

    // p1 结束后不得误删 p2 的 inflight 记录：此时再次请求应仍复用 p2，不发第 3 次请求
    const p2Dedup = cache.fetchObjectUrl('blob-race', 'tok-1')
    expect(fetcher).toHaveBeenCalledTimes(2)

    // p1 结束后 p2 仍应正常完成并缓存
    resolveReq2(new Response(new Blob(['fresh']), { status: 200 }))
    expect(await p2).toBe('blob:race-2')
    expect(await p2Dedup).toBe('blob:race-2')
    expect(await cache.fetchObjectUrl('blob-race', 'tok-1')).toBe('blob:race-2')
    expect(fetcher).toHaveBeenCalledTimes(2)

    // 2. clear() 发生在请求途中：返回后应立即释放 ObjectURL 且不得写入缓存
    const p3 = cache.fetchObjectUrl('blob-clear', 'tok-1')
    cache.clear()
    resolveReq3(new Response(new Blob(['old-space']), { status: 200 }))
    await expect(p3).rejects.toThrow()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:race-3')

    const p4 = cache.fetchObjectUrl('blob-clear', 'tok-2')
    resolveReq4(new Response(new Blob(['new-space']), { status: 200 }))
    expect(await p4).toBe('blob:race-4')
    expect(fetcher).toHaveBeenCalledTimes(4)
  })
})
