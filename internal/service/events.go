package service

// LifecycleEvent is the small event contract used by Phase 4. Phase 10 can
// replace the no-op sink with an outbox-backed implementation without changing
// lifecycle transactions or handlers.
type LifecycleEvent struct {
	Type    string
	OrderID string
	BatchID string
	RiderID string
}

type EventSink interface {
	Emit(LifecycleEvent)
}

type NoopEventSink struct{}

func (NoopEventSink) Emit(LifecycleEvent) {}
