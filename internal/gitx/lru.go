package gitx

import (
	"container/list"
	"sync"
)

// LRU is a bounded cache for immutable, content-addressed reads. A commit's
// file list and diff never change for a given sha, so re-opening the same
// commit (or paging its files) can be served from memory instead of
// re-shelling git. Rewritten shas (amend/rebase) simply orphan their old
// entries, which age out.
//
// It is bounded by entry count AND by bytes: 500 entries of multi-MB lockfile
// diffs is gigabytes while the entry count reads as "within limits".
// Safe for concurrent use.
type LRU[V any] struct {
	mu       sync.Mutex
	max      int
	maxBytes int
	sizeOf   func(V) int
	bytes    int
	order    *list.List // front = most recently used
	items    map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
}

// NewLRU makes a cache holding at most max entries and maxBytes bytes, sizing
// each value with sizeOf.
func NewLRU[V any](max, maxBytes int, sizeOf func(V) int) *LRU[V] {
	return &LRU[V]{max: max, maxBytes: maxBytes, sizeOf: sizeOf, order: list.New(), items: map[string]*list.Element{}}
}

// Get returns the cached value and marks it most-recently-used.
func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry[V]).val, true
}

// Set stores a value, evicting least-recently-used entries past either bound.
func (c *LRU[V]) Set(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := c.sizeOf(value)
	// An entry bigger than half the byte budget would evict everything else
	// for one hit — cheaper to just recompute it on demand. (Like TS, an
	// existing entry under the same key is left as it was.)
	if size*2 > c.maxBytes {
		return
	}
	if el, ok := c.items[key]; ok {
		c.bytes -= c.sizeOf(el.Value.(*lruEntry[V]).val)
		c.order.Remove(el)
		delete(c.items, key)
	}
	c.items[key] = c.order.PushFront(&lruEntry[V]{key: key, val: value})
	c.bytes += size
	for c.order.Len() > c.max || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		e := oldest.Value.(*lruEntry[V])
		c.order.Remove(oldest)
		delete(c.items, e.key)
		c.bytes -= c.sizeOf(e.val)
	}
}

// Len is the number of cached entries.
func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Bytes is the summed size of the cached entries.
func (c *LRU[V]) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// StrBytes sizes a string the way the TS cache did: JS strings are UTF-16, so
// ~2 bytes per code unit. Keeping the JS measure keeps the cache budgets
// (48 MB of diffs, 16 MB of file lists) meaning what they meant.
func StrBytes(s string) int { return UTF16Len(s) * 2 }

// Memo serves key from the cache or computes and caches it. Concurrent misses
// on one key each compute (as in TS); errors are not cached.
func Memo[V any](c *LRU[V], key string, fetch func() (V, error)) (V, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		return v, err
	}
	c.Set(key, v)
	return v, nil
}
