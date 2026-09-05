package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/multica-ai/multica/server/internal/cli"
)

func newReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Work with project reports",
	}
	cmd.AddCommand(newReportGenerateCmd())
	cmd.AddCommand(newReportGetCmd())
	cmd.AddCommand(newReportJobCmd())
	return cmd
}

func newReportGenerateCmd() *cobra.Command {
	var projectID string
	var periodType string
	var rangeStart string
	var rangeEnd string
	var templateID string

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new project report",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}

			start, err := time.Parse(time.RFC3339, rangeStart)
			if err != nil {
				return fmt.Errorf("invalid range-start: %w", err)
			}
			end, err := time.Parse(time.RFC3339, rangeEnd)
			if err != nil {
				return fmt.Errorf("invalid range-end: %w", err)
			}

			req := map[string]any{
				"template_id": templateID,
				"period_type": periodType,
				"range_start": start,
				"range_end":   end,
				"timezone":    time.Local.String(),
			}

			var resp map[string]any
			if err := client.PostJSON(ctx, fmt.Sprintf("/api/projects/%s/reports", projectID), req, &resp); err != nil {
				return err
			}

			return cli.PrintJSON(os.Stdout, resp)
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project ID")
	cmd.Flags().StringVar(&periodType, "type", "weekly", "Report type (daily, weekly, monthly)")
	cmd.Flags().StringVar(&rangeStart, "since", "", "Start time (RFC3339)")
	cmd.Flags().StringVar(&rangeEnd, "until", "", "End time (RFC3339)")
	cmd.Flags().StringVar(&templateID, "template", "", "Template ID (optional)")
	cmd.MarkFlagRequired("project")
	cmd.MarkFlagRequired("since")
	cmd.MarkFlagRequired("until")

	return cmd
}

func newReportGetCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "get <report-id>",
		Short: "Get a project report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}

			var resp map[string]any
			if err := client.GetJSON(ctx, fmt.Sprintf("/api/projects/%s/reports/%s", projectID, args[0]), &resp); err != nil {
				return err
			}

			return cli.PrintJSON(os.Stdout, resp)
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project ID")
	cmd.MarkFlagRequired("project")

	return cmd
}

func newReportJobCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "job <job-id>",
		Short: "Get the status of a project report generation job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}

			var resp map[string]any
			if err := client.GetJSON(ctx, fmt.Sprintf("/api/projects/%s/reports/%s/job", projectID, args[0]), &resp); err != nil {
				return err
			}

			return cli.PrintJSON(os.Stdout, resp)
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project ID")
	cmd.MarkFlagRequired("project")

	return cmd
}
