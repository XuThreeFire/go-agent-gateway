package main

import (
	"strings"
	"testing"
)

func TestReadEventsDeduplicatesAndStops(t *testing.T) {
	input := "id: 7:1\nevent: turn.delta\ndata: {\"delta\":\"old\"}\n\nid: 7:2\nevent: turn.delta\ndata: {\"delta\":\"new\"}\n\nid: 7:3\nevent: run.terminal\ndata: {\"status\":\"completed\"}\n\n"
	after := int64(1)
	var out strings.Builder
	done, err := readEvents(strings.NewReader(input), "7", &after, &out)
	if err != nil || !done || after != 3 || strings.Contains(out.String(), "old") || !strings.Contains(out.String(), "new") {
		t.Fatalf("done=%v seq=%d text=%q err=%v", done, after, out.String(), err)
	}
}
func TestReadEventsKeepsCursorOnPartialFrame(t *testing.T) {
	after := int64(2)
	var out strings.Builder
	done, err := readEvents(strings.NewReader("id: 7:3\nevent: turn.delta\ndata: {\"delta\":\"x\"}"), "7", &after, &out)
	if done || err != nil || after != 2 || out.Len() != 0 {
		t.Fatal("不完整帧不得推进游标")
	}
}
