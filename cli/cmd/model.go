// cli/cmd/model.go
package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "Manage model registration and promotion",
}

var (
	modelRegRunID    string
	modelRegName     string
	modelRegArtifact string

	modelProName    string
	modelProVersion int
	modelProAlias   string
)

var modelRegisterCmd = &cobra.Command{
	Use:     "register",
	Short:   "Register a model version from a succeeded training run",
	Example: "  platformctl model register --run-id <run-id> --name resnet50",
	RunE: func(cmd *cobra.Command, args []string) error {
		payload := map[string]string{
			"run_id":     modelRegRunID,
			"model_name": modelRegName,
		}
		if modelRegArtifact != "" {
			payload["artifact_path"] = modelRegArtifact
		}
		body, err := encodeJSON(payload)
		if err != nil {
			return err
		}
		resp, err := doRequest("POST", buildURL(host(), "/v1/models"), token(), body)
		if err != nil {
			return err
		}
		output(resp, func() {
			var out struct {
				Version struct {
					VersionNumber int    `json:"version_number"`
					Status        string `json:"status"`
					ArtifactURI   string `json:"artifact_uri"`
				} `json:"version"`
			}
			json.Unmarshal(resp, &out)
			printTable([][2]string{
				{"Version", strconv.Itoa(out.Version.VersionNumber)},
				{"Status", out.Version.Status},
				{"Artifact URI", out.Version.ArtifactURI},
			})
		})
		return nil
	},
}

var modelPromoteCmd = &cobra.Command{
	Use:     "promote",
	Short:   "Promote a model version to an alias (staging, production, archived)",
	Example: "  platformctl model promote --name resnet50 --version 1 --alias production",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := fmt.Sprintf("/v1/models/%s/versions/%d/promote", modelProName, modelProVersion)
		body, err := encodeJSON(map[string]string{"alias": modelProAlias})
		if err != nil {
			return err
		}
		resp, err := doRequest("POST", buildURL(host(), path), token(), body)
		if err != nil {
			return err
		}
		output(resp, func() {
			printTable([][2]string{
				{"Model", modelProName},
				{"Version", strconv.Itoa(modelProVersion)},
				{"Alias", modelProAlias},
				{"Status", "promoted"},
			})
		})
		return nil
	},
}

func init() {
	modelRegisterCmd.Flags().StringVar(&modelRegRunID, "run-id", "", "Platform run ID (required)")
	modelRegisterCmd.Flags().StringVar(&modelRegName, "name", "", "Model name (required)")
	modelRegisterCmd.Flags().StringVar(&modelRegArtifact, "artifact-path", "", "Artifact path within MLflow run (default: model/)")
	modelRegisterCmd.MarkFlagRequired("run-id")
	modelRegisterCmd.MarkFlagRequired("name")

	modelPromoteCmd.Flags().StringVar(&modelProName, "name", "", "Model name (required)")
	modelPromoteCmd.Flags().IntVar(&modelProVersion, "version", 0, "Version number (required)")
	modelPromoteCmd.Flags().StringVar(&modelProAlias, "alias", "", "Alias: staging, production, or archived (required)")
	modelPromoteCmd.MarkFlagRequired("name")
	modelPromoteCmd.MarkFlagRequired("version")
	modelPromoteCmd.MarkFlagRequired("alias")

	modelCmd.AddCommand(modelRegisterCmd, modelPromoteCmd)
	rootCmd.AddCommand(modelCmd)
}
