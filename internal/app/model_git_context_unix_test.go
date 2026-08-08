//go:build darwin || linux

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
)

type callTrackingProvider struct {
	called bool
}

func (p *callTrackingProvider) Compile(context.Context, provider.Request) ([]provider.Candidate, error) {
	p.called = true
	return nil, nil
}

type callTrackingInventorySource struct {
	called bool
}

func (source *callTrackingInventorySource) Inventory(context.Context) capability.Inventory {
	source.called = true
	return capability.Inventory{}
}

func TestAlreadyCancelledCompileDoesNotStartGitCollection(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(repo, ".git", "HEAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	p := &callTrackingProvider{}
	inventorySource := &callTrackingInventorySource{}
	m := NewFromDeps(Deps{Provider: p, InventorySource: inventorySource})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := make(chan tea.Msg, 1)
	go func() { results <- m.compile(ctx, 41)() }()

	var result compileResult
	select {
	case msg := <-results:
		result = msg.(compileResult)
	case <-time.After(time.Second):
		t.Fatal("already-cancelled compile blocked on Git metadata")
	}

	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("compile error = %v, want context.Canceled", result.err)
	}
	if result.context != (machinecontext.Context{}) {
		t.Fatalf("cancelled compile collected context = %#v, want zero value", result.context)
	}
	if inventorySource.called {
		t.Fatal("cancelled compile collected capability inventory")
	}
	if p.called {
		t.Fatal("cancelled compile called provider")
	}
}

func TestEscapeCancelsCompileWithHostileGitMetadata(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "fifo git entry",
			setup: func(t *testing.T, repo string) {
				t.Helper()
				if err := unix.Mkfifo(filepath.Join(repo, ".git"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversize head",
			setup: func(t *testing.T, repo string) {
				t.Helper()
				gitDir := filepath.Join(repo, ".git")
				if err := os.Mkdir(gitDir, 0o755); err != nil {
					t.Fatal(err)
				}
				head := "ref: refs/heads/" + strings.Repeat("x", 16*1024)
				if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			tt.setup(t, repo)
			t.Chdir(repo)

			started := make(chan struct{})
			m := NewFromDeps(Deps{
				Provider:        cancellingProvider{started: started},
				InventorySource: staticInventorySource{},
			})
			m.input.SetValue("show status")
			updated, compileCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)

			results := make(chan tea.Msg, 1)
			go func() { results <- compileCmd() }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("compile did not pass hostile Git metadata")
			}

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = updated.(Model)
			if cmd != nil || m.screen != screenInput || m.input.Value() != "show status" {
				t.Fatalf("Escape: cmd nil=%v screen=%v input=%q, want nil/input/restored intent", cmd == nil, m.screen, m.input.Value())
			}

			var result compileResult
			select {
			case msg := <-results:
				result = msg.(compileResult)
			case <-time.After(time.Second):
				t.Fatal("compile did not return after Escape")
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("compile error = %v, want context.Canceled", result.err)
			}

			updated, _ = m.Update(result)
			m = updated.(Model)
			if m.screen != screenInput || m.err != nil || m.Command() != "" {
				t.Fatalf("late cancelled result mutated state: screen=%v err=%v command=%q", m.screen, m.err, m.Command())
			}
		})
	}
}
