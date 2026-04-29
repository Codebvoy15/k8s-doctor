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
	Short: "Start the web dashboard",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		engine, err := server.NewDashboardEngine(ctx, namespace, verbose)
		if err != nil {
			return fmt.Errorf("could not connect to cluster: %w", err)
		}

		srv := server.NewServer(engine, serveRefresh)
		addr := fmt.Sprintf(":%d", servePort)

		fmt.Printf("\nserve  cluster=%s  port=%d  refresh=%ds\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			servePort,
			serveRefresh,
		)
		fmt.Printf("open   http://localhost:%d\n", servePort)
		fmt.Printf("%s\n\n", color.HiBlackString("press Ctrl+C to stop"))

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
