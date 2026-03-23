package cmd

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/server"
)

var (
	servePort    int
	serveRefresh int
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web dashboard — see everything in your browser",
	Long: `Starts a local web server with a real-time cluster dashboard.

On the jumpserver:
  ./k8s-doctor serve --port 8080

On your local machine (SSH tunnel):
  ssh -L 8080:localhost:8080 user@jumpserver
  open http://localhost:8080
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		engine, err := server.NewDashboardEngine(ctx, namespace, verbose)
		if err != nil {
			return fmt.Errorf("could not connect to cluster: %w", err)
		}

		srv := server.NewServer(engine, serveRefresh)

		addr := fmt.Sprintf(":%d", servePort)
		color.Cyan("→ k8s-doctor dashboard starting...")
		color.Green("✓ Open in browser: http://localhost:%d", servePort)
		color.HiBlack("  Cluster:  %s", clusterName)
		color.HiBlack("  Refresh:  every %ds", serveRefresh)
		color.HiBlack("  Press Ctrl+C to stop\n")

		httpSrv := &http.Server{
			Addr:         addr,
			Handler:      srv,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
		}

		return httpSrv.ListenAndServe()
	},
}

func init() {
	serveCmd.Flags().IntVar(&servePort, "port", 8080, "port to serve dashboard on")
	serveCmd.Flags().IntVar(&serveRefresh, "refresh", 30, "auto-refresh interval in seconds")
	rootCmd.AddCommand(serveCmd)
}
