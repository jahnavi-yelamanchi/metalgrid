package queue

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// fakeMsg is a minimal jetstream.Msg for exercising interleaveByKey without
// a real NATS connection.
type fakeMsg struct {
	jetstream.Msg
	data []byte
}

func (f *fakeMsg) Data() []byte { return f.data }

func TestInterleaveByKeyRoundRobinsAcrossGroups(t *testing.T) {
	msgs := []jetstream.Msg{
		&fakeMsg{data: []byte("a1")},
		&fakeMsg{data: []byte("a2")},
		&fakeMsg{data: []byte("a3")},
		&fakeMsg{data: []byte("b1")},
		&fakeMsg{data: []byte("b2")},
	}
	keyFunc := func(data []byte) string { return string(data[:1]) } // "a" or "b"

	got := interleaveByKey(msgs, keyFunc)

	want := []string{"a1", "b1", "a2", "b2", "a3"}
	if len(got) != len(want) {
		t.Fatalf("expected %d messages, got %d", len(want), len(got))
	}
	for i, m := range got {
		if string(m.Data()) != want[i] {
			t.Errorf("position %d: got %s, want %s", i, m.Data(), want[i])
		}
	}
}

func TestInterleaveByKeyPreservesSingleGroupOrder(t *testing.T) {
	msgs := []jetstream.Msg{
		&fakeMsg{data: []byte("x1")},
		&fakeMsg{data: []byte("x2")},
		&fakeMsg{data: []byte("x3")},
	}
	keyFunc := func([]byte) string { return "only-team" }

	got := interleaveByKey(msgs, keyFunc)

	want := []string{"x1", "x2", "x3"}
	for i, m := range got {
		if string(m.Data()) != want[i] {
			t.Errorf("position %d: got %s, want %s", i, m.Data(), want[i])
		}
	}
}

func TestInterleaveByKeyEmpty(t *testing.T) {
	if got := interleaveByKey(nil, func([]byte) string { return "" }); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}
