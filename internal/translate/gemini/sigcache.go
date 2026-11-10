package gemini

import (
	"sync"
	"time"
)

// DummySignature is the value Google documents for skipping thought
// signature validation when the real signature is unavailable.
const DummySignature = "skip_thought_signature_validator"

const (
	sigCacheMax = 50000
	sigCacheTTL = 6 * time.Hour
)

var sigCache = newSigLRU(sigCacheMax, sigCacheTTL)

// RememberSignature stores the thoughtSignature Gemini attached to a
// functionCall, keyed by the tool call ID the gateway exposed to the client.
func RememberSignature(id, sig string) {
	if id == "" || sig == "" || sig == DummySignature {
		return
	}
	sigCache.put(id, sig)
}

// LookupSignature returns the cached thoughtSignature for a tool call ID.
func LookupSignature(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	return sigCache.get(id)
}

type sigEntry struct {
	key, val   string
	exp        int64
	prev, next *sigEntry
}

// sigLRU is a mutex-guarded LRU with a per-entry TTL. head is most recent.
type sigLRU struct {
	mu         sync.Mutex
	max        int
	ttl        time.Duration
	now        func() time.Time
	m          map[string]*sigEntry
	head, tail *sigEntry
}

func newSigLRU(max int, ttl time.Duration) *sigLRU {
	return &sigLRU{max: max, ttl: ttl, now: time.Now, m: make(map[string]*sigEntry)}
}

func (c *sigLRU) unlink(e *sigEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (c *sigLRU) pushFront(e *sigEntry) {
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *sigLRU) put(key, val string) {
	exp := c.now().Add(c.ttl).UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		e.val, e.exp = val, exp
		c.unlink(e)
		c.pushFront(e)
		return
	}
	e := &sigEntry{key: key, val: val, exp: exp}
	c.m[key] = e
	c.pushFront(e)
	for len(c.m) > c.max && c.tail != nil {
		old := c.tail
		c.unlink(old)
		delete(c.m, old.key)
	}
}

func (c *sigLRU) get(key string) (string, bool) {
	now := c.now().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return "", false
	}
	if now >= e.exp {
		c.unlink(e)
		delete(c.m, key)
		return "", false
	}
	c.unlink(e)
	c.pushFront(e)
	return e.val, true
}

func (c *sigLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
