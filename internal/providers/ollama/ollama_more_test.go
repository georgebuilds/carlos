package ollama

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestName_ReturnsOllama(t *testing.T) {
	c := New("http://localhost:11434")
	if got := c.Name(); got != "ollama" {
		t.Errorf("Name=%q want ollama", got)
	}
}

func TestConcat_Empty(t *testing.T) {
	if got := concat(nil); got != "" {
		t.Errorf("concat(nil)=%q want empty", got)
	}
}

func TestConcat_Single(t *testing.T) {
	if got := concat([]string{"hello"}); got != "hello" {
		t.Errorf("concat=%q want hello", got)
	}
}

func TestConcat_MultipleNoSeparator(t *testing.T) {
	got := concat([]string{"Hello", ", ", "Boss", "."})
	want := "Hello, Boss."
	if got != want {
		t.Errorf("concat=%q want %q", got, want)
	}
}

func TestConcat_PreservesContiguousWhitespace(t *testing.T) {
	got := concat([]string{"a ", "b ", "c"})
	if got != "a b c" {
		t.Errorf("concat=%q want %q", got, "a b c")
	}
}

func TestSynthesizeToolUseID_Prefix(t *testing.T) {
	id := synthesizeToolUseID()
	if !strings.HasPrefix(id, "ollama-tu-") {
		t.Errorf("synthesizeToolUseID=%q should start with ollama-tu-", id)
	}
	if len(id) < len("ollama-tu-")+8 {
		t.Errorf("id=%q too short", id)
	}
}

func TestSynthesizeToolUseID_DistinctAcrossCalls(t *testing.T) {
	a := synthesizeToolUseID()
	b := synthesizeToolUseID()
	if a == b {
		t.Errorf("two ids collided: %q", a)
	}
}

func TestIsContextCancellation_CanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isContextCancellation(ctx, nil) {
		t.Error("cancelled ctx should be detected")
	}
}

func TestIsContextCancellation_DeadlineErr(t *testing.T) {
	if !isContextCancellation(context.Background(), context.DeadlineExceeded) {
		t.Error("DeadlineExceeded err should be detected")
	}
}

func TestIsContextCancellation_OtherErr(t *testing.T) {
	if isContextCancellation(context.Background(), errors.New("unrelated")) {
		t.Error("unrelated err should not be classified as cancellation")
	}
}

func TestIsContextCancellation_NoErrNoCancel(t *testing.T) {
	if isContextCancellation(context.Background(), nil) {
		t.Error("live ctx + nil err should not be cancellation")
	}
}
