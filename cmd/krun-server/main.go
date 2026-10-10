package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/krun-dev/krun/internal/control"
	"github.com/krun-dev/krun/internal/localize"
	"os"
	"os/signal"
	"syscall"
)

func run() (result error) {
	lang, args, err := localize.Options(os.Args[1:])
	defer func() {
		if errors.Is(result, flag.ErrHelp) {
			result = nil
		}
		result = localize.Error(lang, result)
	}()
	if err != nil {
		return err
	}
	command := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(localize.Writer(lang, os.Stderr))
	config := flags.String("config", "config/development.yaml", localize.Text(lang, "Server configuration"))
	user := flags.String("user", "", localize.Text(lang, "Existing local user_id"))
	source := flags.String("source", "", localize.Text(lang, "Identity source"))
	subject := flags.String("subject", "", localize.Text(lang, "Stable external subject"))
	if e := flags.Parse(args); e != nil {
		return e
	}
	c, e := control.LoadConfig(*config)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx = localize.WithLanguage(ctx, lang)
	if command == "serve" {
		return control.Serve(ctx, c)
	}
	s, e := control.OpenStore(ctx, c.DB)
	if e != nil {
		return e
	}
	defer s.Close()
	switch command {
	case "bootstrap":
		return control.Bootstrap(ctx, s, *user)
	case "invite":
		return control.Invite(ctx, s, *source, *subject)
	default:
		return fmt.Errorf("unknown command %q; use serve, bootstrap or invite", command)
	}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
