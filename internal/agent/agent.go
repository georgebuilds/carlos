package agent

import (
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/tools"
)

type Config struct {
	Provider providers.Provider
	Tools    *tools.Registry
	Skills   *skills.Library
	Memory   *memory.Store
}

type Agent struct {
	cfg Config
}

func New(cfg Config) (*Agent, error) {
	return &Agent{cfg: cfg}, nil
}
