package sse

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReaderFraming(t *testing.T) {
	in := ": comment\n\nevent: a\ndata: {\"x\":1}\n\ndata: line1\ndata: line2\r\n\r\nid: 5\ndata:nospace\n\n\n\ndata: [DONE]"
	r := NewReader(iotest.OneByteReader(strings.NewReader(in)), 0)
	type got struct{ name, data string }
	var evs []got
	var raws []string
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, got{string(ev.Name), string(ev.Data)})
		raws = append(raws, string(ev.Raw))
	}
	want := []got{{"a", `{"x":1}`}, {"", "line1\nline2"}, {"", "nospace"}, {"", "[DONE]"}}
	if len(evs) != len(want) {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, evs[i], want[i])
		}
	}
	if raws[0] != "event: a\ndata: {\"x\":1}\n\n" {
		t.Errorf("raw[0] = %q", raws[0])
	}
}

func TestReaderLongLineAndLimit(t *testing.T) {
	big := strings.Repeat("x", 100<<10)
	r := NewReader(strings.NewReader("data: "+big+"\n\n"), 0)
	ev, err := r.Next()
	if err != nil || len(ev.Data) != len(big) {
		t.Fatalf("err %v len %d", err, len(ev.Data))
	}
	r = NewReader(strings.NewReader("data: "+big+"\n\n"), 1024)
	if _, err := r.Next(); err != ErrEventTooLarge {
		t.Fatalf("want ErrEventTooLarge, got %v", err)
	}
}

func BenchmarkReader(b *testing.B) {
	ev := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello world\"}}\n\n"
	payload := strings.Repeat(ev, 1000)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := NewReader(strings.NewReader(payload), 0)
		for {
			if _, err := r.Next(); err != nil {
				break
			}
		}
	}
}
