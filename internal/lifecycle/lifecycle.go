// Package lifecycle contains the order state machine. It has no database,
// clock, HTTP, or configuration dependencies so every transition is explicit
// and table-testable.
package lifecycle

import "fmt"

type State string

const (
	Pending   State = "pending"
	Batched   State = "batched"
	Assigned  State = "assigned"
	PickedUp  State = "picked_up"
	Delivered State = "delivered"
	Returning State = "returning"
	Returned  State = "returned"
	Cancelled State = "cancelled"
)

type Event string

const (
	PickupConfirmed Event = "pickup_confirmed"
	DeliveredEvent  Event = "delivered"
	AttemptFailed   Event = "attempt_failed"
	Cancel          Event = "cancel"
	ReturnStarted   Event = "return_started"
	ReturnedEvent   Event = "returned"
	Released        Event = "released"
)

// Transition applies one lifecycle event. AttemptFailed is deliberately a
// self-transition; the caller supplies the attempt count and uses
// ReturnStarted for the third failed attempt.
func Transition(state State, event Event) (State, error) {
	switch event {
	case PickupConfirmed:
		if state == Assigned {
			return PickedUp, nil
		}
	case Released:
		if state == Assigned {
			return Pending, nil
		}
	case DeliveredEvent:
		if state == PickedUp {
			return Delivered, nil
		}
	case AttemptFailed:
		if state == PickedUp {
			return PickedUp, nil
		}
	case ReturnStarted:
		if state == PickedUp {
			return Returning, nil
		}
	case ReturnedEvent:
		if state == Returning {
			return Returned, nil
		}
	case Cancel:
		switch state {
		case Pending, Batched, Assigned, PickedUp:
			return Cancelled, nil
		}
	}
	return state, fmt.Errorf("invalid lifecycle transition: %s + %s", state, event)
}

// TransitionAttemptFailed applies a failed delivery attempt and moves the
// order into the return flow on the third failure.
func TransitionAttemptFailed(state State, attempts int) (State, error) {
	if attempts < 1 || attempts > 3 {
		return state, fmt.Errorf("delivery attempts must be between 1 and 3: %d", attempts)
	}
	if _, err := Transition(state, AttemptFailed); err != nil {
		return state, err
	}
	if attempts == 3 {
		return Transition(state, ReturnStarted)
	}
	return state, nil
}
