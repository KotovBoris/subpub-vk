package queue

import (
	"errors"
	"sync"
)

// возвращаем при Push, NewIterator, если SelfCleaningQueue уже закрыта
var ErrQueueClosed = errors.New("SelfCleaningQueue already closed")

// Вершина односвязного списка.
type node[T any] struct {
	value  T
	next   *node[T]
	isLast chan struct{} // открыт => это последняя вершина
}

// SelfCleaningQueue — односвязная очередь, автоматически удаляющая пройденные всеми итераторами узлы.
// FIFO-порядок, lock-free чтение, мьютекс только для Push/Close.
type SelfCleaningQueue[T any] struct {
	mu     sync.Mutex
	tail   *node[T]
	closed bool
}

// Создаёт пустую очередь.
func NewSelfCleaningQueue[T any]() *SelfCleaningQueue[T] {
	dummy := &node[T]{isLast: make(chan struct{})}
	return &SelfCleaningQueue[T]{tail: dummy}
}

// Push добавляет значение в очередь.
// Возвращает ErrQueueClosed, если очередь закрыта.
func (q *SelfCleaningQueue[T]) Push(v T) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}

	new_node := &node[T]{value: v, isLast: make(chan struct{})}
	q.tail.next = new_node

	close(q.tail.isLast)

	q.tail = new_node

	return nil
}

// Закрывает очередь и пробуждает все итераторы
func (q *SelfCleaningQueue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.tail.isLast)
	}
}

// Создаёт итератор, стартующий от текущего tail.
// Возвращает ErrQueueClosed, если очередь уже закрыта.
func (q *SelfCleaningQueue[T]) End() (*Iterator[T], error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return nil, ErrQueueClosed
	}

	return &Iterator[T]{cur: q.tail}, nil
}

// forward iterator
type Iterator[T any] struct {
	cur *node[T]
}

// Переход к следующему элементу
// Если мы на последнем элементе, то блокируется до Push или Close
func (it *Iterator[T]) Next() bool {
	if it.cur.next != nil {
		it.cur = it.cur.next
		return true
	}

	<-it.cur.isLast

	if it.cur.next != nil {
		it.cur = it.cur.next
		return true
	}

	return false
}

// Разыменование
func (it *Iterator[T]) Value() T {
	return it.cur.value
}
