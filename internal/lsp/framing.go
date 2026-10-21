package lsp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const maxMessage = 64 << 20

// ReadMessage reads one Content-Length framed message.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	first := true
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && first && line == "" {
				return nil, io.EOF
			}
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		first = false
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				return nil, errors.New("lsp: missing Content-Length")
			}
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > maxMessage {
				return nil, fmt.Errorf("lsp: bad Content-Length %q", v)
			}
			length = n
		}
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// WriteMessage writes one framed message in a single Write.
func WriteMessage(w io.Writer, body []byte) error {
	b := append([]byte("Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"), body...)
	_, err := w.Write(b)
	return err
}

// queue is an unbounded ordered queue drained by one goroutine, so callers
// (bus handlers) never block.
type queue struct {
	mu     sync.Mutex
	cv     *sync.Cond
	items  [][]byte
	closed bool
	fin    func()
}

func newQueue(send func([]byte)) *queue {
	q := &queue{}
	q.cv = sync.NewCond(&q.mu)
	go func() {
		for {
			q.mu.Lock()
			for len(q.items) == 0 && !q.closed && q.fin == nil {
				q.cv.Wait()
			}
			if q.closed {
				q.mu.Unlock()
				return
			}
			if len(q.items) == 0 {
				f := q.fin
				q.mu.Unlock()
				f()
				return
			}
			b := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			send(b)
		}
	}()
	return q
}

func (q *queue) push(b []byte) {
	q.mu.Lock()
	if !q.closed && q.fin == nil {
		q.items = append(q.items, b)
		q.cv.Signal()
	}
	q.mu.Unlock()
}

// close discards pending items and stops the goroutine.
func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.items = nil
	q.cv.Broadcast()
	q.mu.Unlock()
}

// finish drains pending items, then runs fn on the queue goroutine.
func (q *queue) finish(fn func()) {
	q.mu.Lock()
	if q.fin == nil && !q.closed {
		q.fin = fn
	}
	q.cv.Broadcast()
	q.mu.Unlock()
}
