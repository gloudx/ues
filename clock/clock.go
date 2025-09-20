package clock

import "sync"

// LogicalClock ...
type LogicalClock struct {
	time uint64
	mu   sync.Mutex
}

func NewLogicalClock() *LogicalClock {
	return &LogicalClock{
		time: 0,
	}
}

// Tick ...
func (lc *LogicalClock) Tick() uint64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.time++
	return lc.time
}

// Update ...
func (lc *LogicalClock) Update(remoteTime uint64) uint64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if remoteTime >= lc.time {
		lc.time = remoteTime + 1
	} else {
		lc.time++
	}
	return lc.time
}

func CompareClocks(a, b *LogicalClock) int {
	if a.time < b.time {
		return -1
	} else if a.time > b.time {
		return 1
	}
	return 0
}
