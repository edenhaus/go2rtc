package streams

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

// Global lookback registry (similar to preloads)
var lookbacks = map[*Stream]*LookbackBuffer{}
var lookbacksMu sync.Mutex

// segmentData stores a video segment with timestamp
type segmentData struct {
	data      []byte
	timestamp time.Time
}

// LookbackBuffer maintains a ring buffer of recent video segments
type LookbackBuffer struct {
	init        []byte        // MP4 init segment (ftyp + moov)
	segments    []segmentData // Ring buffer of mdat segments
	segmentIdx  int           // Current write position in ring buffer
	maxSegments int           // Maximum segments to keep
	consumer    core.Consumer // Background consumer writing to buffer
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

// AddLookback starts lookback buffering for a stream
func AddLookback(stream *Stream, seconds string) error {
	maxSeconds := 30 // default
	if seconds != "" {
		if s, err := strconv.Atoi(seconds); err == nil && s > 0 {
			maxSeconds = s
		}
	}

	lookbacksMu.Lock()
	defer lookbacksMu.Unlock()

	// Remove existing lookback if present
	if lb := lookbacks[stream]; lb != nil {
		if lb.consumer != nil {
			stream.RemoveConsumer(lb.consumer)
		}
		delete(lookbacks, stream)
	}

	// Create new lookback buffer
	lb := NewLookbackBuffer(maxSeconds)

	// Create MP4 consumer that writes to the buffer
	cons := mp4.NewConsumer(nil) // nil = default codecs (H264/H265 + AAC)
	cons.FormatName = "mp4/lookback"
	lb.consumer = cons

	// Add consumer to stream (this will start producers if needed)
	if err := stream.AddConsumer(cons); err != nil {
		return err
	}

	// Register in global map
	lookbacks[stream] = lb

	// Start writing to buffer in background
	go func() {
		_, _ = cons.WriteTo(lb)
	}()

	log.Debug().Msgf("[streams] started lookback buffer (%ds)", maxSeconds)
	return nil
}

// DelLookback stops lookback buffering for a stream
func DelLookback(stream *Stream) error {
	lookbacksMu.Lock()
	defer lookbacksMu.Unlock()

	if lb := lookbacks[stream]; lb != nil {
		if lb.consumer != nil {
			stream.RemoveConsumer(lb.consumer)
		}
		delete(lookbacks, stream)
		log.Debug().Msg("[streams] stopped lookback buffer")
		return nil
	}

	return errors.New("streams: lookback not found")
}

// Lookback is a convenience wrapper for AddLookback
func Lookback(stream *Stream, seconds string) {
	if err := AddLookback(stream, seconds); err != nil {
		log.Error().Err(err).Caller().Send()
	}
}

// GetLookbackData retrieves buffered data from a stream
func GetLookbackData(stream *Stream, seconds int) (init []byte, data []byte) {
	lookbacksMu.Lock()
	lb := lookbacks[stream]
	lookbacksMu.Unlock()

	if lb == nil {
		return nil, nil
	}

	return lb.GetData(seconds)
}
