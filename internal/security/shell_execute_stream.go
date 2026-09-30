package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ConfigureShellCmdForAgentExecute 与 exec 工具一致：非交互 stdin、pager/TERM 环境、独立进程组。
func ConfigureShellCmdForAgentExecute(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	applyDefaultTerminalEnv(cmd)
	attachNonInteractiveStdin(cmd)
	_ = prepareShellCmdSession(cmd)
}

// TerminateShellCmdTree 尽力终止 shell 及其子进程组（与 exec/execute 超时取消一致）。
func TerminateShellCmdTree(cmd *exec.Cmd) {
	terminateCmdTree(cmd)
}

// TerminateShellCmdSession 使用 Start 时缓存的进程组 ID 终止（shell 已退出时仍有效）。
func TerminateShellCmdSession(session *ShellSession) {
	TerminateShellSession(session)
}

// ShellEvent is one streaming result: an output chunk, a final exit code, or an error.
// The SDK-facing adapter turns it into whatever envelope the agent framework expects;
// the process rules below never see that type.
type ShellEvent struct {
	Output   string
	ExitCode *int
	Err      error
}

// ShellSink is where the streaming shell writes. Send returns whether the consumer has
// gone away (the framework's cancel signal), Close ends the stream.
type ShellSink interface {
	Send(event ShellEvent) bool
	Close()
}

// RunShellStreaming executes a command and streams its output into sink, with the same
// behaviour the exec tool has: stdout and stderr are read concurrently in fixed-size
// chunks rather than line by line. Draining stdout first - which is what the stock ADK
// local shell does - hides stderr (a sudo password prompt, say) for as long as the command
// still has output to give, and the UI keeps showing "running".
//
// A background request, or a command that ends in `&`, starts a managed session and reports
// once; everything else streams to completion.
func RunShellStreaming(ctx context.Context, command string, runInBackendGround bool, sink ShellSink) {
	if strings.TrimSpace(command) == "" {
		_ = sink.Send(ShellEvent{Err: fmt.Errorf("command is required")})
		sink.Close()
		return
	}
	if runInBackendGround || IsBackgroundShellCommand(command) {
		go runShellInBackground(ctx, command, sink)
		return
	}
	go streamShellForeground(ctx, command, sink)
}

func runShellInBackground(ctx context.Context, command string, w ShellSink) {
	defer w.Close()

	command = strings.TrimSpace(command)
	if IsBackgroundShellCommand(command) {
		command = strings.TrimSpace(strings.TrimSuffix(command, "&"))
	}
	session, err := StartManagedBackground(ctx, "/bin/sh", command, "")
	if err != nil {
		_ = w.Send(ShellEvent{Err: err})
		return
	}
	exitCode := 0
	_ = w.Send(ShellEvent{
		Output:   fmt.Sprintf("command started in background (process group %d); cleaned up when this task ends\n", session.rootPID),
		ExitCode: &exitCode,
	})
}

func drainShellPipes(stdout, stderr io.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, stderr)
	}()
	wg.Wait()
}

func streamShellForeground(ctx context.Context, command string, w ShellSink) {
	defer w.Close()

	command = PrepareShellCommandForExecute(command)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	applyDefaultTerminalEnv(cmd)
	attachNonInteractiveStdin(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = w.Send(ShellEvent{Err: fmt.Errorf("failed to create stdout pipe: %w", err)})
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		_ = w.Send(ShellEvent{Err: fmt.Errorf("failed to create stderr pipe: %w", err)})
		return
	}
	session, err := StartShellSessionContext(ctx, cmd)
	if err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		_ = w.Send(ShellEvent{Err: fmt.Errorf("failed to start command: %w", err)})
		return
	}

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			TerminateShellCmdSession(session)
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	readStop := make(chan struct{})
	defer close(readStop)
	chunks := make(chan string, 64)
	var wg sync.WaitGroup
	readFn := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-readStop:
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}

	wg.Add(2)
	go readFn(stdoutPipe)
	go readFn(stderrPipe)
	go func() {
		wg.Wait()
		close(chunks)
	}()

	hadOutput := false
	for chunk := range chunks {
		if chunk == "" {
			continue
		}
		hadOutput = true
		if w.Send(ShellEvent{Output: chunk}) {
			TerminateShellCmdSession(session)
			go func() { _ = session.Wait() }()
			return
		}
	}

	waitErr := session.Wait()
	if waitErr == nil {
		exitCode := 0
		_ = w.Send(ShellEvent{ExitCode: &exitCode})
		return
	}

	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		exitCode := exitError.ExitCode()
		event := ShellEvent{ExitCode: &exitCode}
		if !hadOutput {
			event.Output = FormatCommandFailureResult(exitCode, "")
		}
		_ = w.Send(event)
		return
	}
	_ = w.Send(ShellEvent{Err: fmt.Errorf("command failed: %w", waitErr)})
}
