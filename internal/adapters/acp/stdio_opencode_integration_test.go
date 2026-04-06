package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexus/internal/domain"
)

func TestStdioClientOpenCodeFileReadIntegration(t *testing.T) {
	ctx, client, sessionID, workdir, mode := newOpenCodeIntegrationSession(t)
	token := "OPENCODE_CALLBACK_TOKEN_7f6a1c2b"
	if err := os.WriteFile(filepath.Join(workdir, "callback_test.txt"), []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}

	run, events, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_stdio_opencode_read", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: mode},
		Message: domain.Message{
			Text: "Read the file callback_test.txt in the current working directory and reply with only its exact contents. Do not add quotes or any extra words.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOpenCodeRunCompleted(t, run)
	if got := joinedEventText(events); !strings.Contains(got, token) {
		t.Fatalf("expected output to contain callback token %q, got %q", token, got)
	}

	status := client.RuntimeStatus()
	if !status.Running || !status.Initialized {
		t.Fatalf("unexpected stdio runtime status after live run: %+v", status)
	}
	t.Logf("opencode file-read callback counts: %+v", status.CallbackCounts)
}

func TestStdioClientOpenCodeFileWriteIntegration(t *testing.T) {
	ctx, client, sessionID, workdir, mode := newOpenCodeIntegrationSession(t)
	token := "OPENCODE_WRITE_TOKEN_3c9d4e1f"
	targetPath := filepath.Join(workdir, "callback_written.txt")

	run, events, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_stdio_opencode_write", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: mode},
		Message: domain.Message{
			Text: "Use the file editing tool to create a file named callback_written.txt in the current working directory with the exact contents " + token + ". Then reply with only the exact contents you wrote.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOpenCodeRunCompleted(t, run)
	if got := joinedEventText(events); !strings.Contains(got, token) {
		t.Fatalf("expected output to contain write token %q, got %q", token, got)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != token {
		t.Fatalf("unexpected file contents: %q", string(data))
	}

	status := client.RuntimeStatus()
	t.Logf("opencode file-write callback counts: %+v", status.CallbackCounts)
}

func TestStdioClientOpenCodeTerminalIntegration(t *testing.T) {
	ctx, client, sessionID, _, mode := newOpenCodeIntegrationSession(t)
	token := "OPENCODE_TERMINAL_TOKEN_51ad9b7c"

	run, events, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_stdio_opencode_terminal", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: mode},
		Message: domain.Message{
			Text: "Use the terminal tool to run a command that prints exactly " + token + ". Reply with only the exact terminal output.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOpenCodeRunCompleted(t, run)
	if got := joinedEventText(events); !strings.Contains(got, token) {
		t.Fatalf("expected output to contain terminal token %q, got %q", token, got)
	}

	status := client.RuntimeStatus()
	t.Logf("opencode terminal callback counts: %+v", status.CallbackCounts)
}

func newOpenCodeIntegrationSession(t *testing.T) (context.Context, *StdioClient, string, string, string) {
	t.Helper()
	if os.Getenv("NEXUS_INTEGRATION_OPENCODE") != "1" {
		t.Skip("set NEXUS_INTEGRATION_OPENCODE=1 to run real opencode stdio integration")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not found in PATH")
	}

	workdir := t.TempDir()
	mode := strings.TrimSpace(os.Getenv("NEXUS_INTEGRATION_OPENCODE_MODE"))
	if mode == "" {
		mode = "build"
	}
	client := NewStdioClient(StdioConfig{
		Command:          "opencode",
		Args:             []string{"acp", "--pure", "--cwd", workdir},
		Workdir:          workdir,
		DefaultAgentName: mode,
		StartupTimeout:   20 * time.Second,
		RPCTimeout:       2 * time.Minute,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	agents, err := client.DiscoverAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one discovered agent")
	}

	sessionID, err := client.EnsureSession(ctx, domain.Session{ID: "session_stdio_opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if sessionID == "" {
		t.Fatal("expected acp session id")
	}
	return ctx, client, sessionID, workdir, mode
}

func assertOpenCodeRunCompleted(t *testing.T, run domain.Run) {
	t.Helper()
	if run.ACPRunID == "" {
		t.Fatalf("expected run id, got %+v", run)
	}
	if run.Status != "completed" && run.Status != "running" {
		t.Fatalf("unexpected run status: %+v", run)
	}
}

func joinedEventText(events []domain.RunEvent) string {
	var output strings.Builder
	for _, event := range events {
		if strings.TrimSpace(event.Text) != "" {
			if output.Len() > 0 {
				output.WriteString("\n")
			}
			output.WriteString(strings.TrimSpace(event.Text))
		}
	}
	return output.String()
}
