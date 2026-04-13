// cli/cmd/client.go
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// buildURL joins host and path, stripping any trailing slash from host.
func buildURL(baseHost, path string) string {
	return strings.TrimRight(baseHost, "/") + path
}

// doRequest performs an authenticated HTTP request and returns the raw body.
// body may be nil. Returns an error for HTTP 4xx/5xx responses.
func doRequest(method, url, tok string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return raw, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// encodeJSON marshals v into a JSON reader for use as a request body.
func encodeJSON(v interface{}) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

// printTable prints key-value pairs in aligned columns.
func printTable(pairs [][2]string) {
	maxKey := 0
	for _, p := range pairs {
		if len(p[0]) > maxKey {
			maxKey = len(p[0])
		}
	}
	for _, p := range pairs {
		fmt.Printf("%-*s  %s\n", maxKey+1, p[0]+":", p[1])
	}
}

// printJSON pretty-prints a raw JSON byte slice to stdout.
func printJSON(raw []byte) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Println(string(raw))
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

// output prints raw as JSON if --json flag is set, otherwise calls humanFn.
func output(raw []byte, humanFn func()) {
	if flagJSON {
		printJSON(raw)
	} else {
		humanFn()
	}
}
