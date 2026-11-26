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
})
