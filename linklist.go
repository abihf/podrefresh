package main

import (
	"iter"
	"sync"
)

type linkListItem[T any] struct {
	value T
	next  *linkListItem[T]
}

// LinkList is a thread-safe linked list.
type LinkList[T any] struct {
	mu    sync.RWMutex
	first *linkListItem[T]
	last  *linkListItem[T]
}

// Append adds a new item to the end of the linked list.
func (l *LinkList[T]) Append(value T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	item := &linkListItem[T]{value: value}
	if l.last == nil {
		l.first = item
		l.last = item
	} else {
		l.last.next = item
		l.last = item
	}
}

// Iter returns an iterator over the linked list.
func (l *LinkList[T]) Iter() iter.Seq[T] {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return func(yield func(T) bool) {
		for item := l.first; item != nil; item = item.next {
			if !yield(item.value) {
				return
			}
		}
	}
}
