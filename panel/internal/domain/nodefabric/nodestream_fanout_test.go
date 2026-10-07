package nodefabric

import (
	"testing"
)

func TestStreamPushQueueCoalesces(t *testing.T) {
	q := newStreamPushQueue()
	q.addNode("n1")
	q.addNode("n1")
	q.addAllUsers()
	q.addAllUsers()
	select {
	case <-q.wake:
	default:
		t.Fatal("queue did not wake the worker")
	}
	select {
	case <-q.wake:
		t.Fatal("queue woke the worker once per event instead of coalescing")
	default:
	}
	nodes, all := q.take()
	if len(nodes) != 1 || nodes[0] != "n1" || !all {
		t.Fatalf("take = %v %v", nodes, all)
	}
	if nodes, all := q.take(); len(nodes) != 0 || all {
		t.Fatalf("queue kept work after take: %v %v", nodes, all)
	}
}
