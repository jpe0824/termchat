package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrFrameTooLarge is returned when a frame exceeds MaxFrame bytes.
var ErrFrameTooLarge = errors.New("proto: frame too large")

// Codec reads and writes newline-delimited JSON frames. Read must be called
// from one goroutine at a time; Write may be called concurrently.
type Codec struct {
	r  *bufio.Reader
	mu sync.Mutex
	w  io.Writer
}

// NewCodec wraps rw.
func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{r: bufio.NewReader(rw), w: rw}
}

// Read returns the next frame. It stops buffering once a line passes
// MaxFrame and returns ErrFrameTooLarge; the stream is then positioned
// mid-line and unusable, so the caller must close the connection.
func (c *Codec) Read() (Frame, error) {
	for {
		line, err := c.readLine()
		if err != nil {
			return Frame{}, err
		}
		if len(line) == 0 {
			continue
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return Frame{}, fmt.Errorf("proto: bad frame: %w", err)
		}
		return f, nil
	}
}

func (c *Codec) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxFrame+1 { // +1 for the newline
			return nil, ErrFrameTooLarge
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// Write sends f as one line. A frame larger than MaxFrame is refused with
// ErrFrameTooLarge before anything is written.
func (c *Codec) Write(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return ErrFrameTooLarge
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(b)
	return err
}
