// Package queue runs deployment jobs on a fixed pool of in-process workers.
//
// Jobs live only in memory; the store is the record. At startup the Deployer
// re-enqueues deployments still marked "queued", so a restart drops nothing.
package queue

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

var ErrFull = errors.New("deploy queue is full")

type Queue struct {
	jobs chan string
}

func New(capacity int) *Queue {
	return &Queue{jobs: make(chan string, capacity)}
}

// Enqueue never blocks: an API request should fail fast rather than hang
// when the builders are saturated.
func (q *Queue) Enqueue(id string) error {
	select {
	case q.jobs <- id:
		return nil
	default:
		return ErrFull
	}
}

// Run processes jobs with the given number of workers until ctx is
// cancelled, then waits for the workers to return.
func (q *Queue) Run(ctx context.Context, workers int, handle func(context.Context, string) error, log *slog.Logger) {
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-q.jobs:
					if err := handle(ctx, id); err != nil {
						log.Error("job failed", "job", id, "err", err)
					}
				}
			}
		})
	}
	wg.Wait()
}
