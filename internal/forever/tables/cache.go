package tables

// The per-session record cache: the agent never asks twice for the same
// record within a verdict's own life. It is a plain LRU over (table, id)
// with a fixed entry bound, so a hostile or merely busy server cannot grow
// the client's memory, guarded by its own mutex: the session goroutine and
// the tests beside it share it. The zero value is not usable; NewCache
// makes one.

import (
	"container/list"
	"sync"
	"time"
)

// DefaultCacheSize bounds one session's cached records. Three tables, a few
// dozen spells and faction templates per run: 256 entries is far above that
// and still a hard stop.
const DefaultCacheSize = 256

// DefaultNegativeTTL bounds how long a miss stays cached: a "not found" or
// timed-out id is asked again after it lapses instead of never. The server
// does not ungive a record, so a found record never lapses.
const DefaultNegativeTTL = 5 * time.Minute

// recordKey names one cached record.
type recordKey struct {
	table uint32
	id    uint32
}

// recordEntry is one cached record: the reply's verdict and, when it is
// Found, the record bytes. A not-found verdict is cached too, until lapsesAt
// passes, so a missing id is asked once per while, not once per lookup.
type recordEntry struct {
	key      recordKey
	found    bool
	data     []byte
	lapsesAt time.Time // when a miss stops counting; zero for a found record
}

// Cache is a bounded LRU of DB records.
type Cache struct {
	mu     sync.Mutex
	cap    int
	negTTL time.Duration
	now    func() time.Time
	items  map[recordKey]*list.Element
	order  *list.List
}

// NewCache returns a cache of at most n records; n <= 0 means DefaultCacheSize.
func NewCache(n int) *Cache {
	if n <= 0 {
		n = DefaultCacheSize
	}
	return &Cache{cap: n, negTTL: DefaultNegativeTTL, now: time.Now, items: map[recordKey]*list.Element{}, order: list.New()}
}

// Get returns the cached record for table/id, if any. A lapsed miss is not
// returned: evicting it here makes the caller ask again.
func (c *Cache) Get(table, id uint32) (data []byte, found, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, hit := c.items[recordKey{table, id}]
	if !hit {
		return nil, false, false
	}
	en := e.Value.(recordEntry)
	if !en.found && !en.lapsesAt.IsZero() && !c.now().Before(en.lapsesAt) {
		c.order.Remove(e)
		delete(c.items, en.key)
		return nil, false, false
	}
	c.order.MoveToFront(e)
	return en.data, en.found, true
}

// Put stores the reply's verdict for table/id, evicting the least recently
// used entry past the bound. A miss lapses after the negative TTL.
func (c *Cache) Put(table, id uint32, found bool, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := recordKey{table, id}
	en := recordEntry{key: k, found: found, data: data}
	if !found {
		en.lapsesAt = c.now().Add(c.negTTL)
	}
	if e, hit := c.items[k]; hit {
		c.order.MoveToFront(e)
		e.Value = en
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
	c.items[k] = c.order.PushFront(en)
}

// Len is the cached entry count.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
