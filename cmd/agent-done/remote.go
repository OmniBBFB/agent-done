package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
)

func (s *Service) serveRemote(ctx context.Context) {
	configHome := envOr("XDG_CONFIG_HOME", filepath.Join(homeDir(), ".config"))
	config, err := remote.ReadConfig(filepath.Join(configHome, "agent-done", "remote.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		catalog := codexCatalog{socket: codexSocket(), snapshot: s.remoteSnapshot}
		readOnly := remote.Handler{Catalog: catalog}
		ledger, ledgerErr := remote.OpenLedger(s.state.db)
		if ledgerErr != nil && len(config.ControlPeers) != 0 {
			log.Printf("remote control unavailable: %v", ledgerErr)
		}
		catalog.control = ledgerErr == nil
		controller := remote.Handler{Catalog: catalog, Controls: catalog, Ledger: ledger}
		err = remote.Serve(ctx, config, func(ctx context.Context, peer string, clear json.RawMessage) []byte {
			if ledgerErr == nil && slices.Contains(config.ControlPeers, peer) {
				return controller.Handle(ctx, peer, clear)
			}
			return readOnly.Handle(ctx, peer, clear)
		}, s.remoteUpdates, log.Default())
	}
	if err != nil {
		log.Printf("remote disabled: %v", err)
	}
}

func (s *Service) remoteSnapshot() (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.state.snapshot()
	if err != nil {
		return nil, err
	}
	for i := range snapshot.Sessions {
		session := &snapshot.Sessions[i]
		if updated := s.remoteActivity[session.ID]; updated > session.Updated {
			session.Updated = updated
		}
	}
	return snapshot, nil
}

func runRemote(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: agent-done remote {request|keygen}")
	}
	if args[0] == "keygen" {
		flags := flag.NewFlagSet("remote keygen", flag.ContinueOnError)
		path := flags.String("out", "", "private key JSON file to create")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || flags.NArg() != 0 {
			return errors.New("remote keygen requires -out")
		}
		private, public, err := remote.NewKeys()
		if err != nil {
			return err
		}
		data, err := json.Marshal(map[string][]byte{"private_key": private, "public_key": public})
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(append(data, '\n'))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			os.Remove(*path)
			return err
		}
		return printJSON(map[string][]byte{"public_key": public})
	}
	if args[0] != "request" {
		return errors.New("用法: agent-done remote {request|keygen}")
	}
	flags := flag.NewFlagSet("remote "+args[0], flag.ContinueOnError)
	configPath := flags.String("config", "", "path to the local device configuration")
	target := flags.String("to", "", "paired workstation ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *target == "" {
		return errors.New("remote requires -config and request also needs -to")
	}
	config, err := remote.ReadConfig(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	const maxInput = 256 << 10
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	if err != nil {
		return err
	}
	if len(data) > maxInput {
		return errors.New("remote request is too large")
	}
	requestCtx, stop := context.WithTimeout(ctx, 18*time.Second)
	defer stop()
	reply, err := remote.Request(requestCtx, config, *target, data)
	if err != nil {
		return err
	}
	return printJSON(reply)
}
