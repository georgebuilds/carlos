package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// wrongModel is any tea.Model that is NOT a sessionPickerModel, used to
// exercise pickerResult's defensive type-assertion guard.
type wrongModel struct{}

func (wrongModel) Init() tea.Cmd                       { return nil }
func (wrongModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return wrongModel{}, nil }
func (wrongModel) View() string                        { return "" }

func TestPickerResult_ReturnsChosen(t *testing.T) {
	got, err := pickerResult(sessionPickerModel{chosen: "sess-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sess-123" {
		t.Errorf("chosen = %q, want sess-123", got)
	}
}

func TestPickerResult_Cancelled(t *testing.T) {
	_, err := pickerResult(sessionPickerModel{cancelled: true})
	if !errors.Is(err, errPickerCancelled) {
		t.Errorf("err = %v, want errPickerCancelled", err)
	}
}

// A final model of the wrong type returns an error instead of panicking.
func TestPickerResult_WrongModelType(t *testing.T) {
	_, err := pickerResult(wrongModel{})
	if err == nil || !strings.Contains(err.Error(), "unexpected final model") {
		t.Fatalf("want unexpected-model error, got %v", err)
	}
}
