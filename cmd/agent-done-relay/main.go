package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func (r *relay) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/connect", r.websocket)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func serve(args []string, logger *log.Logger) error {
	flags := flag.NewFlagSet("agent-done-relay serve", flag.ContinueOnError)
	config := flags.String("config", "", "path to the device registry JSON")
	listen := flags.String("listen", "127.0.0.1:8443", "listen address")
	cert := flags.String("tls-cert", "", "TLS certificate PEM")
	key := flags.String("tls-key", "", "TLS private key PEM")
	dev := flags.Bool("dev-insecure", false, "allow unencrypted connections on a loopback IP for testing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *config == "" {
		return errors.New("serve requires -config and no positional arguments")
	}
	if *dev {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || !net.ParseIP(host).IsLoopback() || *cert != "" || *key != "" {
			return errors.New("-dev-insecure needs a loopback IP listen address and no TLS flags")
		}
	} else if *cert == "" || *key == "" {
		return errors.New("serve requires -tls-cert and -tls-key")
	}
	devices, err := readRegistry(*config)
	if err != nil {
		return err
	}
	r := newRelay(devices, logger)
	defer r.close()
	server := &http.Server{
		Handler: r.handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13},
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() {
		if *dev {
			done <- server.Serve(listener)
		} else {
			done <- server.ServeTLS(listener, *cert, *key)
		}
	}()
	logger.Printf("relay listening on %s", listener.Addr())
	for {
		select {
		case err := <-done:
			return err
		case received := <-signals:
			if received == syscall.SIGHUP {
				next, err := readRegistry(*config)
				if err != nil {
					logger.Print("device registry reload failed")
					continue
				}
				r.reload(next)
				logger.Print("device registry reloaded")
				continue
			}
			r.close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(ctx)
		}
	}
}

func run(args []string, logger *log.Logger) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("usage: agent-done-relay serve [flags] | credential")
		return nil
	}
	if len(args) > 0 && args[0] == "serve" {
		return serve(args[1:], logger)
	}
	if len(args) == 1 && args[0] == "credential" {
		token, digest := newCredential()
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"token": token, "token_sha256": digest})
	}
	return errors.New("usage: agent-done-relay serve [flags] | credential")
}

func main() {
	logger := log.New(os.Stderr, "agent-done-relay: ", log.LstdFlags)
	if err := run(os.Args[1:], logger); err != nil && !errors.Is(err, flag.ErrHelp) {
		logger.Print(err)
		os.Exit(1)
	}
}
