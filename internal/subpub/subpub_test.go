package subpub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func ensureClosed(t *testing.T, sp SubPub) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, sp.Close(ctx), "SubPub Close failed")
}

// Проверяет базовую функциональность публикации-подписки.
func TestBasicPubSub(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	doneSignal := make(chan struct{})
	testSubject := "notifications.user.created"
	testMessage := "User John Doe created"

	_, err := sp.Subscribe(testSubject, func(msg interface{}) {
		require.Equal(t, testMessage, msg, "Received message does not match sent message")
		close(doneSignal)
	})
	require.NoError(t, err, "Subscribe failed")

	err = sp.Publish(testSubject, testMessage)
	require.NoError(t, err, "Publish failed")

	select {
	case <-doneSignal:
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout: Message not received by subscriber")
	}
}

// Проверяет публикацию в топик без подписчиков.
func TestPublishToNonSubscribed(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	err := sp.Publish("metrics.cpu.load", 0.75)
	require.NoError(t, err, "Publishing to a topic with no subscribers should not error")
}

// Проверяет изоляцию подписок на разные топики.
func TestMultipleTopics(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	var wg sync.WaitGroup
	wg.Add(2)

	topicAlpha := "events.alpha"
	messageAlpha := "Message for Alpha"
	_, err := sp.Subscribe(topicAlpha, func(msg interface{}) {
		require.Equal(t, messageAlpha, msg)
		wg.Done()
	})
	require.NoError(t, err)

	topicBeta := "events.beta"
	messageBeta := "Message for Beta"
	_, err = sp.Subscribe(topicBeta, func(msg interface{}) {
		require.Equal(t, messageBeta, msg)
		wg.Done()
	})
	require.NoError(t, err)

	require.NoError(t, sp.Publish(topicAlpha, messageAlpha))
	require.NoError(t, sp.Publish(topicBeta, messageBeta))

	waitTimeout(&wg, t, 2*time.Second, "Timeout waiting for messages on multiple topics")
}

// Проверяет получение сообщений всеми подписчиками одного топика.
func TestMultipleSubscribersSameTopic(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	const subscriberCount = 3
	var wg sync.WaitGroup
	wg.Add(subscriberCount)
	sharedTopic := "updates.global"
	sharedMessage := "System maintenance soon"

	for i := 0; i < subscriberCount; i++ {
		_, err := sp.Subscribe(sharedTopic, func(msg interface{}) {
			require.Equal(t, sharedMessage, msg)
			wg.Done()
		})
		require.NoError(t, err)
	}

	require.NoError(t, sp.Publish(sharedTopic, sharedMessage))
	waitTimeout(&wg, t, 2*time.Second, "Timeout waiting for all subscribers on the same topic")
}

// Проверяет, что медленный подписчик не блокирует быстрого.
func TestSlowSubscriberNonBlockingFast(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	topic := "feed.realtime"
	const messageCount = 10
	var fastSubscriberMessages int32
	fastSubscriberDone := make(chan struct{})

	_, err := sp.Subscribe(topic, func(msg interface{}) {
		time.Sleep(100 * time.Millisecond)
	})
	require.NoError(t, err)

	_, err = sp.Subscribe(topic, func(msg interface{}) {
		if atomic.AddInt32(&fastSubscriberMessages, 1) == messageCount {
			close(fastSubscriberDone)
		}
	})
	require.NoError(t, err)

	for i := 0; i < messageCount; i++ {
		require.NoError(t, sp.Publish(topic, fmt.Sprintf("item-%d", i)))
	}

	select {
	case <-fastSubscriberDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Fast subscriber was blocked or did not receive messages in time. Received: %d", atomic.LoadInt32(&fastSubscriberMessages))
	}
}

// Проверяет прекращение получения сообщений после отписки.
func TestUnsubscribe(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	topic := "alerts.critical"
	unsubscribedHandlerCalled := atomic.Bool{}

	subToCancel, err := sp.Subscribe(topic, func(msg interface{}) {
		unsubscribedHandlerCalled.Store(true)
		t.Error("Handler for unsubscribed subscription was unexpectedly called")
	})
	require.NoError(t, err)

	subToCancel.Unsubscribe()
	subToCancel.Unsubscribe()

	activeHandlerDone := make(chan struct{})
	activeMessage := "System operational"
	_, err = sp.Subscribe(topic, func(msg interface{}) {
		require.Equal(t, activeMessage, msg)
		close(activeHandlerDone)
	})
	require.NoError(t, err)

	require.NoError(t, sp.Publish(topic, activeMessage))

	select {
	case <-activeHandlerDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Active subscriber did not receive message after another was unsubscribed")
	}

	time.Sleep(50 * time.Millisecond)
	require.False(t, unsubscribedHandlerCalled.Load(), "Unsubscribed handler was called")
}

// Проверяет порядок доставки сообщений FIFO.
func TestMessageOrderFIFO(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	topic := "logs.sequential"
	const messageTotal = 25
	var wg sync.WaitGroup
	wg.Add(messageTotal)

	var lastReceivedID int32 = -1

	_, err := sp.Subscribe(topic, func(msg interface{}) {
		defer wg.Done()
		currentID, ok := msg.(int32)
		require.True(t, ok, "Message type is not int32 as expected")

		previousID := atomic.LoadInt32(&lastReceivedID)
		require.Equal(t, previousID+1, currentID, "Message order violated")
		atomic.StoreInt32(&lastReceivedID, currentID)
	})
	require.NoError(t, err)

	for i := int32(0); i < messageTotal; i++ {
		require.NoError(t, sp.Publish(topic, i))
	}

	waitTimeout(&wg, t, 2*time.Second, "Timeout waiting for all FIFO messages")
}

