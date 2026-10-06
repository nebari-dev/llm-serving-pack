package store

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// A full queue drops content; under the load that fills it, a line per drop
// would bury the log. It says so on the first drop, then every 1,000th.
func TestContentWriterLogsDropsSparingly(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	w := &ContentWriter{ch: make(chan CapturedContent)} // nothing reads it: every Put drops
	for range 2500 {
		w.Put(CapturedContent{ReceiptID: "r"})
	}
	if n := w.Dropped.Load(); n != 2500 {
		t.Fatalf("dropped %d", n)
	}
	if lines := strings.Count(buf.String(), "\n"); lines != 3 {
		t.Errorf("%d log lines for 2,500 drops, want 3:\n%s", lines, buf.String())
	}
}
