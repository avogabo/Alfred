package runner

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

// runCommand runs a command and streams stdout/stderr line-by-line to onLine.
func runCommand(ctx context.Context, onLine func(string), name string, args ...string) error {
	return runCommandInDir(ctx, "", onLine, name, args...)
}

// runCommandInDir runs a command from cwd (if provided) and streams stdout/stderr line-by-line.
func runCommandInDir(ctx context.Context, cwd string, onLine func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8 -Dsun.jnu.encoding=UTF-8",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	consume := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		// allow long lines (some tools can log big JSON)
		buf := make([]byte, 0, 64*1024)
		s.Buffer(buf, 1024*1024)
		for s.Scan() {
			onLine(s.Text())
		}
	}

	go consume(stdout)
	go consume(stderr)

	wg.Wait()
	return cmd.Wait()
}
