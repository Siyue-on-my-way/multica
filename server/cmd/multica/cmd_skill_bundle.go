package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var skillInitCmd = &cobra.Command{
	Use:   "init <directory>",
	Short: "Initialize a new skill bundle directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillInit,
}

var skillPackCmd = &cobra.Command{
	Use:   "pack <directory> [output.zip]",
	Short: "Pack a skill directory into a .zip archive",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runSkillPack,
}

func init() {
	skillCmd.AddCommand(skillInitCmd)
	skillCmd.AddCommand(skillPackCmd)
}

func runSkillInit(cmd *cobra.Command, args []string) error {
	dir := args[0]
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	manifestPath := filepath.Join(dir, "manifest.yaml")
	manifestContent := `name: my-skill
description: A new Multica skill
entrypoint: scripts/main.py
runtime: docker
image: python:3.10-slim
required_secrets: []
tools:
  - name: my_tool
    description: A tool provided by this skill
`
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		return fmt.Errorf("write manifest.yaml: %w", err)
	}

	skillMdPath := filepath.Join(dir, "SKILL.md")
	skillMdContent := `---
name: my-skill
description: A new Multica skill
---

# my-skill

This skill provides tools for the agent.
`
	if err := os.WriteFile(skillMdPath, []byte(skillMdContent), 0644); err != nil {
		return fmt.Errorf("write SKILL.md: %w", err)
	}

	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		return fmt.Errorf("create scripts directory: %w", err)
	}

	mainPyPath := filepath.Join(scriptsDir, "main.py")
	mainPyContent := `import sys

def main():
    print("Hello from my-skill!")

if __name__ == "__main__":
    main()
`
	if err := os.WriteFile(mainPyPath, []byte(mainPyContent), 0755); err != nil {
		return fmt.Errorf("write main.py: %w", err)
	}

	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	fmt.Printf("Initialized skill bundle in %s\n", dir)
	return nil
}

func runSkillPack(cmd *cobra.Command, args []string) error {
	dir := args[0]
	output := dir + ".zip"
	if len(args) > 1 {
		output = args[1]
	}

	if !strings.HasSuffix(output, ".zip") && !strings.HasSuffix(output, ".skill") {
		output += ".zip"
	}

	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	w := zip.NewWriter(outFile)
	defer w.Close()

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip hidden files and directories
		for _, part := range strings.Split(relPath, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return nil
			}
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		zf, err := w.Create(relPath)
		if err != nil {
			return err
		}

		_, err = io.Copy(zf, f)
		return err
	})

	if err != nil {
		return fmt.Errorf("pack directory: %w", err)
	}

	fmt.Printf("Packed skill bundle to %s\n", output)
	return nil
}
