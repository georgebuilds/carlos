package providers

import "context"

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Capabilities struct {
	ParallelToolUse bool
	PromptCaching   bool
	StructuredOut   bool
	Vision          bool
}

type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolSpec
}

type Message struct {
	Role    string
	Content []Block
}

type Block struct {
	Kind       string
	Text       string
	ToolUseID  string
	ToolName   string
	ToolInput  []byte
	ToolResult []byte
}

type ToolSpec struct {
	Name        string
	Description string
	Schema      []byte
}

type EventKind int

const (
	EventTextDelta EventKind = iota
	EventToolUseStart
	EventToolUseDelta
	EventToolUseEnd
	EventStopReason
	EventError
)

type Event struct {
	Kind    EventKind
	Text    string
	ToolUse *ToolUse
	Stop    string
	Err     error
}

type ToolUse struct {
	ID    string
	Name  string
	Input []byte
}
