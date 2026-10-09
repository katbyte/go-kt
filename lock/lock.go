// Package lock keeps the callers in one process from undoing each other's
// change to the same record, with a lock for each record by its key.
//
// A tool that edits a record by reading it whole and sending it back - no
// server it talks to has a conditional update - loses a change when two such
// edits run at once: each sends back what it read, and the later one undoes
// the earlier. An MCP client runs the calls of one turn at once, so eight
// calls each adding a tag to one book left one tag. Holding the record's key
// for the whole read and write keeps them apart.
//
// The locks belong to the process. Another client of the same server, its
// own web app included, is not held back.
package lock

import (
	"slices"
	"sync"
)

// Locks is a set of locks by key, made when first wanted and dropped when
// nobody holds or waits for one, so a long-running server does not keep one
// for every record it ever touched. The zero value is ready to use.
type Locks struct {
	all sync.RWMutex

	mu   sync.Mutex
	keys map[string]*keyLock
}

type keyLock struct {
	sync.Mutex

	holders int
}

// Hold takes the named keys until release is called. They are taken in
// sorted order, each once however often it is named, so two calls holding
// overlapping sets cannot wait on each other for ever.
//
// A caller holds once at a time: taking a second set while holding one can
// wait on a HoldAll that is itself waiting for the first to be released.
func (l *Locks) Hold(keys ...string) (release func()) {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	l.all.RLock()
	held := make([]*keyLock, 0, len(keys))
	for _, key := range keys {
		l.mu.Lock()
		if l.keys == nil {
			l.keys = map[string]*keyLock{}
		}
		k := l.keys[key]
		if k == nil {
			k = &keyLock{}
			l.keys[key] = k
		}
		k.holders++
		l.mu.Unlock()

		k.Lock()
		held = append(held, k)
	}

	return func() {
		for i, k := range slices.Backward(held) {
			k.Unlock()
			l.mu.Lock()
			k.holders--
			if k.holders == 0 {
				delete(l.keys, keys[i])
			}
			l.mu.Unlock()
		}
		l.all.RUnlock()
	}
}

// HoldAll takes every key there is or could be, for a change that reads a
// whole collection and writes it back: it waits for every Hold to be
// released, and no Hold is granted until it is.
func (l *Locks) HoldAll() (release func()) {
	l.all.Lock()

	return l.all.Unlock
}
