package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransition(t *testing.T) {
	tests := []struct {
		from  State
		event Event
		want  State
		ok    bool
	}{
		{Assigned, PickupConfirmed, PickedUp, true},
		{Assigned, Released, Pending, true},
		{PickedUp, DeliveredEvent, Delivered, true},
		{PickedUp, AttemptFailed, PickedUp, true},
		{PickedUp, ReturnStarted, Returning, true},
		{Returning, ReturnedEvent, Returned, true},
		{Pending, Cancel, Cancelled, true},
		{Batched, Cancel, Cancelled, true},
		{Assigned, Cancel, Cancelled, true},
		{PickedUp, Cancel, Cancelled, true},
		{Delivered, DeliveredEvent, Delivered, false},
		{Cancelled, PickupConfirmed, Cancelled, false},
		{Returning, Cancel, Returning, false},
		{Returned, ReturnedEvent, Returned, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.from)+"_"+string(tc.event), func(t *testing.T) {
			got, err := Transition(tc.from, tc.event)
			if tc.ok {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			} else {
				assert.Error(t, err)
				assert.Equal(t, tc.from, got)
			}
		})
	}
}

func TestTransitionAttemptFailed(t *testing.T) {
	for attempts, want := range map[int]State{1: PickedUp, 2: PickedUp, 3: Returning} {
		got, err := TransitionAttemptFailed(PickedUp, attempts)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	for _, attempts := range []int{0, 4} {
		_, err := TransitionAttemptFailed(PickedUp, attempts)
		assert.Error(t, err)
	}
}
