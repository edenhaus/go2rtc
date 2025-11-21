package streams

import (
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

// segmentData stores a video segment with timestamp
type segmentData struct {
	data      []byte
	timestamp time.Time
}

// LookbackBuffer maintains a ring buffer of recent video segments
type LookbackBuffer struct {
	init        []byte          // MP4 init segment (ftyp + moov)
	segments    []segmentData   // Ring buffer of mdat segments
	segmentIdx  int             // Current write position in ring buffer
	maxSegments int             // Maximum segments to keep
	consumer    core.Consumer   // Background consumer writing to buffer
	mu          sync.Mutex
}

// NewLookbackBuffer creates a new lookback buffer
func NewLookbackBuffer(maxSeconds int) *LookbackBuffer {
	// Assume ~0.5s per segment (typical keyframe interval)
	maxSegments := maxSeconds * 2
	if maxSegments < 20 {
		maxSegments = 20 // Minimum 10 seconds
	}
	if maxSegments > 120 {
		maxSegments = 120 // Maximum 60 seconds
	}

	return &LookbackBuffer{
		maxSegments: maxSegments,
		segments:    make([]segmentData, maxSegments),
	}
}

// Write implements io.Writer - called by MP4 consumer
func (lb *LookbackBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if lb.init == nil {
		// First write is the init segment (ftyp + moov boxes)
		lb.init = make([]byte, len(p))
		copy(lb.init, p)
	} else {
		// Subsequent writes are mdat segments - store in ring buffer
		segCopy := make([]byte, len(p))
		copy(segCopy, p)
		lb.segments[lb.segmentIdx] = segmentData{
			data:      segCopy,
			timestamp: time.Now(),
		}
		lb.segmentIdx = (lb.segmentIdx + 1) % lb.maxSegments
	}

	return len(p), nil
}

// GetData retrieves buffered segments from the last 'seconds' seconds
// Returns init segment and concatenated segment data
func (lb *LookbackBuffer) GetData(seconds int) (init []byte, data []byte) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if lb.init == nil {
		return nil, nil
	}

	cutoffTime := time.Now().Add(-time.Duration(seconds) * time.Second)
	var buffer []byte

	// Collect segments from ring buffer within the lookback period
	// Iterate from oldest to newest
	for i := 0; i < lb.maxSegments; i++ {
		idx := (lb.segmentIdx + i) % lb.maxSegments
		seg := lb.segments[idx]

		if seg.data == nil {
			continue
		}

		if seg.timestamp.After(cutoffTime) {
			buffer = append(buffer, seg.data...)
		}
	}

	if len(buffer) == 0 {
		return nil, nil
	}

	return lb.init, buffer
}

// StartLookbackBuffer starts a background MP4 consumer for buffering
func (s *Stream) StartLookbackBuffer() error {
	s.mu.Lock()
	if s.lookback != nil {
		s.mu.Unlock()
		return nil // Already started
	}

	// Create lookback buffer (default 30 seconds)
	s.lookback = NewLookbackBuffer(30)

	// Create MP4 consumer that writes to the buffer
	cons := mp4.NewConsumer(nil) // nil = default codecs (H264/H265 + AAC)
	cons.FormatName = "mp4/lookback"
	s.lookback.consumer = cons
	s.mu.Unlock()

	// Add consumer (this will start producers if needed)
	// Must be done without holding lock to avoid deadlock
	if err := s.AddConsumer(cons); err != nil {
		s.mu.Lock()
		s.lookback = nil
		s.mu.Unlock()
		return err
	}

	// Start writing to buffer in background
	go func() {
		_, _ = cons.WriteTo(s.lookback)
	}()

	log.Debug().Msg("[streams] started lookback buffer")
	return nil
}

// stopLookbackBuffer stops the background buffering consumer
func (s *Stream) stopLookbackBuffer() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lookback == nil {
		return
	}

	if s.lookback.consumer != nil {
		_ = s.lookback.consumer.Stop()
		// Remove from consumers list
		for i, cons := range s.consumers {
			if cons == s.lookback.consumer {
				s.consumers = append(s.consumers[:i], s.consumers[i+1:]...)
				break
			}
		}
	}

	s.lookback = nil
	log.Debug().Msg("[streams] stopped lookback buffer")
}

// GetLookbackData retrieves buffered data from the stream
func (s *Stream) GetLookbackData(seconds int) (init []byte, data []byte) {
	s.mu.Lock()
	lb := s.lookback
	s.mu.Unlock()

	if lb == nil {
		return nil, nil
	}

	return lb.GetData(seconds)
}

// HasLookbackBuffer returns true if lookback buffering is active
func (s *Stream) HasLookbackBuffer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookback != nil
}
