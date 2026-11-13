package scionic

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
)

// WorkerPanic is what a panic on one of this library's own goroutines is
// raised again as, on the goroutine of the caller. Value is what was
// panicked with, and Stack is where.
//
// A panic on a goroutine that nobody waits for ends the whole program, and
// no caller can catch it. So this library catches a panic where it starts
// work, lets the other work end, and raises it again where the caller can
// see it: nothing waits for a goroutine that is gone, and a panic reaches
// the caller as a panic.
type WorkerPanic struct {
	Value any
	Stack []byte
}

func (p *WorkerPanic) Error() string {
	return fmt.Sprintf("panic on a worker goroutine: %v\n%s", p.Value, p.Stack)
}

// Unwrap gives the value that was panicked with, when it is an error.
func (p *WorkerPanic) Unwrap() error {
	err, _ := p.Value.(error)
	return err
}

// caught keeps the first panic of a group of goroutines.
type caught struct {
	lock  sync.Mutex
	first *WorkerPanic
}

// note is deferred on a goroutine. If the goroutine is panicking, it keeps
// the panic, calls onPanic if there is one, and lets the goroutine end.
func (c *caught) note(onPanic func()) {
	value := recover()
	if value == nil {
		return
	}
	panicked, again := value.(*WorkerPanic)
	if !again {
		panicked = &WorkerPanic{Value: value, Stack: debug.Stack()}
	}
	c.lock.Lock()
	if c.first == nil {
		c.first = panicked
	}
	c.lock.Unlock()
	if onPanic != nil {
		onPanic()
	}
}

// raise panics again with the first panic that was kept, if there is one.
func (c *caught) raise() {
	c.lock.Lock()
	first := c.first
	c.lock.Unlock()
	if first != nil {
		panic(first)
	}
}

// workerCount gives how many goroutines to work on: what was asked for, or
// one for each core when nothing was.
func workerCount(asked int) int {
	if asked <= 0 {
		return runtime.GOMAXPROCS(0)
	}
	return asked
}

// runWorkers runs work on count goroutines, and gives what each of them
// ended with, in their order. With one, the work is done here. A panic in
// one of them is raised again here once all of them have ended; onPanic, if
// there is one, is called as soon as it happens, to tell the others to
// stop.
func runWorkers(count int, onPanic func(), work func(worker int) error) []error {
	results := make([]error, max(count, 1))
	if count <= 1 {
		results[0] = work(0)
		return results
	}
	var panics caught
	var wait sync.WaitGroup
	for worker := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer panics.note(onPanic)
			results[worker] = work(worker)
		}()
	}
	wait.Wait()
	panics.raise()
	return results
}
