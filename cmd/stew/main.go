// Command stew is the FeatureSteward command-line tool.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Melmonster13/featuresteward/internal/client"
)

const usage = `stew manages FeatureSteward flags.

Usage:
  stew login --url <api-url> [--insecure]   save an API token (read from stdin)
  stew logout [--keep-token]                revoke and forget the saved token
  stew whoami                               show who you're logged in as
  stew list [--env <env>] [--steward <handle>|none]
                                            list flags

Environment: STEW_URL and STEW_TOKEN override the saved login.
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// env is everything a command needs from its surroundings, so tests can
// run commands in-process.
type env struct {
	ctx    context.Context
	getenv func(string) string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// errUsage means the command line was wrong; the message is already printed.
var errUsage = errors.New("usage")

// run executes a command and returns the exit code: 0 on success, 1 on
// failure, 2 on bad usage.
func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{ctx: ctx, getenv: getenv, stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	commands := map[string]func(*env, []string) error{
		"login":  cmdLogin,
		"logout": cmdLogout,
		"whoami": cmdWhoami,
		"list":   cmdList,
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "stew: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	err := cmd(e, args[1:])
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		fmt.Fprintf(stderr, "stew %s: %v\n", args[0], err)
		return 1
	}
}

// flags returns a FlagSet whose errors and help go to stderr.
func (e *env) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("stew "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs
}

func (e *env) parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "stew: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return errUsage
	}
	return nil
}

// client returns an API client from the saved login.
func (e *env) client() (*client.Client, config, error) {
	c, err := loadConfig(e.getenv, func(msg string) { fmt.Fprintln(e.stderr, msg) })
	if err != nil {
		return nil, c, err
	}
	return client.New(c.URL, c.Token), c, nil
}

// readSecret reads one line from stdin, hiding the input on a terminal.
func (e *env) readSecret(prompt string) (string, error) {
	if f, ok := e.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(e.stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(e.stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(e.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
