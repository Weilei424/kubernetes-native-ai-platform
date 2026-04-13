// cli/cmd/train.go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// trainSpec mirrors jobs.JobSubmitRequest for YAML/JSON round-trip.
type trainSpec struct {
	Name      string         `yaml:"name"       json:"name"`
	ProjectID string         `yaml:"project_id" json:"project_id"`
	Runtime   trainRuntime   `yaml:"runtime"    json:"runtime"`
	Resources trainResources `yaml:"resources"  json:"resources"`
}

type trainRuntime struct {
	Image   string            `yaml:"image"   json:"image"`
	Command []string          `yaml:"command" json:"command"`
	Args    []string          `yaml:"args"    json:"args"`
	Env     map[string]string `yaml:"env"     json:"env"`
}

type trainResources struct {
	NumWorkers   int    `yaml:"num_workers"   json:"num_workers"`
	WorkerCPU    string `yaml:"worker_cpu"    json:"worker_cpu"`
	WorkerMemory string `yaml:"worker_memory" json:"worker_memory"`
	HeadCPU      string `yaml:"head_cpu"      json:"head_cpu"`
	HeadMemory   string `yaml:"head_memory"   json:"head_memory"`
}

var trainCmd = &cobra.Command{
	Use:   "train",
	Short: "Manage training jobs",
}

var trainSubmitFile string

var trainSubmitCmd = &cobra.Command{
	Use:     "submit",
	Short:   "Submit a training job from a YAML spec file",
	Example: "  platformctl train submit -f examples/training-specs/resnet.yaml",
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := os.ReadFile(trainSubmitFile)
		if err != nil {
			return fmt.Errorf("read spec: %w", err)
		}
		var spec trainSpec
		if err := yaml.Unmarshal(raw, &spec); err != nil {
			return fmt.Errorf("parse spec: %w", err)
		}
		body, err := encodeJSON(spec)
		if err != nil {
			return err
		}
		resp, err := doRequest("POST", buildURL(host(), "/v1/jobs"), token(), body)
		if err != nil {
			return err
		}
		output(resp, func() {
			var out struct {
				JobID string `json:"job_id"`
				RunID string `json:"run_id"`
			}
			json.Unmarshal(resp, &out)
			printTable([][2]string{
				{"Job ID", out.JobID},
				{"Run ID", out.RunID},
			})
		})
		return nil
	},
}

var trainWatch bool

var trainStatusCmd = &cobra.Command{
	Use:     "status <job-id>",
	Short:   "Show status of a training job",
	Args:    cobra.ExactArgs(1),
	Example: "  platformctl train status abc123\n  platformctl train status abc123 --watch",
	RunE: func(cmd *cobra.Command, args []string) error {
		fetch := func() ([]byte, error) {
			return doRequest("GET", buildURL(host(), "/v1/jobs/"+args[0]), token(), nil)
		}
		print := func(raw []byte) {
			output(raw, func() { printJobStatus(raw) })
		}
		if !trainWatch {
			raw, err := fetch()
			if err != nil {
				return err
			}
			print(raw)
			return nil
		}
		return watchPoll(fetch, func(raw []byte) bool {
			var out struct {
				Job struct{ Status string } `json:"job"`
			}
			json.Unmarshal(raw, &out)
			s := out.Job.Status
			return s == "SUCCEEDED" || s == "FAILED" || s == "CANCELLED"
		}, print)
	},
}

func printJobStatus(raw []byte) {
	var out struct {
		Job struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"job"`
		Run struct {
			ID          string  `json:"id"`
			Status      string  `json:"status"`
			MLflowRunID *string `json:"mlflow_run_id"`
		} `json:"run"`
	}
	json.Unmarshal(raw, &out)
	pairs := [][2]string{
		{"Job ID", out.Job.ID},
		{"Name", out.Job.Name},
		{"Status", out.Job.Status},
		{"Run ID", out.Run.ID},
		{"Run Status", out.Run.Status},
	}
	if out.Run.MLflowRunID != nil && *out.Run.MLflowRunID != "" {
		pairs = append(pairs, [2]string{"MLflow Run", *out.Run.MLflowRunID})
	}
	printTable(pairs)
}

// watchPoll polls fetchFn every 5 seconds until doneFn returns true.
func watchPoll(fetchFn func() ([]byte, error), doneFn func([]byte) bool, printFn func([]byte)) error {
	for {
		raw, err := fetchFn()
		if err != nil {
			return err
		}
		fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
		printFn(raw)
		if doneFn(raw) {
			return nil
		}
		fmt.Println()
		time.Sleep(5 * time.Second)
	}
}

func init() {
	trainSubmitCmd.Flags().StringVarP(&trainSubmitFile, "file", "f", "", "Path to training spec YAML (required)")
	trainSubmitCmd.MarkFlagRequired("file")

	trainStatusCmd.Flags().BoolVar(&trainWatch, "watch", false, "Poll every 5s until terminal state")

	trainCmd.AddCommand(trainSubmitCmd, trainStatusCmd)
	rootCmd.AddCommand(trainCmd)
}
