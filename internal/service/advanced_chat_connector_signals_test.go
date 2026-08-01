package service

import (
	"testing"
	"time"
)

func TestAdvancedChatConnectorSignalsWakeAndUnsubscribe(t *testing.T) {
	key := "test-signal-" + time.Now().Format("150405.000000000")
	first, stopFirst := watchAdvancedChatConnectorSignal(key)
	second, stopSecond := watchAdvancedChatConnectorSignal(key)
	defer stopSecond()

	notifyAdvancedChatConnectorSignal(key)
	for _, signal := range []<-chan struct{}{first, second} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatal("connector signal did not wake a waiter")
		}
	}

	stopFirst()
	notifyAdvancedChatConnectorSignal(key)
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("remaining connector signal waiter was not notified")
	}
	select {
	case <-first:
		t.Fatal("unsubscribed connector signal waiter was notified")
	default:
	}
}
