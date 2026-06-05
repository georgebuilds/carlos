package tui

import "errors"

type Mode int

const (
	ModeChat Mode = iota
	ModePlan
	ModeManage
	ModeDiff
	ModeSkills
)

type App struct {
	mode Mode
}

func New() *App { return &App{mode: ModeChat} }

func (a *App) Run() error {
	return errors.New("tui: not wired (bubbletea integration pending)")
}
