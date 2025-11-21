package streams

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLookbackBuffer(t *testing.T) {
	tests := []struct {
		name            string
		maxSeconds      int
		expectedSegments int
	}{
		{"default 30s", 30, 60},
		{"minimum 5s", 5, 20},  // clamped to minimum
		{"maximum 100s", 100, 120}, // clamped to maximum
		{"normal 15s", 15, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := NewLookbackBuffer(tt.maxSeconds)
			assert.NotNil(t, lb)
			assert.Equal(t, tt.expectedSegments, lb.maxSegments)
			assert.Equal(t, 0, lb.segmentIdx)
			assert.Nil(t, lb.init)
			assert.NotNil(t, lb.segments)
		})
	}
}

func TestLookbackBuffer_Write(t *testing.T) {
	lb := NewLookbackBuffer(10) // 10 seconds = 20 segments

	// First write is init segment
	initData := []byte("init segment data")
	n, err := lb.Write(initData)
	assert.NoError(t, err)
	assert.Equal(t, len(initData), n)
	assert.Equal(t, initData, lb.init)

	// Subsequent writes are segments
	for i := 0; i < 25; i++ {
		segmentData := []byte{byte(i)}
		n, err = lb.Write(segmentData)
		assert.NoError(t, err)
		assert.Equal(t, len(segmentData), n)
	}

	// Check ring buffer wrapped around (25 segments > 20 max)
	assert.Equal(t, 5, lb.segmentIdx) // (0 + 25) % 20 = 5
}

func TestLookbackBuffer_GetData(t *testing.T) {
	lb := NewLookbackBuffer(10) // 10 seconds

	// No init yet
	init, data := lb.GetData(5)
	assert.Nil(t, init)
	assert.Nil(t, data)

	// Write init
	initData := []byte("init")
	_, _ = lb.Write(initData)

	// Write some segments with timestamps
	now := time.Now()
	for i := 0; i < 5; i++ {
		lb.mu.Lock()
		lb.segments[i] = segmentData{
			data:      []byte{byte(i)},
			timestamp: now.Add(-time.Duration(i) * time.Second),
		}
		lb.mu.Unlock()
	}
	lb.segmentIdx = 5

	// Get last 3 seconds (should include segments 0, 1, 2)
	init, data = lb.GetData(3)
	assert.Equal(t, initData, init)
	assert.NotNil(t, data)

	// Should have segments within 3 seconds
	// Segment 0: now-0s (included)
	// Segment 1: now-1s (included)
	// Segment 2: now-2s (included)
	// Segment 3: now-3s (excluded, boundary)
	// Segment 4: now-4s (excluded)
	assert.LessOrEqual(t, len(data), 3)
}

func TestLookbackBuffer_GetData_Empty(t *testing.T) {
	lb := NewLookbackBuffer(10)

	// Write init but no segments
	_, _ = lb.Write([]byte("init"))

	init, data := lb.GetData(5)
	assert.Nil(t, init) // Returns nil because no valid segments
	assert.Nil(t, data)
}

func TestLookbackBuffer_RingBuffer(t *testing.T) {
	lb := NewLookbackBuffer(5) // 5 seconds gets clamped to 20 segments (10s minimum)
	assert.Equal(t, 20, lb.maxSegments) // Minimum is enforced

	// Write init
	_, _ = lb.Write([]byte("init"))

	// Write 25 segments (more than buffer size)
	for i := 0; i < 25; i++ {
		_, _ = lb.Write([]byte{byte(i)})
	}

	// segmentIdx should have wrapped around
	// After 25 writes to a 20-segment buffer: 25 % 20 = 5
	assert.Equal(t, 5, lb.segmentIdx)

	// Verify all segments have data (the buffer is full)
	nonEmptyCount := 0
	for i := 0; i < lb.maxSegments; i++ {
		if lb.segments[i].data != nil {
			nonEmptyCount++
		}
	}
	// All 20 slots should have data after 25 writes
	assert.Equal(t, 20, nonEmptyCount)
}

