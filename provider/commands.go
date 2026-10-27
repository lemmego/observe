package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
	"github.com/spf13/cobra"
)

// AddCommands contributes observe's commands.
func (p *Provider) AddCommands() []app.Command {
	return []app.Command{p.pruneCommand, p.lastCommand}
}

// pruneCommand deletes entries older than the retention window.
//
// A command rather than something the provider does on a timer, because a
// prune started wherever it was constructed would run in every web process
// and every worker at once — each deleting rows the others are reading. Run
// it from the scheduler, as one process.
func (p *Provider) pruneCommand(a app.App) *cobra.Command {
	var olderThan string

	cmd := &cobra.Command{
		Use:   "observe:prune",
		Short: "Delete recorded entries older than the retention window",
		Long: "Entries accumulate with traffic and nothing prunes them on its own.\n" +
			"Run this on a schedule, as one process.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			recorder := p.Recorder()
			if recorder == nil {
				return errRecordingOff
			}
			store := recorder.Store()
			if store == nil {
				return errors.New("observe: no store")
			}

			window := p.retention()
			if olderThan != "" {
				parsed, err := time.ParseDuration(olderThan)
				if err != nil {
					return fmt.Errorf("observe: --older-than: %w", err)
				}
				window = parsed
			}

			cutoff := time.Now().Add(-window)
			removed, err := store.Prune(cmd.Context(), cutoff)
			if err != nil {
				return err
			}

			cmd.Printf("observe: removed %d entries recorded before %s\n",
				removed, cutoff.Format(time.RFC3339))
			return nil
		},
	}

	cmd.Flags().StringVar(&olderThan, "older-than", "",
		"a duration such as 168h; defaults to the configured retention")
	return cmd
}

// lastCommand prints the most recent request and what it did.
//
// The terminal answer to the question the dashboard and the MCP tools answer
// their own ways — useful when you are already in a shell, and the quickest
// way to tell whether recording is working at all.
func (p *Provider) lastCommand(a app.App) *cobra.Command {
	var requestID string

	cmd := &cobra.Command{
		Use:   "observe:last",
		Short: "Show the most recent request and everything it caused",
		RunE: func(cmd *cobra.Command, _ []string) error {
			recorder := p.Recorder()
			if recorder == nil {
				return errRecordingOff
			}
			store := recorder.Store()
			if store == nil {
				return errors.New("observe: no store")
			}
			ctx := cmd.Context()

			if requestID == "" {
				requests, err := store.Entries(ctx, observe.Query{
					Kinds:  []observe.Kind{observe.KindRequest},
					Cursor: observe.Cursor{Limit: 1},
				})
				if err != nil {
					return err
				}
				if len(requests) == 0 {
					cmd.Println("observe: nothing recorded yet.")
					// Named, because an empty result has two causes and only
					// one of them is "no traffic".
					if watchers := recorder.Watchers(); len(watchers) == 0 {
						cmd.Println("No watchers are installed, so nothing would be recorded.")
					} else {
						cmd.Printf("Watching: %s\n", strings.Join(watchers, ", "))
					}
					return nil
				}
				requestID = requests[0].RequestID
			}

			entries, err := store.Entries(ctx, observe.Query{
				RequestID: requestID,
				Cursor:    observe.Cursor{Limit: observe.MaxPageSize},
			})
			if err != nil {
				return err
			}

			// Oldest first here, unlike the store's newest-first, because a
			// request reads as a sequence.
			for i := len(entries) - 1; i >= 0; i-- {
				entry := entries[i]
				marker := " "
				if entry.Failed() {
					marker = "!"
				}
				cmd.Printf("%s %-8s %8s  %s\n", marker, entry.Kind,
					entry.Duration.Round(time.Microsecond), entry.Summary)
			}

			if repeated := observe.Repetitions(entries, observe.DefaultNPlusOneThreshold); len(repeated) > 0 {
				cmd.Println()
				for _, rep := range repeated {
					cmd.Printf("N+1: %d executions of %s (%s total)\n",
						rep.Count, rep.Normalised, rep.Total.Round(time.Millisecond))
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&requestID, "request", "",
		"a request id; defaults to the most recent request")
	return cmd
}
