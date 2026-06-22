package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sguldemond/goblin/agent/internal/config"
	"github.com/sguldemond/goblin/agent/internal/k8s"
	"github.com/sguldemond/goblin/agent/internal/scout"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("goblin-scout: %v", err)
	}
}

const banner = `
  /\       /\
 /  \ ___ /  \
( ⊙  (___) ⊙ )    G O B L I N   S C O U T
 \   // \\   /     something broke. me fix.
  '--'   '--'
`

func run() error {
	fmt.Print(banner)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	restCfg, client, err := k8s.NewClient()
	if err != nil {
		return fmt.Errorf("building k8s client: %w", err)
	}

	s, err := scout.New(cfg, restCfg, client)
	if err != nil {
		return fmt.Errorf("creating scout: %w", err)
	}

	ctx := context.Background()
	if err := s.Run(ctx); err != nil {
		return fmt.Errorf("running scout: %w", err)
	}
	return nil
}
