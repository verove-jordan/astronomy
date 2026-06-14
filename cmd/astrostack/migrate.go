package main

import (
	"context"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/store"
)

func runMigrate(args []string) error {
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}
	cfg := config.Load()
	ctx := context.Background()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	switch direction {
	case "up":
		n, err := st.Migrate(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("migrations applied: %d (schema up to date)\n", n)
		return nil
	case "down":
		if err := st.MigrateDown(ctx); err != nil {
			return err
		}
		fmt.Println("rolled back the last migration")
		return nil
	default:
		return fmt.Errorf("usage: astrostack migrate [up|down]")
	}
}
