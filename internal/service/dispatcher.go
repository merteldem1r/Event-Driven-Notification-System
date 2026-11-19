package service

import "context"

// Dispatcher delivers queued notifications, one per ProcessNext call; the worker calls it.
type Dispatcher struct{}

// NewDispatcher returns a Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// ProcessNext takes the next notification and delivers it; found is false if the queue is empty.
// Claiming, sending and recording the result are added in steps 6.2 to 6.4; until then there is
// nothing to take.
func (d *Dispatcher) ProcessNext(_ context.Context) (found bool, err error) {
	return false, nil
}
