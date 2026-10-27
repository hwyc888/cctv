package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

type segmentFetchResult struct {
	index int
	data  []byte
	err   error
}

// fetchSegmentsInOrder overlaps HTTP requests while leaving decryption and
// file writes on the caller's goroutine. Dispatching only after consumption
// bounds buffered segments to the configured concurrency.
func fetchSegmentsInOrder(ctx context.Context, client *http.Client, segments []hlsSegment, concurrency int, consume func(int, []byte) error) error {
	if len(segments) == 0 {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(segments) {
		concurrency = len(segments)
	}

	ctx, cancel := context.WithCancel(ctx)
	requests := make(chan int)
	results := make(chan segmentFetchResult, concurrency)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-requests:
					if !ok {
						return
					}
					data, err := fetchBytes(ctx, client, segments[index].URL)
					select {
					case results <- segmentFetchResult{index: index, data: data, err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() {
		cancel()
		workers.Wait()
	}()

	nextRequest := 0
	for ; nextRequest < concurrency; nextRequest++ {
		select {
		case requests <- nextRequest:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	pending := make(map[int][]byte, concurrency)
	nextSegment := 0
	for nextSegment < len(segments) {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-results:
			if result.err != nil {
				return fmt.Errorf("下载最高画质分片 %d/%d: %w", result.index+1, len(segments), result.err)
			}
			pending[result.index] = result.data
			for {
				data, ready := pending[nextSegment]
				if !ready {
					break
				}
				delete(pending, nextSegment)
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := consume(nextSegment, data); err != nil {
					return err
				}
				nextSegment++
				if nextRequest < len(segments) {
					select {
					case requests <- nextRequest:
						nextRequest++
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
	}
	return nil
}