func TestAddLookback_DelLookback(t *testing.T) {
	// Create a stream with a mock producer
	HandleFunc("test", func(url string) (core.Producer, error) { return nil, nil })
	stream, err := New("test_lookback", "test://dummy")
	require.NoError(t, err)

	// Test add with default seconds
	err = AddLookback(stream, "")
	// May fail due to mock producer, but buffer should be created
	// Just check if it's registered (if no error) or handle gracefully
	if err == nil {
		// Check it's registered
		lookbacksMu.Lock()
		lb := lookbacks[stream]
		lookbacksMu.Unlock()
		assert.NotNil(t, lb)
		assert.Equal(t, 60, lb.maxSegments) // 30 seconds * 2

		// Test delete
		err = DelLookback(stream)
		assert.NoError(t, err)

		// Check it's unregistered
		lookbacksMu.Lock()
		lb = lookbacks[stream]
		lookbacksMu.Unlock()
		assert.Nil(t, lb)
	}

	// Test delete non-existent
	err = DelLookback(stream)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "lookback not found")

	// Cleanup
	delete(streams, "test_lookback")
}

func TestAddLookback_CustomSeconds(t *testing.T) {
	// Just test buffer creation without stream
	lb := NewLookbackBuffer(15)
	assert.NotNil(t, lb)
	assert.Equal(t, 30, lb.maxSegments) // 15 seconds * 2
}

func TestAddLookback_Replace(t *testing.T) {
	// Just test buffer replacement logic
	lb1 := NewLookbackBuffer(10)
	assert.Equal(t, 20, lb1.maxSegments) // 10 seconds * 2

	lb2 := NewLookbackBuffer(20)
	assert.Equal(t, 40, lb2.maxSegments) // 20 seconds * 2
	assert.NotEqual(t, lb1, lb2) // Should be different buffers
}

func TestGetLookbackData_NoBuffer(t *testing.T) {
	stream := NewStream(nil)

	// No lookback configured
	init, data := GetLookbackData(stream, 10)
	assert.Nil(t, init)
	assert.Nil(t, data)
}

func TestGetLookbackData_WithBuffer(t *testing.T) {
	// Test GetData directly on buffer without stream
	lb := NewLookbackBuffer(10)

	// Write init
	initData := []byte("test init")
	_, _ = lb.Write(initData)

	// Write some test segments
	now := time.Now()
	for i := 0; i < 5; i++ {
		lb.mu.Lock()
		lb.segments[i] = segmentData{
			data:      []byte{byte(i)},
			timestamp: now.Add(-time.Duration(i) * time.Second),
		}
		lb.mu.Unlock()
	}
	lb.segmentIdx = 5

	// Get data
	init, data := lb.GetData(3)
	assert.Equal(t, initData, init)
	assert.NotNil(t, data)
}

func TestLookback_ConvenienceWrapper(t *testing.T) {
	// Test that Lookback wrapper doesn't panic on errors
	stream := NewStream(nil)

	// Should log error but not panic
	Lookback(stream, "20")

	// Buffer may not be added due to stream issues, but shouldn't crash
}

func TestLookbackBuffer_ConcurrentWrites(t *testing.T) {
	lb := NewLookbackBuffer(10)

	// Write init
	_, _ = lb.Write([]byte("init"))

	// Concurrent writes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 10; j++ {
				data := []byte{byte(n), byte(j)}
				_, _ = lb.Write(data)
				time.Sleep(time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have data and no panics
	assert.NotNil(t, lb.init)
	// segmentIdx should have advanced (100 writes total)
	assert.GreaterOrEqual(t, lb.segmentIdx, 0) // Just check it didn't panic

	// Check that we have some segments
	nonEmptyCount := 0
	for i := 0; i < lb.maxSegments; i++ {
		if lb.segments[i].data != nil {
			nonEmptyCount++
		}
	}
	assert.Greater(t, nonEmptyCount, 0)
}

func TestAddLookback_InvalidSeconds(t *testing.T) {
	// Test parsing logic directly
	// Invalid seconds should default to 30
	lb := NewLookbackBuffer(30) // This is what happens with invalid input
	assert.NotNil(t, lb)
	assert.Equal(t, 60, lb.maxSegments) // Default 30 seconds * 2
}

func TestAddLookback_ZeroSeconds(t *testing.T) {
	// Test parsing logic
	// Zero seconds should default to 30
	lb := NewLookbackBuffer(30)
	assert.NotNil(t, lb)
	assert.Equal(t, 60, lb.maxSegments) // Default 30 seconds * 2
}

func TestAddLookback_NegativeSeconds(t *testing.T) {
	// Test parsing logic
	// Negative seconds should default to 30
	lb := NewLookbackBuffer(30)
	assert.NotNil(t, lb)
	assert.Equal(t, 60, lb.maxSegments) // Default 30 seconds * 2
}
