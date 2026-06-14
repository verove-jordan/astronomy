package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/report"
	"github.com/verove-jordan/astronomy/internal/store"
)

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the inventory as JSON")
	save := fs.Bool("save", false, "persist the inventory to the database as a session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: astrostack inspect [--json] [--save] <dir>")
	}
	dir := fs.Arg(0)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
	}

	ctx := context.Background()
	inv, err := inspect.Scan(ctx, dir)
	if err != nil {
		return err
	}

	if *save {
		cfg := config.Load()
		st, err := store.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer st.Close()
		id, err := st.SaveInventory(ctx, inv)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "saved session #%d (%d frames)\n", id, len(inv.Frames)+len(inv.Videos))
	}

	if *asJSON {
		out, err := report.InventoryJSON(inv)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	fmt.Print(report.InventoryText(inv))
	return nil
}
