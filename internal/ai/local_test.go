package ai_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
)

// The local-model test runs a real model in Ollama, in a container, through
// the openai provider. It is opt-in (LAUNCHWAY_TEST_LOCAL_MODEL=1) because it
// pulls an image and a model of a few hundred megabytes. The model files are
// kept in a named volume, so later runs skip the download.
const (
	ollamaImage  = "docker.io/ollama/ollama:0.40.16"
	ollamaModel  = "qwen2.5:0.5b" // about 400 MB
	ollamaVolume = "launchway-test-ollama-models"
)

func TestLocalModel(t *testing.T) {
	if os.Getenv("LAUNCHWAY_TEST_LOCAL_MODEL") != "1" {
		t.Skip("set LAUNCHWAY_TEST_LOCAL_MODEL=1 to run a real local model in a container")
	}
	engine := ""
	for _, e := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(e); err == nil {
			engine = e
			break
		}
	}
	if engine == "" {
		t.Skip("neither podman nor docker is installed")
	}
	suffix := make([]byte, 4)
	rand.Read(suffix)
	name := "launchway-ai-test-" + hex.EncodeToString(suffix)

	run := func(timeout time.Duration, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(10*time.Minute, "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::11434",
		"-v", ollamaVolume+":/root/.ollama", ollamaImage)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, engine, "stop", "-t", "5", name).Run()
	})
	port := run(30*time.Second, "port", name, "11434/tcp")
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	base := "http://127.0.0.1:" + strings.Fields(port)[0]

	deadline := time.Now().Add(2 * time.Minute)
	for {
		res, err := http.Get(base + "/api/version")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ollama did not start within 2m: %v", err)
		}
		time.Sleep(time.Second)
	}

	t.Logf("pulling %s (may take minutes the first time)", ollamaModel)
	pullCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": ollamaModel, "stream": false})
	req, _ := http.NewRequestWithContext(pullCtx, http.MethodPost, base+"/api/pull", bytes.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pull %s: %v", ollamaModel, err)
	}
	var pulled struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	json.NewDecoder(res.Body).Decode(&pulled)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || pulled.Status != "success" {
		t.Fatalf("pull %s: status %d %q %s", ollamaModel, res.StatusCode, pulled.Status, pulled.Error)
	}

	schema := json.RawMessage(`{"type":"object","properties":{
		"answer":{"type":"string","enum":["yes","no"]},
		"confidence":{"type":"integer","minimum":0,"maximum":100}},
		"required":["answer","confidence"]}`)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			r, err := ai.NewRegistry([]ai.Model{{Name: "local", Provider: "openai", Endpoint: base + "/v1",
				Model: ollamaModel, Projects: []string{"*"}, MaxTokens: 200, Timeout: "10m", Stream: &stream}})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := r.Get("local")
			temp := 0.0
			resp, err := (&ai.Client{}).Call(context.Background(), m, "", ai.Request{
				System:      "You answer questions about the world.",
				Prompt:      "Is water wet? Answer yes or no, with your confidence from 0 to 100.",
				Schema:      schema,
				Temperature: &temp,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("answer %s, stop %s, model %s, usage %+v, %d ms", resp.JSON, resp.StopReason, resp.Model, resp.Usage, resp.DurationMS)
			if resp.JSON == nil || resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
				t.Errorf("response %+v", resp)
			}
		})
	}
}
