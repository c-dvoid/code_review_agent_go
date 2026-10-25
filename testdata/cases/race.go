package counter

import "sync"

type Counter struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *Counter) Inc(k string) {
	c.mu.Lock()
	c.m[k]++
	c.mu.Unlock()
}

func (c *Counter) Get(k string) int {
	return c.m[k]
}