// Проверяет ошибки операций после закрытия SubPub.
func TestOperationsAfterClose(t *testing.T) {
	sp := NewSubPub()
	require.NoError(t, sp.Close(context.Background()))

	_, err := sp.Subscribe("some.topic", func(msg interface{}) {
		t.Error("Subscribe callback should not be invoked on a closed SubPub")
	})
	require.Error(t, err, "Subscribe on a closed SubPub should return an error")

	err = sp.Publish("some.topic", "a message")
	require.Error(t, err, "Publish on a closed SubPub should return an error")
}

// Проверяет обработку отмены контекста при закрытии.
func TestCloseWithContextCancellation(t *testing.T) {
	sp := NewSubPub()

	handlerStarted := make(chan struct{})
	handlerFinished := make(chan struct{})

	_, err := sp.Subscribe("long.task", func(msg interface{}) {
		close(handlerStarted)
		time.Sleep(1 * time.Second)
		close(handlerFinished)
	})
	require.NoError(t, err)
	require.NoError(t, sp.Publish("long.task", "process this"))

	select {
	case <-handlerStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Handler did not start in time")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	closeStartTime := time.Now()
	closeErr := sp.Close(ctx)
	closeDuration := time.Since(closeStartTime)

	if closeErr != nil {
		assert.ErrorIs(t, closeErr, context.DeadlineExceeded, "Close error should be context.DeadlineExceeded or similar")
	}
	assert.True(t, closeDuration < 200*time.Millisecond, "Close took too long (%v), expected to return quickly due to context", closeDuration)

	select {
	case <-handlerFinished:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Long-running handler did not complete after Close returned with canceled context")
	}
}

// Проверяет ожидание доставки сообщений при закрытии с context.Background().
func TestCloseWaitsForDelivery(t *testing.T) {
	sp := NewSubPub()

	const numMessages = 5
	var messagesProcessedCount int32
	allMessagesProcessedSignal := make(chan struct{})

	_, err := sp.Subscribe("data.pipeline", func(msg interface{}) {
		time.Sleep(50 * time.Millisecond)
		if atomic.AddInt32(&messagesProcessedCount, 1) == numMessages {
			close(allMessagesProcessedSignal)
		}
	})
	require.NoError(t, err)

	for i := 0; i < numMessages; i++ {
		require.NoError(t, sp.Publish("data.pipeline", fmt.Sprintf("chunk-%d", i)))
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- sp.Close(context.Background())
	}()

	select {
	case err := <-closeDone:
		require.NoError(t, err, "Close returned an error")
		assert.EqualValues(t, numMessages, atomic.LoadInt32(&messagesProcessedCount), "Not all messages processed before Close returned")
	case <-time.After(1 * time.Second):
		t.Fatal("Close with background context timed out, or messages not delivered")
	}

	select {
	case <-allMessagesProcessedSignal:
	default:
		t.Error("Signal from handler indicating all messages processed was not received")
	}
}

// Проверяет неблокирующую публикацию при медленном подписчике.
func TestPublishNonBlockingToSlowSub(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	var messageReceivedBySlowSub atomic.Int32
	const numToPublish = 20

	_, err := sp.Subscribe("nonblock-topic", func(msg interface{}) {
		time.Sleep(50 * time.Millisecond)
		messageReceivedBySlowSub.Add(1)
	})
	require.NoError(t, err)

	publishCompleted := make(chan struct{})
	go func() {
		defer close(publishCompleted)
		for i := 0; i < numToPublish; i++ {
			err := sp.Publish("nonblock-topic", fmt.Sprintf("event-%d", i))
			if err != nil {
				require.NoError(t, err, "Publish returned an error")
			}
		}
	}()

	select {
	case <-publishCompleted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Publish calls appear to be blocking on slow subscriber")
	}

	time.Sleep((numToPublish * 50 * time.Millisecond) / 2)
	assert.Less(t, messageReceivedBySlowSub.Load(), int32(numToPublish), "Slow subscriber should not have processed all messages yet")
	assert.Greater(t, messageReceivedBySlowSub.Load(), int32(0), "Slow subscriber should have processed some messages")
}

// Проверяет корректность одновременной публикации из нескольких горутин.
func TestConcurrentPublishers(t *testing.T) {
	sp := NewSubPub()
	defer ensureClosed(t, sp)

	topic := "high-traffic.updates"
	numPublisherRoutines := 5
	messagesPerRoutine := 10
	totalExpectedMessages := numPublisherRoutines * messagesPerRoutine

	var receivedMessageCount atomic.Int32
	allMessagesReceivedSignal := make(chan struct{})

	_, err := sp.Subscribe(topic, func(msg interface{}) {
		if receivedMessageCount.Add(1) == int32(totalExpectedMessages) {
			close(allMessagesReceivedSignal)
		}
	})
	require.NoError(t, err)

	var publisherWg sync.WaitGroup
	publisherWg.Add(numPublisherRoutines)

	for i := 0; i < numPublisherRoutines; i++ {
		go func(publisherID int) {
			defer publisherWg.Done()
			for j := 0; j < messagesPerRoutine; j++ {
				payload := fmt.Sprintf("P%d-M%d", publisherID, j)
				err := sp.Publish(topic, payload)
				assert.NoError(t, err, "Concurrent publish failed")
			}
		}(i)
	}

	publisherWg.Wait()

	select {
	case <-allMessagesReceivedSignal:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timeout waiting for all messages from concurrent publishers. Expected: %d, Got: %d",
			totalExpectedMessages, receivedMessageCount.Load())
	}
}

func waitTimeout(wg *sync.WaitGroup, t *testing.T, timeout time.Duration, msg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal(msg)
	}
}
