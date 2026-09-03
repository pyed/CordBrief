package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

var segmentFileRegex = regexp.MustCompile(`^(\d{16})\.ndjson$`)

// FormatSegmentFilename formats a segment number into 16-digit padded filename.
func FormatSegmentFilename(segment uint64) string {
	return fmt.Sprintf("%016d.ndjson", segment)
}

// ParseSegmentNumber extracts the segment number from a filename.
func ParseSegmentNumber(filename string) (uint64, error) {
	m := segmentFileRegex.FindStringSubmatch(filename)
	if len(m) != 2 {
		return 0, fmt.Errorf("invalid segment filename: %q (expected 16-digit .ndjson)", filename)
	}
	num, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil || num < 1 {
		return 0, fmt.Errorf("invalid segment number %q: %w", m[1], err)
	}
	return num, nil
}

// DiscoverSegments returns a sorted slice of available segment numbers in eventsDir.
func DiscoverSegments(eventsDir string) ([]uint64, error) {
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events directory: %w", err)
	}

	var segments []uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		seg, err := ParseSegmentNumber(entry.Name())
		if err == nil {
			segments = append(segments, seg)
		}
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i] < segments[j]
	})

	return segments, nil
}

// Watermark represents an immutable boundary snapshot of existing segments and their byte lengths.
type Watermark struct {
	Segments     []uint64
	SegmentSizes map[uint64]int64
	MaxSegment   uint64
}

// CaptureWatermark captures the current snapshot of all segments up to this moment.
func CaptureWatermark(eventsDir string) (*Watermark, error) {
	segments, err := DiscoverSegments(eventsDir)
	if err != nil {
		return nil, err
	}

	sizes := make(map[uint64]int64, len(segments))
	var maxSeg uint64

	for _, seg := range segments {
		path := filepath.Join(eventsDir, FormatSegmentFilename(seg))
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat segment %d: %w", seg, err)
		}
		sizes[seg] = info.Size()
		if seg > maxSeg {
			maxSeg = seg
		}
	}

	return &Watermark{
		Segments:     segments,
		SegmentSizes: sizes,
		MaxSegment:   maxSeg,
	}, nil
}

// Record bundles a parsed Event with its exact segment and byte-offset boundaries.
type Record struct {
	Event      Event
	Segment    uint64
	Offset     int64
	NextOffset int64
}

// Reader reads events sequentially from segmented NDJSON files starting from a cursor.
type Reader struct {
	eventsDir string
	watermark *Watermark
}

// NewReader creates a Reader bounded by a captured Watermark.
func NewReader(eventsDir string, watermark *Watermark) *Reader {
	return &Reader{
		eventsDir: eventsDir,
		watermark: watermark,
	}
}

// ReadBatch reads up to maxRecords starting from startCursor within the watermark boundaries.
// It returns the read records and the exact NextCursor pointing to the start of the subsequent record.
func (r *Reader) ReadBatch(startCursor Cursor, maxRecords int) ([]Record, Cursor, error) {
	if err := startCursor.Validate(); err != nil {
		return nil, startCursor, fmt.Errorf("invalid start cursor: %w", err)
	}
	if maxRecords <= 0 {
		maxRecords = 1000
	}

	currentCursor := startCursor
	var records []Record

	for _, seg := range r.watermark.Segments {
		if seg < currentCursor.Segment {
			continue
		}

		maxSize, exists := r.watermark.SegmentSizes[seg]
		if !exists || maxSize == 0 {
			continue
		}

		startOffset := int64(0)
		if seg == currentCursor.Segment {
			startOffset = currentCursor.Offset
		}

		if startOffset >= maxSize {
			// This segment has already been fully consumed up to watermark
			continue
		}

		path := filepath.Join(r.eventsDir, FormatSegmentFilename(seg))
		f, err := os.Open(path)
		if err != nil {
			return records, currentCursor, fmt.Errorf("open segment %d: %w", seg, err)
		}

		segRecords, nextOffset, err := readSegmentUpTo(f, seg, startOffset, maxSize, maxRecords-len(records))
		_ = f.Close()

		if err != nil {
			return records, currentCursor, err
		}

		records = append(records, segRecords...)
		currentCursor = Cursor{
			Version: CurrentSchemaVersion,
			Segment: seg,
			Offset:  nextOffset,
		}

		if len(records) >= maxRecords {
			break
		}
	}

	return records, currentCursor, nil
}

// readSegmentUpTo reads records from an open file starting at startOffset up to maxBytes.
func readSegmentUpTo(f *os.File, segment uint64, startOffset, maxBytes int64, limit int) ([]Record, int64, error) {
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, startOffset, fmt.Errorf("seek segment %d to offset %d: %w", segment, startOffset, err)
	}

	bytesToRead := maxBytes - startOffset
	if bytesToRead <= 0 {
		return nil, startOffset, nil
	}

	buf := make([]byte, bytesToRead)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, startOffset, fmt.Errorf("read segment %d: %w", segment, err)
	}
	buf = buf[:n]

	var records []Record
	currentPos := startOffset
	bufOffset := 0

	for bufOffset < len(buf) && len(records) < limit {
		newlineIdx := bytes.IndexByte(buf[bufOffset:], '\n')
		if newlineIdx == -1 {
			// Incomplete trailing line: line has not been terminated by \n yet.
			// Per spec, do NOT fail; leave currentPos at bufOffset and wait for complete write.
			break
		}

		lineLen := newlineIdx + 1 // includes \n
		lineBytes := buf[bufOffset : bufOffset+newlineIdx] // without \n
		recordStart := currentPos
		recordNext := currentPos + int64(lineLen)

		bufOffset += lineLen
		currentPos = recordNext

		trimmed := bytes.TrimSpace(lineBytes)
		if len(trimmed) == 0 {
			continue
		}

		var event Event
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&event); err != nil {
			return records, recordStart, fmt.Errorf("corrupted journal record at segment %d, offset %d: %w", segment, recordStart, err)
		}
		if event.Version != CurrentSchemaVersion {
			return records, recordStart, fmt.Errorf("unsupported event version %d at segment %d, offset %d", event.Version, segment, recordStart)
		}

		records = append(records, Record{
			Event:      event,
			Segment:    segment,
			Offset:     recordStart,
			NextOffset: recordNext,
		})
	}

	return records, currentPos, nil
}
