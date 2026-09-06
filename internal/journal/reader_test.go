package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createTestEventLine(version int, msgID, content string) []byte {
	ts := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	return []byte(fmt.Sprintf(`{"version":%d,"event":"message_create","message_id":"%s","guild_id":"g1","channel_id":"c1","timestamp":"%s","captured_at":"%s","author":{"id":"a1","name":"alice","display_name":"Alice","bot":false},"content":"%s"}`+"\n",
		version, msgID, ts, ts, content))
}

func TestReader_DiscoverSegments(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create out of order
	_ = os.WriteFile(filepath.Join(eventsDir, "0000000000000003.ndjson"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(eventsDir, "0000000000000001.ndjson"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(eventsDir, "0000000000000002.ndjson"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(eventsDir, "not_a_segment.tmp"), []byte(""), 0644)

	segs, err := DiscoverSegments(eventsDir)
	if err != nil {
		t.Fatalf("DiscoverSegments error: %v", err)
	}

	if len(segs) != 3 || segs[0] != 1 || segs[1] != 2 || segs[2] != 3 {
		t.Fatalf("unexpected segments: %v", segs)
	}
}

func TestReader_ReadBatchAndOffsets(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	seg1File := filepath.Join(eventsDir, "0000000000000001.ndjson")
	l1 := createTestEventLine(1, "m1", "first")
	l2 := createTestEventLine(1, "m2", "second")
	l3 := createTestEventLine(1, "m3", "third")

	data := append(append(l1, l2...), l3...)
	if err := os.WriteFile(seg1File, data, 0644); err != nil {
		t.Fatal(err)
	}

	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader := NewReader(eventsDir, wm)
	startCur := Cursor{Version: 1, Segment: 1, Offset: 0}

	// Read batch with limit 2
	records, nextCur, err := reader.ReadBatch(startCur, 2)
	if err != nil {
		t.Fatalf("ReadBatch error: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Event.MessageID != "m1" || records[0].Offset != 0 || records[0].NextOffset != int64(len(l1)) {
		t.Fatalf("unexpected record 0: %+v", records[0])
	}
	if records[1].Event.MessageID != "m2" || records[1].Offset != int64(len(l1)) || records[1].NextOffset != int64(len(l1)+len(l2)) {
		t.Fatalf("unexpected record 1: %+v", records[1])
	}

	expectedNext := Cursor{Version: 1, Segment: 1, Offset: int64(len(l1) + len(l2))}
	if nextCur != expectedNext {
		t.Fatalf("expected next cursor %+v, got %+v", expectedNext, nextCur)
	}

	// Read remaining record
	records2, nextCur2, err := reader.ReadBatch(nextCur, 10)
	if err != nil {
		t.Fatalf("ReadBatch remaining error: %v", err)
	}
	if len(records2) != 1 || records2[0].Event.MessageID != "m3" {
		t.Fatalf("unexpected remaining records: %+v", records2)
	}

	expectedFinal := Cursor{Version: 1, Segment: 1, Offset: int64(len(data))}
	if nextCur2 != expectedFinal {
		t.Fatalf("expected final cursor %+v, got %+v", expectedFinal, nextCur2)
	}
}

func TestReader_IncompleteTrailingLine(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	seg1File := filepath.Join(eventsDir, "0000000000000001.ndjson")
	l1 := createTestEventLine(1, "m1", "complete record")
	partial := []byte(`{"version":1,"event":"message_create","message_id":"m2"`) // no \n

	data := append(l1, partial...)
	if err := os.WriteFile(seg1File, data, 0644); err != nil {
		t.Fatal(err)
	}

	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader := NewReader(eventsDir, wm)
	records, nextCur, err := reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err != nil {
		t.Fatalf("expected incomplete line to be ignored without error, got: %v", err)
	}

	if len(records) != 1 || records[0].Event.MessageID != "m1" {
		t.Fatalf("expected only m1, got %d records", len(records))
	}
	// Cursor should stop at the boundary of m1, waiting for m2 to complete
	if nextCur.Offset != int64(len(l1)) {
		t.Fatalf("cursor offset should be %d, got %d", len(l1), nextCur.Offset)
	}
}

func TestReader_MalformedRecordFailsLoudly(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	seg1File := filepath.Join(eventsDir, "0000000000000001.ndjson")
	l1 := createTestEventLine(1, "m1", "valid")
	corrupt := []byte("{corrupt json line}\n")
	data := append(l1, corrupt...)

	if err := os.WriteFile(seg1File, data, 0644); err != nil {
		t.Fatal(err)
	}

	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader := NewReader(eventsDir, wm)
	_, _, err = reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err == nil {
		t.Fatal("expected loud error on corrupt complete record, got nil")
	}
}

func TestReader_WatermarkIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	seg1File := filepath.Join(eventsDir, "0000000000000001.ndjson")
	l1 := createTestEventLine(1, "m1", "initial")
	if err := os.WriteFile(seg1File, l1, 0644); err != nil {
		t.Fatal(err)
	}

	// Capture watermark BEFORE later appends
	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Later append happens after watermark
	f, err := os.OpenFile(seg1File, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	l2 := createTestEventLine(1, "m2", "appended after watermark")
	if _, err := f.Write(l2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Also new segment 2 added after watermark
	seg2File := filepath.Join(eventsDir, "0000000000000002.ndjson")
	_ = os.WriteFile(seg2File, createTestEventLine(1, "m3", "seg2 after watermark"), 0644)

	// Reader bounded by earlier watermark must ONLY see m1
	reader := NewReader(eventsDir, wm)
	records, nextCur, err := reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err != nil {
		t.Fatalf("ReadBatch error: %v", err)
	}

	if len(records) != 1 || records[0].Event.MessageID != "m1" {
		t.Fatalf("watermark isolation violated: got %d records", len(records))
	}
	if nextCur.Offset != int64(len(l1)) {
		t.Fatalf("next cursor offset mismatch: got %d, want %d", nextCur.Offset, len(l1))
	}
}

func TestReader_CrossSegmentTransition(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	l1 := createTestEventLine(1, "seg1_m1", "msg 1")
	l2 := createTestEventLine(1, "seg2_m1", "msg 2")

	_ = os.WriteFile(filepath.Join(eventsDir, "0000000000000001.ndjson"), l1, 0644)
	_ = os.WriteFile(filepath.Join(eventsDir, "0000000000000002.ndjson"), l2, 0644)

	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader := NewReader(eventsDir, wm)
	records, nextCur, err := reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err != nil {
		t.Fatalf("ReadBatch across segments error: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records across 2 segments, got %d", len(records))
	}
	if records[0].Segment != 1 || records[0].Event.MessageID != "seg1_m1" {
		t.Fatalf("record 0 segment mismatch: %+v", records[0])
	}
	if records[1].Segment != 2 || records[1].Event.MessageID != "seg2_m1" {
		t.Fatalf("record 1 segment mismatch: %+v", records[1])
	}
	if nextCur.Segment != 2 || nextCur.Offset != int64(len(l2)) {
		t.Fatalf("expected next cursor to point to end of segment 2: %+v", nextCur)
	}
}

// TestReader_WatermarkPartialLineRace explicitly verifies the watermark boundary when a line is partially written:
// 1. Collector writes half of a JSON record (no \n).
// 2. Core captures watermark 1.
// 3. Reader 1 sees only the partial record and does NOT consume it; cursor remains at last complete boundary.
// 4. Collector completes the record after watermark 1 was taken.
// 5. Batch 1 does not contain it.
// 6. Next watermark/batch contains it exactly once.
func TestReader_WatermarkPartialLineRace(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	segFile := filepath.Join(eventsDir, "0000000000000001.ndjson")
	m1 := createTestEventLine(1, "m1", "first complete record")
	if err := os.WriteFile(segFile, m1, 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Collector writes half of record m2 (no newline)
	f, err := os.OpenFile(segFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	partialM2 := []byte(`{"version":1,"event":"message_create","message_id":"m2","content":"half-w`)
	if _, err := f.Write(partialM2); err != nil {
		t.Fatal(err)
	}

	// Step 2: Core captures Watermark 1 while m2 is only half written
	wm1, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Step 3 & 5: Reader 1 reads batch 1 under Watermark 1
	reader1 := NewReader(eventsDir, wm1)
	startCur := Cursor{Version: 1, Segment: 1, Offset: 0}
	batch1, curAfterBatch1, err := reader1.ReadBatch(startCur, 10)
	if err != nil {
		t.Fatalf("batch 1 read error: %v", err)
	}

	if len(batch1) != 1 || batch1[0].Event.MessageID != "m1" {
		t.Fatalf("batch 1 must contain ONLY m1, got %d records", len(batch1))
	}
	// Committed cursor must remain on the boundary of m1 (byte offset = len(m1))
	if curAfterBatch1.Offset != int64(len(m1)) {
		t.Fatalf("cursor offset must remain at boundary of m1 (%d), got %d", len(m1), curAfterBatch1.Offset)
	}

	// Step 4: Collector finishes writing the remainder of record m2
	remainderM2 := []byte(`ritten"}` + "\n")
	if _, err := f.Write(remainderM2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Step 6: Core captures Watermark 2 and reads next batch starting from committed cursor
	wm2, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader2 := NewReader(eventsDir, wm2)
	batch2, curAfterBatch2, err := reader2.ReadBatch(curAfterBatch1, 10)
	if err != nil {
		t.Fatalf("batch 2 read error: %v", err)
	}

	if len(batch2) != 1 || batch2[0].Event.MessageID != "m2" {
		t.Fatalf("batch 2 must contain m2 exactly once, got: %+v", batch2)
	}

	totalExpectedLen := int64(len(m1) + len(partialM2) + len(remainderM2))
	if curAfterBatch2.Offset != totalExpectedLen {
		t.Fatalf("cursor after batch 2 mismatch: got %d, want %d", curAfterBatch2.Offset, totalExpectedLen)
	}
}

func TestCalculateBacklog(t *testing.T) {
	// Case A: cursor in segment 1, active segment 1
	wmA := &Watermark{
		Segments:     []uint64{1},
		SegmentSizes: map[uint64]int64{1: 500},
		MaxSegment:   1,
	}
	curA := &Cursor{Version: 1, Segment: 1, Offset: 100}
	unconsumedA, totalA := CalculateBacklog(curA, wmA)
	if unconsumedA != 400 || totalA != 500 {
		t.Errorf("Case A mismatch: got unconsumed=%d, total=%d (want 400, 500)", unconsumedA, totalA)
	}

	// Case B: cursor in segment 1, active segment 2
	wmB := &Watermark{
		Segments:     []uint64{1, 2},
		SegmentSizes: map[uint64]int64{1: 500, 2: 300},
		MaxSegment:   2,
	}
	curB := &Cursor{Version: 1, Segment: 1, Offset: 100}
	unconsumedB, totalB := CalculateBacklog(curB, wmB)
	if unconsumedB != 700 || totalB != 800 {
		t.Errorf("Case B mismatch: got unconsumed=%d, total=%d (want 700, 800)", unconsumedB, totalB)
	}

	// Case C: cursor in middle of segment 2, active segment 3
	hmC := &Watermark{
		Segments:     []uint64{1, 2, 3},
		SegmentSizes: map[uint64]int64{1: 500, 2: 400, 3: 150},
		MaxSegment:   3,
	}
	curC := &Cursor{Version: 1, Segment: 2, Offset: 200}
	unconsumedC, totalC := CalculateBacklog(curC, hmC)
	if unconsumedC != 350 || totalC != 1050 {
		t.Errorf("Case C mismatch: got unconsumed=%d, total=%d (want 350, 1050)", unconsumedC, totalC)
	}

	// Case D: zero backlog
	wmD := &Watermark{
		Segments:     []uint64{1},
		SegmentSizes: map[uint64]int64{1: 500},
		MaxSegment:   1,
	}
	curD := &Cursor{Version: 1, Segment: 1, Offset: 500}
	unconsumedD, totalD := CalculateBacklog(curD, wmD)
	if unconsumedD != 0 || totalD != 500 {
		t.Errorf("Case D mismatch: got unconsumed=%d, total=%d (want 0, 500)", unconsumedD, totalD)
	}

	// Case E: empty closed segment where legal (size 0)
	wmE := &Watermark{
		Segments:     []uint64{1},
		SegmentSizes: map[uint64]int64{1: 0},
		MaxSegment:   1,
	}
	curE := &Cursor{Version: 1, Segment: 1, Offset: 0}
	unconsumedE, totalE := CalculateBacklog(curE, wmE)
	if unconsumedE != 0 || totalE != 0 {
		t.Errorf("Case E mismatch: got unconsumed=%d, total=%d (want 0, 0)", unconsumedE, totalE)
	}

	// Case F: missing/corrupt journal state fails safely and does not display negative
	unconsumedF1, totalF1 := CalculateBacklog(nil, nil)
	if unconsumedF1 < 0 || totalF1 < 0 {
		t.Errorf("Case F1 mismatch: got negative unconsumed=%d, total=%d", unconsumedF1, totalF1)
	}
	curF2 := &Cursor{Version: 1, Segment: 99, Offset: 99999}
	unconsumedF2, totalF2 := CalculateBacklog(curF2, wmA)
	if unconsumedF2 < 0 || totalF2 < 0 {
		t.Errorf("Case F2 mismatch: got negative unconsumed=%d, total=%d", unconsumedF2, totalF2)
	}
}

func TestJournalSchemaEvolution(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}

	segPath := filepath.Join(eventsDir, "0000000000000001.ndjson")

	// 1. Event with unknown additive fields (e.g. emitted by newer collector version)
	additiveEvent := `{"version":1,"event":"message_create","message_id":"msg-additive-1","guild_id":"g1","channel_id":"c1","timestamp":"2026-09-06T12:00:00Z","captured_at":"2026-09-06T12:00:01Z","author":{"id":"a1","username":"user1","bot":false},"content":"hello additive","thread_id":"th-99","custom_meta":{"flag":true}}` + "\n"
	if err := os.WriteFile(segPath, []byte(additiveEvent), 0644); err != nil {
		t.Fatal(err)
	}

	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatal(err)
	}

	reader := NewReader(eventsDir, wm)
	records, _, err := reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err != nil {
		t.Fatalf("expected additive unknown fields to be accepted under ADDITIVE policy, got error: %v", err)
	}
	if len(records) != 1 || records[0].Event.MessageID != "msg-additive-1" {
		t.Fatalf("expected record to parse successfully, got %+v", records)
	}

	// 2. Event with unsupported schema version (e.g. version 2 breaking change) -> MUST fail fail-closed
	unsupportedVerEvent := `{"version":2,"event":"message_create","message_id":"msg-ver-2","guild_id":"g1","channel_id":"c1","timestamp":"2026-09-06T12:00:00Z","captured_at":"2026-09-06T12:00:01Z","author":{"id":"a1","username":"user1","bot":false},"content":"breaking"}` + "\n"
	if err := os.WriteFile(segPath, []byte(unsupportedVerEvent), 0644); err != nil {
		t.Fatal(err)
	}
	wm2, _ := CaptureWatermark(eventsDir)
	reader2 := NewReader(eventsDir, wm2)
	_, _, err = reader2.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err == nil || !strings.Contains(err.Error(), "unsupported event version 2") {
		t.Fatalf("expected error rejecting unsupported schema version 2, got: %v", err)
	}
}

