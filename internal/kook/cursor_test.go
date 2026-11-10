package kook

import (
	"encoding/json"
	"testing"
)

func TestCursorDuplicateDoesNotMove(t *testing.T) {
	c := &snCursor{}
	good := json.RawMessage(`{"type":255,"extra":{"type":"joined_channel"}}`)
	if got := c.Push(1, good); len(got.Events) != 1 || c.maxSn != 1 {
		t.Fatalf("first %+v max %d", got, c.maxSn)
	}
	if got := c.Push(1, good); len(got.Events) != 0 || got.Poison || c.maxSn != 1 {
		t.Fatalf("dup %+v max %d", got, c.maxSn)
	}
	if got := c.Push(0, good); c.maxSn != 1 || len(got.Events) != 0 {
		t.Fatalf("older %+v max %d", got, c.maxSn)
	}
}

func TestCursorAheadBadStaysPut(t *testing.T) {
	c := &snCursor{maxSn: 4}
	got := c.Push(10, json.RawMessage(`{"type":1}`))
	if got.Poison || got.Fatal || c.maxSn != 4 || len(c.buf) != 0 {
		t.Fatalf("ahead bad %+v max %d buf %d", got, c.maxSn, len(c.buf))
	}
}

func TestCursorReordersThenDrains(t *testing.T) {
	c := &snCursor{}
	frame := func(extra string) json.RawMessage {
		return json.RawMessage(`{"type":255,"extra":{"type":"` + extra + `"}}`)
	}
	if got := c.Push(2, frame("exited_channel")); len(got.Events) != 0 || c.maxSn != 0 {
		t.Fatalf("buffered early %+v max %d", got, c.maxSn)
	}
	got := c.Push(1, frame("joined_channel"))
	if c.maxSn != 2 || len(got.Events) != 2 {
		t.Fatalf("drain %+v max %d", got, c.maxSn)
	}
	if got.Events[0].SN != 1 || got.Events[1].SN != 2 {
		t.Fatalf("order %+v", got.Events)
	}
	if got.Events[0].ExtraType != "joined_channel" || got.Events[1].ExtraType != "exited_channel" {
		t.Fatalf("types %+v", got.Events)
	}
}

func TestCursorNumericExtraIsNotPoison(t *testing.T) {
	c := &snCursor{}
	one := c.Push(1, json.RawMessage(`{"type":1,"target_id":"20","extra":{"type":1}}`))
	nine := c.Push(2, json.RawMessage(`{"type":9,"extra":{"type":9}}`))
	if one.Poison || nine.Poison || len(one.Events) != 1 || len(nine.Events) != 1 {
		t.Fatalf("one %+v nine %+v", one, nine)
	}
	if one.Events[0].Type != 1 || one.Events[0].ExtraType != "1" || one.Events[0].ChannelID != "20" {
		t.Fatalf("type1 %+v", one.Events[0])
	}
	if nine.Events[0].Type != 9 || nine.Events[0].ExtraType != "9" {
		t.Fatalf("type9 %+v", nine.Events[0])
	}
}

func TestCursorEightPoisonsThenADrainedSuccessClearsFatal(t *testing.T) {
	c := &snCursor{}
	good := json.RawMessage(`{"type":255,"extra":{"type":"joined_channel"}}`)
	if got := c.Push(9, good); got.Fatal || c.maxSn != 0 {
		t.Fatalf("buffer %+v max %d", got, c.maxSn)
	}
	for sn := int64(1); sn <= 7; sn++ {
		got := c.Push(sn, json.RawMessage(`{"type":1}`))
		if !got.Poison || got.Fatal {
			t.Fatalf("sn %d %+v", sn, got)
		}
	}
	got := c.Push(8, json.RawMessage(`{"type":1}`))
	if got.Fatal {
		t.Fatal("a following good frame must clear the failure streak")
	}
	if c.maxSn != 9 || c.fails != 0 {
		t.Fatalf("max %d fails %d", c.maxSn, c.fails)
	}
	if len(got.Events) != 1 || got.Events[0].SN != 9 {
		t.Fatalf("drained %+v", got.Events)
	}
}

func TestCursorEightPoisonsAreFatal(t *testing.T) {
	c := &snCursor{}
	var last snPush
	for sn := int64(1); sn <= 8; sn++ {
		last = c.Push(sn, json.RawMessage(`{"type":1}`))
		if sn < 8 && last.Fatal {
			t.Fatalf("fatal too early at %d", sn)
		}
	}
	if !last.Fatal || c.maxSn != 8 || c.fails != 8 {
		t.Fatalf("last %+v max %d fails %d", last, c.maxSn, c.fails)
	}
}
