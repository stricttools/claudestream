package claudestream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipeWriter starts a writer of a fresh pipe and returns it with the read end.
func pipeWriter(t *testing.T) (*stdinWriter, *os.File, *atomic.Int32) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	broke := &atomic.Int32{}
	sw := newStdinWriter(w, func() { broke.Add(1) })
	go sw.run()
	t.Cleanup(func() {
		sw.stop()
		w.Close()
		<-sw.exited
		r.Close()
	})
	return sw, r, broke
}

func TestStdinWriterKeepsEveryFrameWholeAndInSubmissionOrder(t *testing.T) {
	sw, r, _ := pipeWriter(t)
	const writers, frames = 8, 200
	lines := make(chan string, writers*frames+writers)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var wg sync.WaitGroup
	var answered atomic.Int32
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			pad := strings.Repeat("x", 1000*(g+1))
			for i := 0; i < frames; i++ {
				data := []byte(fmt.Sprintf("%d %d %s\n", g, i, pad))
				if g%2 == 0 {
					if err := sw.write(context.Background(), data); err != nil {
						t.Error(err)
						return
					}
				} else {
					done := make(chan struct{})
					sw.enqueue(context.Background(), data, func(err error) {
						if err != nil {
							t.Error(err)
						}
						answered.Add(1)
						close(done)
					})
					<-done
				}
			}
		}(g)
	}
	wg.Wait()
	next := make([]int, writers)
	for n := 0; n < writers*frames; n++ {
		var g, i int
		var pad string
		line := <-lines
		if _, err := fmt.Sscanf(line, "%d %d %s", &g, &i, &pad); err != nil {
			t.Fatalf("broken frame %q: %v", line[:min(len(line), 40)], err)
		}
		if i != next[g] || len(pad) != 1000*(g+1) {
			t.Fatalf("writer %d: frame %d arrived as %d with %d bytes", g, next[g], i, len(pad))
		}
		next[g]++
	}
	if answered.Load() != int32(writers/2*frames) {
		t.Fatalf("%d enqueued answers resolved", answered.Load())
	}
}

func TestStdinWriterWithdrawsAQueuedFrameWhoseContextEnds(t *testing.T) {
	sw, r, broke := pipeWriter(t)
	// Fill the pipe so the writer blocks on a frame nobody reads yet.
	big := []byte(strings.Repeat("a", 1<<20) + "\n")
	first := make(chan error, 1)
	go func() { first <- sw.write(context.Background(), big) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := sw.write(ctx, []byte("withdrawn\n"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued write: %v", err)
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(len(big))))
	if err != nil || len(data) != len(big) {
		t.Fatalf("read %d bytes: %v", len(data), err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := sw.write(context.Background(), []byte("after\n")); err != nil {
		t.Fatalf("a withdrawn frame broke the writer: %v", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil || line != "after\n" {
		t.Fatalf("next line %q %v (the withdrawn frame was written?)", line, err)
	}
	if broke.Load() != 0 {
		t.Fatal("the session was told stdin broke")
	}
}

func TestStdinWriterInterruptsAWriteWhoseContextEndsAndBreaksOnAPartialFrame(t *testing.T) {
	sw, r, broke := pipeWriter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Larger than the pipe buffer and never read: the write stops part-way.
	err := sw.write(ctx, []byte(strings.Repeat("b", 4<<20)+"\n"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interrupted write: %v", err)
	}
	if broke.Load() != 1 {
		t.Fatalf("broke called %d times", broke.Load())
	}
	err = sw.write(context.Background(), []byte("later\n"))
	if !errors.Is(err, ErrSessionUnusable) {
		t.Fatalf("a write after a broken frame: %v", err)
	}
	var refused error
	sw.enqueue(context.Background(), []byte("x\n"), func(err error) { refused = err })
	if !errors.Is(refused, ErrSessionUnusable) {
		t.Fatalf("an enqueue after a broken frame: %v", refused)
	}
	_ = r
}

func TestStdinWriterInterruptedBeforeAnyByteStaysUsable(t *testing.T) {
	sw, r, broke := pipeWriter(t)
	// Fill the pipe exactly so the next write cannot start.
	fill := make([]byte, 0)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := sw.write(ctx, []byte("c\n"))
		cancel()
		if err != nil {
			if broke.Load() != 0 {
				t.Skip("the pipe filled mid-frame; the case needs a frame boundary at the buffer's end")
			}
			break
		}
		fill = append(fill, 'c', '\n')
	}
	if broke.Load() != 0 {
		t.Fatal("an interrupted write that wrote nothing broke stdin")
	}
	go io.Copy(io.Discard, r)
	if err := sw.write(context.Background(), []byte("d\n")); err != nil {
		t.Fatalf("after an interrupted empty write: %v", err)
	}
}

func TestStdinWriterStopFailsQueuedFramesWithErrSessionClosed(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sw := newStdinWriter(w, func() {})
	go sw.run()
	block := make(chan error, 1)
	go func() { block <- sw.write(context.Background(), []byte(strings.Repeat("e", 4<<20)+"\n")) }()
	time.Sleep(50 * time.Millisecond)
	queued := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { queued <- sw.write(context.Background(), []byte("q\n")) }()
	}
	var answers atomic.Int32
	sw.enqueue(context.Background(), []byte("answer\n"), func(err error) {
		if errors.Is(err, ErrSessionClosed) {
			answers.Add(1)
		}
	})
	time.Sleep(50 * time.Millisecond)
	sw.stop()
	w.Close()
	<-sw.exited
	if err := <-block; !errors.Is(err, ErrSessionClosed) {
		t.Errorf("the write in progress: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := <-queued; !errors.Is(err, ErrSessionClosed) {
			t.Errorf("a queued write: %v", err)
		}
	}
	if answers.Load() != 1 {
		t.Errorf("the queued answer was resolved %d times with ErrSessionClosed", answers.Load())
	}
	if err := sw.write(context.Background(), []byte("late\n")); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("a write after stop: %v", err)
	}
}

func TestStdinWriterRefusesAnEndedContextAtOnce(t *testing.T) {
	sw, _, _ := pipeWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sw.write(ctx, []byte("x\n")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
