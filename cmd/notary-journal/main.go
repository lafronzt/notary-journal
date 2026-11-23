package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lafronzt/notary-journal/internal/journal"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("notary-journal", flag.ContinueOnError)
	dbPath := flags.String("db", env("NOTARY_DB", "data/journal.sqlite3"), "SQLite database path")
	listen := flags.String("listen", env("LISTEN_ADDR", "127.0.0.1:8087"), "HTTP listen address")
	publicURL := flags.String("public-url", os.Getenv("PUBLIC_URL"), "external browser origin; required beyond loopback")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: notary-journal [options] serve|verify|sync|export FILE|restore FILE|restore-gcs JOURNAL_UUID")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 {
		flags.Usage()
		return errors.New("a command is required")
	}
	switch command[0] {
	case "serve", "verify", "sync":
		if len(command) != 1 {
			return errors.New("unexpected command arguments")
		}
	case "export", "restore", "restore-gcs":
		if len(command) != 2 {
			return errors.New("this command requires one file path or journal identifier")
		}
	default:
		return errors.New("unknown command")
	}
	store, err := journal.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer store.Close()
	archive := journal.NewArchive(store, os.Getenv("GCS_BUCKET"))
	defer archive.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	printJSON := func(v any) error {
		body, err := journal.Canonical(v)
		if err != nil {
			return err
		}
		fmt.Println(string(body))
		return nil
	}
	switch command[0] {
	case "verify":
		result, err := store.Verify()
		if err != nil {
			return err
		}
		return printJSON(result)
	case "sync":
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		result, err := archive.Sync(syncCtx)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "export":
		events, err := store.AllEvents()
		if err != nil {
			return err
		}
		if _, err = journal.VerifyEvents(events); err != nil {
			return err
		}
		body, err := journal.Canonical(journal.Bundle{Schema: journal.Schema, Events: events})
		if err != nil {
			return err
		}
		f, err := os.OpenFile(command[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(body)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		fmt.Printf("Exported %d events to encrypted-media destination of your choice. The JSON file itself is plaintext.\n", len(events))
		return nil
	case "restore":
		f, err := os.Open(command[1])
		if err != nil {
			return err
		}
		defer f.Close()
		// A personal journal export is intentionally loaded in memory for whole-chain validation.
		body, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		bundle, err := journal.ReadBundle(body)
		if err != nil {
			return err
		}
		result, err := store.Restore(bundle.Events)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "restore-gcs":
		restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		events, err := archive.RestoreEvents(restoreCtx, command[1])
		if err != nil {
			return err
		}
		result, err := store.Restore(events)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "serve":
		password := os.Getenv("NOTARY_PASSWORD")
		if file := os.Getenv("NOTARY_PASSWORD_FILE"); file != "" {
			value, err := os.ReadFile(filepath.Clean(file))
			if err != nil {
				return errors.New("cannot read NOTARY_PASSWORD_FILE")
			}
			password = strings.TrimRight(string(value), "\r\n")
		}
		app, err := journal.NewServer(store, archive, password, *publicURL, *listen)
		if err != nil {
			return err
		}
		if _, err = store.Verify(); err != nil {
			return fmt.Errorf("journal integrity check failed; refusing to serve: %w", err)
		}
		server := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx }}
		workerCtx, cancelWorker := context.WithCancel(ctx)
		defer cancelWorker()
		done := make(chan struct{})
		go func() { defer close(done); archive.Run(workerCtx) }()
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				_ = server.Close()
			}
		}()
		log.Printf("Notary Journal listening on %s", *listen)
		err = server.ListenAndServe()
		if ctx.Err() != nil {
			<-shutdownDone
		}
		cancelWorker()
		<-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return nil
}
