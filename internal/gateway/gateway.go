package gateway

import "context"

type Gateway interface {
	Name() string
	Run(ctx context.Context) error
}

type CLI struct{}

func (CLI) Name() string                       { return "cli" }
func (CLI) Run(ctx context.Context) error      { <-ctx.Done(); return ctx.Err() }
