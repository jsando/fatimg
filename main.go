package main

import (
	"fmt"
	"os"
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

	subcommand := args[0]
	for _, cmd := range cmds {
		if cmd.Name() == subcommand {
			return cmd.Run(args[1:])
		}
	}
	return fmt.Errorf("unknown subcommand: %s", subcommand)
}

func printUsage() {
	fmt.Printf(`fatimg - Create and manage FAT32 boot (EFI) partition disk images

Usage:
  fatimg <command> [arguments]

Commands:
  create    Create a disk image with an EFI partition
  ls        List contents of the first partition in a disk image
  cp        Copy files from a disk image to a local directory

Use "fatimg <command> --help" for more information about a command.
`)
}
