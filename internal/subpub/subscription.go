package subpub

// Реализует Subscription
type MySubscription struct {
	closeToUnsub chan struct{}
}

// Закрывает канал, чтобы остановить subscriberLoop.
func (s *MySubscription) Unsubscribe() {
	select {
	case <-s.closeToUnsub:
		// уже закрыт
	default:
		close(s.closeToUnsub)
	}
}
