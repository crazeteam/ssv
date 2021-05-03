package eth1

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bloxapp/ssv/pubsub"
)

// Event struct
type Event struct {
	pubsub.BaseSubject
	Log          types.Log
}

// NewEvent create new event observer
func NewEvent(name string) *Event {
	return &Event{
		BaseSubject: pubsub.BaseSubject{
			Name: name,
		},
	}
}

// NotifyAll notify all subscribe observables
func (e *Event) NotifyAll(){
	for _, observer := range e.ObserverList {
		observer.Update(e.Log)
	}
}


