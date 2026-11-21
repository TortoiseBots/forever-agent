package tables

// The per-session record cache: the agent never asks twice for the same
// record. It is a plain LRU over (table, id) with a fixed entry bound, so a
// hostile or merely busy server cannot grow the client's memory. The zero
// value is not usable; NewCache makes one.

import (
	"container/list"
)

// DefaultCacheSize bounds one session's cached records. Three tables, a few
// dozen spells and faction templates per run: 256 entries is far above that
// and still a hard stop.
const DefaultCacheSize = 256

// recordKey names one cached record.
type recordKey struct {
	table uint32
	id    uint32
}

// recordEntry is one cached record: the reply's verdict and, when it is
// Found, the record bytes. A not-found verdict is cached too, so a missing
// id is asked once, not once per lookup.
type recordEntry struct {
	key   recordKey
	found bool
	data  []byte
}

// Cache is a bounded LRU of DB records.
type Cache struct {
	cap   int
	items map[recordKey]*list.Element
	order *list.List
}

// NewCache returns a cache of at most n records; n <= 0 means DefaultCacheSize.
func NewCache(n int) *Cache {
	if n <= 0 {
		n = DefaultCacheSize
	}
	return &Cache{cap: n, items: map[recordKey]*list.Element{}, order: list.New()}
}

// Get returns the cached record for table/id, if any.
func (c *Cache) Get(table, id uint32) (data []byte, found, ok bool) {
	if e, hit := c.items[recordKey{table, id}]; hit {
		c.order.MoveToFront(e)
		en := e.Value.(recordEntry)
		return en.data, en.found, true
	}
	return nil, false, false
}

// Put stores the reply's verdict for table/id, evicting the least recently
// used entry past the bound.
func (c *Cache) Put(table, id uint32, found bool, data []byte) {
	k := recordKey{table, id}
	if e, hit := c.items[k]; hit {
		c.order.MoveToFront(e)
		e.Value = recordEntry{key: k, found: found, data: data}
		return
	}
	for len(c.items) >= c.cap {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.items, back.Value.(recordEntry).key)
	}
	c.items[k] = c.order.PushFront(recordEntry{key: k, found: found, data: data})
}

// Len is the cached entry count.
func (c *Cache) Len() int { return len(c.items) }
