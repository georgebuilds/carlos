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

// Block is one typed content block inside a Message. Kind selects the
// variant:
//
//	"" / "text"   - Text
//	"image"       - MediaType + ImageData (use ImageBlock to construct)
//	"tool_use"    - ToolUseID + ToolName + ToolInput
//	"tool_result" - ToolUseID + ToolResult
//
// Adapters reject kinds they don't know; "image" additionally requires
// the provider to support vision (check Capabilities.Vision before
// building image blocks - adapters for non-vision providers degrade
// or reject, see each package's toAPIMessages).
type Block struct {
	Kind       string
	Text       string
	MediaType  string // image: IANA media type, e.g. "image/png"
	ImageData  []byte // image: raw (NOT base64) encoded image bytes
	ToolUseID  string
	ToolName   string
	ToolInput  []byte
	ToolResult []byte
}

// ImageBlock constructs an image content block from raw encoded image
// bytes (PNG/JPEG/GIF/WebP) and their IANA media type. The data is
// carried raw; each adapter applies its own wire encoding (base64
// source block for Anthropic, data: URL content-part for the OpenAI-
// compatible providers).
func ImageBlock(mediaType string, data []byte) Block {
	return Block{Kind: "image", MediaType: mediaType, ImageData: data}
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
