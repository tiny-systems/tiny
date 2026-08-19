package cmd

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/tiny-systems/module/api/v1alpha1"
)

// Scenario samples are captured from real traffic, which means a credential
// that once flowed through the graph is sitting in one — and unlike graph
// elements, samples were published verbatim.
//
// Redaction by key name, which is what the element redactor does, cannot help
// here: the value lands inside an ordinary payload field (a key-value store's
// query_result, a log line) whose name says nothing about what it holds. So
// these are matched by the shape of the secret itself.
//
// Samples exist to pin the SHAPE of a port's data for compile-time edge
// validation, never its contents, so replacing a matched value costs the
// sample nothing.

const redactedSample = "<redacted>"

// credentialPatterns matches the issued-token formats a flow realistically
// carries. Deliberately anchored on known prefixes rather than guessing at
// entropy: a false positive silently corrupts a sample someone relies on,
// and "looks random" describes plenty of legitimate ids.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`),                                       // Anthropic
	regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_\-]{20,}`),                                 // OpenAI
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),                                       // GitHub
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),                                     // GitHub fine-grained
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                                 // AWS access key id
	regexp.MustCompile(`ASIA[0-9A-Z]{16}`),                                                 // AWS temporary
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`),                                    // Slack
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`),                                           // Google API
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), // JWT
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_\-.=]{20,}`),                               // Authorization header
}

// redactSecretText masks every credential-shaped run in a string and reports
// whether anything changed.
func redactSecretText(s string) (string, bool) {
	out := s
	for _, re := range credentialPatterns {
		out = re.ReplaceAllString(out, redactedSample)
	}
	return out, out != s
}

// redactSecretValue walks decoded JSON, masking credential-shaped strings
// wherever they sit — including inside object keys' values, arrays, and
// strings that themselves contain embedded JSON.
func redactSecretValue(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case string:
		return redactSecretText(t)
	case []interface{}:
		changed := false
		for i, item := range t {
			replaced, did := redactSecretValue(item)
			if did {
				t[i] = replaced
				changed = true
			}
		}
		return t, changed
	case map[string]interface{}:
		changed := false
		for k, item := range t {
			replaced, did := redactSecretValue(item)
			if did {
				t[k] = replaced
				changed = true
			}
		}
		return t, changed
	}
	return v, false
}

// redactScenarioPorts masks credentials in scenario sample data before it is
// exported, returning the ports to publish and the port names that were
// touched so the caller can say what it did.
//
// A sample that does not parse as JSON is still scrubbed as raw text rather
// than passed through: the point is that nothing credential-shaped leaves the
// machine, whatever shape the sample happens to be in.
func redactScenarioPorts(ports []v1alpha1.ScenarioPortData) ([]v1alpha1.ScenarioPortData, []string) {
	var touched []string
	out := make([]v1alpha1.ScenarioPortData, 0, len(ports))

	for _, p := range ports {
		if len(p.Data) == 0 {
			out = append(out, p)
			continue
		}

		var decoded interface{}
		if err := json.Unmarshal(p.Data, &decoded); err != nil {
			if masked, changed := redactSecretText(string(p.Data)); changed {
				p.Data = []byte(masked)
				touched = append(touched, p.Port)
			}
			out = append(out, p)
			continue
		}

		replaced, changed := redactSecretValue(decoded)
		if !changed {
			out = append(out, p)
			continue
		}
		reencoded, err := marshalUnescaped(replaced)
		if err != nil {
			// Cannot re-encode what we know contains a secret: drop the
			// sample rather than publish the original.
			touched = append(touched, p.Port)
			continue
		}
		p.Data = reencoded
		touched = append(touched, p.Port)
		out = append(out, p)
	}
	return out, touched
}

// marshalUnescaped encodes without Go's default HTML escaping, which would
// turn the redaction marker into <redacted> and make a scrubbed
// sample harder to read than the thing it replaced.
func marshalUnescaped(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
