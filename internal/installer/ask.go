package installer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Choice is one answer a question offers. A choice with Unavailable set is shown, with the reason,
// and cannot be picked: nothing the installer offers may be a choice that fails.
type Choice struct {
	Value       string
	Label       string
	Unavailable string // why it cannot be chosen now ("" = available)
}

// Question is one thing asked. Key names it in an answers file.
type Question struct {
	Key     string
	Prompt  string
	Help    string
	Default string
	Choices []Choice
	// Secret answers are never echoed, never in the transcript, and never defaulted from the file.
	Secret bool
	// Required: no default exists, so unattended runs must give it.
	Required bool
}

// Asker asks the questions of an install and counts what the person had to do.
type Asker struct {
	env     *Env
	answers map[string]string
	rd      *bufio.Reader
	t       *Transcript

	mu    sync.Mutex
	Typed int // answers the person typed (not Enter on a default, not from a file)
	Asked int // questions asked in all
	used  map[string]bool
}

// NewAsker builds the asker. answers comes from --answers and --set (may be nil).
func NewAsker(env *Env, answers map[string]string, t *Transcript) *Asker {
	return &Asker{env: env, answers: answers, rd: bufio.NewReader(env.In), t: t, used: map[string]bool{}}
}

// LoadAnswers reads an answers file: a flat YAML map of question keys to values. Keys it holds
// that no question uses are reported by Unused after the run, so a typo is not silently ignored.
func LoadAnswers(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}

// Unused lists the answers given that no question read (probably a typo), sorted.
func (a *Asker) Unused() []string {
	var u []string
	for k := range a.answers {
		if !a.used[k] {
			u = append(u, k)
		}
	}
	sort.Strings(u)
	return u
}

// Has reports whether an answer was supplied for key.
func (a *Asker) Has(key string) bool { _, ok := a.answers[key]; return ok }

var errNeedsAnswer = errors.New("a question needs an answer and none was given")

// NeedsAnswer is returned when an unattended run reaches a question with no answer and no default.
type NeedsAnswer struct{ Q Question }

func (n *NeedsAnswer) Error() string {
	return fmt.Sprintf("this needs an answer and none was given: %s (answers key %q)", n.Q.Prompt, n.Q.Key)
}
func (n *NeedsAnswer) Unwrap() error { return errNeedsAnswer }

// Ask returns the answer: from the answers, else typed, else the default.
func (a *Asker) Ask(q Question) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Asked++
	if v, ok := a.answers[q.Key]; ok {
		a.used[q.Key] = true
		if err := checkChoice(q, v); err != nil {
			return "", fmt.Errorf("answer %q: %w", q.Key, err)
		}
		a.t.Answer(q, v, "from the answers")
		return v, nil
	}
	if !a.env.Interactive {
		if q.Required && q.Default == "" {
			return "", &NeedsAnswer{Q: q}
		}
		a.t.Answer(q, q.Default, "default")
		return q.Default, nil
	}
	return a.prompt(q)
}

func checkChoice(q Question, v string) error {
	if len(q.Choices) == 0 {
		return nil
	}
	for _, c := range q.Choices {
		if c.Value == v {
			if c.Unavailable != "" {
				return fmt.Errorf("%q is not available: %s", v, c.Unavailable)
			}
			return nil
		}
	}
	var vs []string
	for _, c := range q.Choices {
		vs = append(vs, c.Value)
	}
	return fmt.Errorf("%q is not one of %s", v, strings.Join(vs, ", "))
}

func (a *Asker) prompt(q Question) (string, error) {
	out := a.env.Out
	fmt.Fprintln(out)
	fmt.Fprintln(out, q.Prompt)
	if q.Help != "" {
		fmt.Fprintln(out, indent(q.Help, "  "))
	}
	def := q.Default
	for i, c := range q.Choices {
		note := ""
		if c.Unavailable != "" {
			note = "  (coming, not available now: " + c.Unavailable + ")"
		}
		mark := " "
		if c.Value == def {
			mark = "*"
		}
		fmt.Fprintf(out, " %s %d) %s%s\n", mark, i+1, c.Label, note)
	}
	for {
		hint := ""
		if def != "" && !q.Secret {
			hint = " [" + displayDefault(q, def) + "]"
		}
		fmt.Fprintf(out, "> %s", strings.TrimSuffix(shortPrompt(q), "?")+hint+": ")
		line, err := a.readLine(q.Secret)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("the input ended while waiting for an answer to: %s", q.Prompt)
			}
			return "", err
		}
		line = strings.TrimSpace(line)
		typed := line != ""
		if line == "" {
			line = def
		}
		if line == "" && q.Required {
			fmt.Fprintln(out, "  An answer is needed here.")
			continue
		}
		if n, err := strconv.Atoi(line); err == nil && len(q.Choices) > 0 && n >= 1 && n <= len(q.Choices) {
			line = q.Choices[n-1].Value
		}
		if err := checkChoice(q, line); err != nil {
			fmt.Fprintf(out, "  %v. Try again.\n", err)
			continue
		}
		if typed {
			a.Typed++
		}
		src := "typed"
		if !typed {
			src = "Enter, the default"
		}
		a.t.Answer(q, line, src)
		return line, nil
	}
}

func displayDefault(q Question, def string) string {
	for i, c := range q.Choices {
		if c.Value == def {
			return strconv.Itoa(i + 1)
		}
	}
	return def
}

func shortPrompt(q Question) string {
	if len(q.Choices) > 0 {
		return "your choice"
	}
	return "answer"
}

func (a *Asker) readLine(secret bool) (string, error) {
	if secret {
		if line, ok := readSecret(a.env); ok {
			return line, nil
		}
	}
	return a.rd.ReadString('\n')
}

func indent(s, p string) string {
	return p + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+p)
}

// Confirm asks a yes/no question (default as given). Unattended runs answer from the answers or
// take the default.
func (a *Asker) Confirm(key, prompt string, def bool) (bool, error) {
	d := "no"
	if def {
		d = "yes"
	}
	v, err := a.Ask(Question{Key: key, Prompt: prompt, Default: d, Choices: []Choice{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}})
	if err != nil {
		return false, err
	}
	return v == "yes", nil
}

var secretLike = regexp.MustCompile(`(?i)(token|password|secret|passphrase)`)

// IsSecretKey is true for answers keys that carry secret material; they are refused in answers files
// unless they name a file or variable (the installer never stores a pasted secret in a plain file).
func IsSecretKey(k string) bool {
	return secretLike.MatchString(k) && !strings.HasSuffix(k, "_file") && !strings.HasSuffix(k, "_env")
}
