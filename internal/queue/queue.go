// Package queue wraps NATS JetStream for the job submission queue. Publishing
// with a Nats-Msg-Id gives idempotent submission for free: JetStream drops
// duplicate message IDs within its dedup window.
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName   = "JOBS"
	SubmitSubj   = "jobs.submit"
	DLQSubj      = "jobs.dlq"
	ConsumerName = "operator-jobs"
)

type Queue struct {
	nc *nats.Conn
	js jetstream.JetStream
}

func Connect(ctx context.Context, url string) (*Queue, error) {
	nc, err := nats.Connect(url, nats.MaxReconnects(-1))
	if err != nil {
		return nil, fmt.Errorf("connecting to nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("creating jetstream context: %w", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{SubmitSubj, DLQSubj},
		Duplicates: 2 * time.Minute,
		MaxAge:     24 * time.Hour,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensuring stream: %w", err)
	}

	return &Queue{nc: nc, js: js}, nil
}

func (q *Queue) Close() {
	q.nc.Close()
}

// Publish sends payload to the submit subject, deduped on msgID.
func (q *Queue) Publish(ctx context.Context, msgID string, payload []byte) error {
	_, err := q.js.Publish(ctx, SubmitSubj, payload, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("publishing job: %w", err)
	}
	return nil
}

// FairShareBatchSize bounds how many pending messages get regrouped for
// per-team round-robin ordering in one fetch. Fairness only holds within a
// batch; a backlog deeper than this drains its excess in arrival order.
// ponytail: fixed batch size, not a true global priority queue. Upgrade path:
// grow this or fetch repeatedly per key if a single team can flood the queue.
const FairShareBatchSize = 50

// ConsumeFairShare drains the submit subject via a durable pull consumer,
// but instead of strict FIFO it fetches a batch and interleaves messages
// round-robin by keyFunc(payload) — e.g. team — so one team's burst of
// submissions can't starve another team's jobs behind it in the queue.
func (q *Queue) ConsumeFairShare(ctx context.Context, maxDeliver int, keyFunc func([]byte) string, handler func(context.Context, []byte) error) error {
	cons, err := q.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       ConsumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    maxDeliver,
		BackOff:       []time.Duration{time.Second, 5 * time.Second, 30 * time.Second},
		FilterSubject: SubmitSubj,
	})
	if err != nil {
		return fmt.Errorf("creating consumer: %w", err)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batch, err := cons.Fetch(FairShareBatchSize, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			return fmt.Errorf("fetching batch: %w", err)
		}

		var msgs []jetstream.Msg
		for msg := range batch.Messages() {
			msgs = append(msgs, msg)
		}
		if err := batch.Error(); err != nil && len(msgs) == 0 {
			// Timed out with nothing pending; loop back and fetch again.
			continue
		}

		for _, msg := range interleaveByKey(msgs, keyFunc) {
			if err := handler(ctx, msg.Data()); err != nil {
				_ = msg.Nak()
				continue
			}
			_ = msg.Ack()
		}
	}
}

// interleaveByKey groups msgs by keyFunc while preserving each group's
// internal (arrival) order, then round-robins across groups in the order
// each key was first seen.
func interleaveByKey(msgs []jetstream.Msg, keyFunc func([]byte) string) []jetstream.Msg {
	if len(msgs) == 0 {
		return nil
	}

	groups := map[string][]jetstream.Msg{}
	var order []string
	for _, m := range msgs {
		k := keyFunc(m.Data())
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], m)
	}

	out := make([]jetstream.Msg, 0, len(msgs))
	for {
		progressed := false
		for _, k := range order {
			if len(groups[k]) == 0 {
				continue
			}
			out = append(out, groups[k][0])
			groups[k] = groups[k][1:]
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return out
}

// PublishDLQ moves a message that exhausted retries to the dead-letter subject.
func (q *Queue) PublishDLQ(ctx context.Context, payload []byte) error {
	_, err := q.js.Publish(ctx, DLQSubj, payload)
	return err
}
