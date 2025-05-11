package subpub

import (
	"context"
	"errors"
	"sync"

	"github.com/KotovBoris/subpub-vk/internal/subpub/internal/queue"
)

// Возвращаем при Publish, Subscribe, если MySubPub уже закрыт
var ErrSubPubClosed = errors.New("MySubPub already closed")

// Реализует SubPub
type MySubPub struct {
	mu     sync.RWMutex
	topics map[string]*queue.SelfCleaningQueue[interface{}]
	closed bool
	wg     sync.WaitGroup
}

func NewSubPub() SubPub {
	return &MySubPub{topics: make(map[string]*queue.SelfCleaningQueue[interface{}])}
}

// Возвращает очередь, создавая её при необходимости.
// Возвращает ErrSubPubClosed, если SubPub уже закрыт.
func (this *MySubPub) getQueue(subject string) (*queue.SelfCleaningQueue[interface{}], error) {
	this.mu.RLock()

	if this.closed {
		this.mu.RUnlock()
		return nil, ErrSubPubClosed
	}

	topicQueue, exists := this.topics[subject]

	this.mu.RUnlock()

	if !exists {
		this.mu.Lock()

		topicQueue, exists = this.topics[subject]
		if !exists {
			topicQueue = queue.NewSelfCleaningQueue[interface{}]()
			this.topics[subject] = topicQueue
		}

		this.mu.Unlock()
	}

	return topicQueue, nil
}

func (this *MySubPub) Publish(subject string, msg interface{}) error {
	topicQueue, err := this.getQueue(subject)
	if err != nil {
		return err
	}

	if err := topicQueue.Push(msg); err != nil {
		return err
	}

	return nil
}

func (this *MySubPub) Subscribe(subject string, callback MessageHandler) (Subscription, error) {
	topicQueue, err := this.getQueue(subject)
	if err != nil {
		return nil, err
	}

	iterator, err := topicQueue.End()
	if err != nil {
		return nil, err
	}

	closeToUnsub := make(chan struct{})

	this.wg.Add(1)
	go subscriberLoop(iterator, closeToUnsub, callback, &this.wg)

	return &MySubscription{closeToUnsub: closeToUnsub}, nil
}

func (this *MySubPub) Close(ctx context.Context) error {
	this.mu.Lock()

	if !this.closed {
		this.closed = true

		for _, topicQueue := range this.topics {
			topicQueue.Close()
		}
	}

	this.mu.Unlock()

	// ждём завершения всех подписчиков или контекста
	done := make(chan struct{})
	go func() {
		this.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Обработка сообщений для одного подписчика
func subscriberLoop(
	iterator *queue.Iterator[interface{}],
	closeToUnsub <-chan struct{},
	callback MessageHandler,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	for {
		select {
		case <-closeToUnsub:
			return
		default:
			if ok := iterator.Next(); !ok {
				return
			}

			callback(iterator.Value())
		}
	}
}
