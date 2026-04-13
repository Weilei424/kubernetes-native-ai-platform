// cli/cmd/root.go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagHost  string
	flagToken string
	flagJSON  bool
)

var rootCmd = &cobra.Command{
	Use:   "platformctl",
	Short: "platformctl — kubernetes-native AI/ML platform CLI",
	Long: `platformctl is the developer interface for the kubernetes-native AI/ML platform.

Config (in precedence order):
  --host / --token flags
  PLATFORMCTL_HOST / PLATFORMCTL_TOKEN environment variables
  Default host: http://localhost:8080`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagHost, "host", "", "Control plane base URL (overrides PLATFORMCTL_HOST)")
	rootCmd.PersistentFlags().StringVar(&flagToken, "token", "", "API bearer token (overrides PLATFORMCTL_TOKEN)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Output raw JSON instead of human-readable format")
}

// host returns the resolved control-plane base URL.
func host() string {
	if flagHost != "" {
		return flagHost
	}
	if v := os.Getenv("PLATFORMCTL_HOST"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

// token returns the resolved bearer token.
func token() string {
	if flagToken != "" {
		return flagToken
	}
	return os.Getenv("PLATFORMCTL_TOKEN")
}
