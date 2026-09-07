// Package main implements the ARES unified CLI.
//
// This file adds the `recall` command tree for querying the round archive.
// Round archives persist conversation rounds as independent JSON files so
// they survive event-stream compaction — recall gives operators a way to
// search past rounds by keyword or inspect a specific round by number.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime/archive"
)

var recallCmd = &cobra.Command{
	Use:   "recall",
	Short: "Query round archives (survive compaction)",
	Long: `Query the round archive — conversation rounds persisted as independent
round_N.json files under the configured archive directory.

Archiving is enabled by default. Disable with memory.archive.enabled: false
in the config YAML.

Subcommands:
  recall query <text>   Search archives by keyword and print matching rounds.
  recall round <N>      Print a specific round's archive record as JSON.`,
}

var recallQueryCmd = &cobra.Command{
	Use:   "query <text>",
	Short: "Search archives by keyword",
	Long: `Search archived rounds for the given keyword (case-insensitive substring
match across summary, decisions, file paths, and identifier refs). Prints a
human-readable conclusion for each matching round, newest first.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRecallQuery(args[0])
	},
}

var recallRoundCmd = &cobra.Command{
	Use:   "round <N>",
	Short: "Print a specific round's archive record",
	Long: `Print the round_N.json archive record as pretty-printed JSON. The round
number must be a positive integer.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRecallRound(args[0])
	},
}

var recallConfigPath string

func init() {
	rootCmd.AddCommand(recallCmd)
	recallCmd.AddCommand(recallQueryCmd)
	recallCmd.AddCommand(recallRoundCmd)
	for _, c := range []*cobra.Command{recallQueryCmd, recallRoundCmd} {
		c.Flags().StringVarP(&recallConfigPath, "config", "c", "", "Path to config YAML")
	}
}

// loadRecallConfig resolves the config path (falling back to ares.yaml,
// mirroring loadServeConfig), loads it, and applies environment overrides.
// Returns the loaded config or a wrapped error.
func loadRecallConfig() (*ares_config.Config, error) {
	configPath := recallConfigPath
	if configPath == "" {
		for _, p := range []string{
			"ares.yaml",
			"./ares.yaml",
		} {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
		if configPath == "" {
			configPath = "ares.yaml"
		}
	}

	cfg, err := ares_config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := ares_config.LoadFromEnv(cfg); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	return cfg, nil
}

// runRecallQuery searches the archive directory for rounds matching the query
// and prints a human-readable conclusion. When the archive directory does not
// exist (no rounds archived yet), it prints a friendly message and returns nil.
func runRecallQuery(query string) error {
	cfg, err := loadRecallConfig()
	if err != nil {
		return err
	}
	if !cfg.Memory.Archive.IsEnabled() {
		return errors.New("archive is disabled in config (set memory.archive.enabled: true or omit it)")
	}

	reader, err := archive.NewFileArchiveReader(cfg.Memory.Archive.Dir)
	if err != nil {
		return fmt.Errorf("create archive reader: %w", err)
	}

	// Handle a missing archive directory gracefully so a fresh deployment
	// gets a friendly message instead of an error.
	if _, statErr := os.Stat(cfg.Memory.Archive.Dir); errors.Is(statErr, os.ErrNotExist) {
		fmt.Printf("no archive directory found at %s\n", cfg.Memory.Archive.Dir)
		return nil
	}

	out, err := reader.Recall(context.Background(), query)
	if err != nil {
		return fmt.Errorf("recall query %q: %w", query, err)
	}
	fmt.Println(out)
	return nil
}

// runRecallRound prints a single round's archive record as pretty-printed JSON.
// The round argument must be a positive integer. A missing round file yields a
// friendly "not found" message rather than a raw error.
func runRecallRound(arg string) error {
	n, err := strconv.Atoi(arg)
	if err != nil {
		return fmt.Errorf("invalid round number %q: must be a positive integer", arg)
	}
	if n <= 0 {
		return fmt.Errorf("invalid round number %d: must be positive", n)
	}

	cfg, err := loadRecallConfig()
	if err != nil {
		return err
	}
	if !cfg.Memory.Archive.IsEnabled() {
		return errors.New("archive is disabled in config (set memory.archive.enabled: true or omit it)")
	}

	reader, err := archive.NewFileArchiveReader(cfg.Memory.Archive.Dir)
	if err != nil {
		return fmt.Errorf("create archive reader: %w", err)
	}

	rec, err := reader.Read(context.Background(), n)
	if err != nil {
		if errors.Is(err, archive.ErrRoundNotFound) {
			fmt.Printf("round %d not found in archive at %s\n", n, cfg.Memory.Archive.Dir)
			return nil
		}
		return fmt.Errorf("read round %d: %w", n, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal round %d: %w", n, err)
	}
	fmt.Println(string(data))
	return nil
}
