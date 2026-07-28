// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2023-2026 Jason Sando

package main

import (
	"fmt"
	"os"
)

// Build variables - these can be set during build using ldflags
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type Runner interface {
	Name() string
	Run(args []string) error
}

func main() {
	if err := executeSubcommand(os.Args[1:]); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func executeSubcommand(args []string) error {
	cmds := []Runner{
		NewCreateCommand(),
		NewCopyCommand(),
		NewListCommand(),
	}

	// Check for help flag
	if len(args) < 1 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		printUsage()
		return nil
	}

	// Check for version flag
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v" || args[0] == "version") {
		printVersion()
		return nil
	}

	subcommand := args[0]
	for _, cmd := range cmds {
		if cmd.Name() == subcommand {
			return cmd.Run(args[1:])
		}
	}
	return fmt.Errorf("unknown subcommand: %s", subcommand)
}

func printUsage() {
	fmt.Printf(`fatimg %s - Create and manage FAT32 boot (EFI) partition disk images

Usage:
  fatimg <command> [arguments]

Commands:
  create    Create a disk image with an EFI partition
  ls        List contents of the first partition in a disk image
  cp        Copy files into or out of a disk image

Use "fatimg <command> --help" for more information about a command.
Use "fatimg --version" to see version information.
`, version)
}

func printVersion() {
	fmt.Printf("fatimg version %s\n", version)
	if commit != "unknown" && buildDate != "unknown" {
		fmt.Printf("  commit: %s\n", commit)
		fmt.Printf("  built:  %s\n", buildDate)
	}
}
