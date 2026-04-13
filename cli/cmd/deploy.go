// cli/cmd/deploy.go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// deploySpec mirrors deployments.CreateDeploymentRequest for YAML/JSON round-trip.
type deploySpec struct {
	Name         string `yaml:"name"          json:"name"`
	ModelName    string `yaml:"model_name"    json:"model_name"`
	ModelVersion int    `yaml:"model_version" json:"model_version"`
	Namespace    string `yaml:"namespace"     json:"namespace"`
	Replicas     int    `yaml:"replicas"      json:"replicas"`
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Manage model serving deployments",
}

var deployCreateFile string

var deployCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create a deployment from a YAML spec file",
	Example: "  platformctl deploy create -f examples/deployment-specs/resnet-prod.yaml",
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := os.ReadFile(deployCreateFile)
		if err != nil {
			return fmt.Errorf("read spec: %w", err)
		}
		var spec deploySpec
		if err := yaml.Unmarshal(raw, &spec); err != nil {
			return fmt.Errorf("parse spec: %w", err)
		}
		body, err := encodeJSON(spec)
		if err != nil {
			return err
		}
		resp, err := doRequest("POST", buildURL(host(), "/v1/deployments"), token(), body)
		if err != nil {
			return err
		}
		output(resp, func() {
			var out struct {
				Deployment struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"deployment"`
			}
			json.Unmarshal(resp, &out)
			printTable([][2]string{
				{"Deployment ID", out.Deployment.ID},
				{"Status", out.Deployment.Status},
			})
		})
		return nil
	},
}

var deployWatch bool

var deployStatusCmd = &cobra.Command{
	Use:     "status <deployment-id>",
	Short:   "Show status of a deployment",
	Args:    cobra.ExactArgs(1),
	Example: "  platformctl deploy status abc123\n  platformctl deploy status abc123 --watch",
	RunE: func(cmd *cobra.Command, args []string) error {
		fetch := func() ([]byte, error) {
			return doRequest("GET", buildURL(host(), "/v1/deployments/"+args[0]), token(), nil)
		}
		print := func(raw []byte) {
			output(raw, func() { printDeployStatus(raw) })
		}
		if !deployWatch {
			raw, err := fetch()
			if err != nil {
				return err
			}
			print(raw)
			return nil
		}
		return watchPoll(fetch, func(raw []byte) bool {
			var out struct {
				Deployment struct{ Status string } `json:"deployment"`
			}
			json.Unmarshal(raw, &out)
			s := out.Deployment.Status
			return s == "running" || s == "failed" || s == "deleted"
		}, print)
	},
}

func printDeployStatus(raw []byte) {
	var out struct {
		Deployment struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Status          string `json:"status"`
			ServingEndpoint string `json:"serving_endpoint"`
			FailureReason   string `json:"failure_reason"`
		} `json:"deployment"`
	}
	json.Unmarshal(raw, &out)
	pairs := [][2]string{
		{"Deployment ID", out.Deployment.ID},
		{"Name", out.Deployment.Name},
		{"Status", out.Deployment.Status},
	}
	if out.Deployment.ServingEndpoint != "" {
		pairs = append(pairs, [2]string{"Endpoint", out.Deployment.ServingEndpoint})
	}
	if out.Deployment.FailureReason != "" {
		pairs = append(pairs, [2]string{"Failure", out.Deployment.FailureReason})
	}
	printTable(pairs)
}

func init() {
	deployCreateCmd.Flags().StringVarP(&deployCreateFile, "file", "f", "", "Path to deployment spec YAML (required)")
	deployCreateCmd.MarkFlagRequired("file")

	deployStatusCmd.Flags().BoolVar(&deployWatch, "watch", false, "Poll every 5s until terminal state")

	deployCmd.AddCommand(deployCreateCmd, deployStatusCmd)
	rootCmd.AddCommand(deployCmd)
}
