package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type panicModel struct{}

func (panicModel) Init() tea.Cmd                       { return func() tea.Msg { return "boom" } }
func (panicModel) Update(tea.Msg) (tea.Model, tea.Cmd) { panic("update failed") }
func (panicModel) View() tea.View                      { return tea.NewView("") }

// The framework catches a panic in Update, restores the terminal and makes
// Run return ErrProgramPanic; the interface turns that into one line.
func TestTUIPanicIsReportedInOneLine(t *testing.T) {
	p := tea.NewProgram(panicModel{}, tea.WithInput(&bytes.Buffer{}), tea.WithOutput(io.Discard))
	_, err := p.Run()
	if !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatalf("Run = %v, want ErrProgramPanic", err)
	}
	got := tuiExit(err)
	want := "the chat interface stopped on an internal error; unsent drafts in it were lost"
	if got == nil || got.Error() != want || strings.Contains(got.Error(), "\n") {
		t.Errorf("tuiExit = %v, want %q", got, want)
	}
	if other := fmt.Errorf("x"); tuiExit(other) != other || tuiExit(nil) != nil {
		t.Error("tuiExit changed an error that is not a panic")
	}
}
