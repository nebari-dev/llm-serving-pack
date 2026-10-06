package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
)

// Providers the console can add: each is a base URL speaking OpenAI's API
// and an optional key the gateway sends as a bearer token. Anthropic's base
// URL serves both its OpenAI-compatible endpoint, for OpenAI-style callers,
// and its own Messages API, for Anthropic-style ones (Compile's native twin,
// which sends the key as x-api-key).
var Providers = []string{"OpenAI", "Anthropic", "OpenAI-compatible", "Self-hosted"}

// CloudProviders need cloud credentials (AWS, Azure, GCP identities) rather
// than an API key, which the console can't set up yet.
var CloudProviders = []string{"Bedrock", "Azure", "Vertex"}

// ParseBaseURL is the endpoint for a provider's base URL (what an OpenAI SDK
// takes as base_url). localhost is the control plane's machine: the gateway
// reaches it as ${STARGATE_HOST:-localhost}, as it does the seeded fake
// backends, so a gateway in Docker can set STARGATE_HOST.
func ParseBaseURL(raw string) (model.BackendEndpoint, error) {
	var e model.BackendEndpoint
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || strings.ContainsAny(raw, " \t\n"):
		return e, errors.New("the base URL isn't a URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return e, errors.New("the base URL must start with https:// or http://")
	case u.Hostname() == "":
		return e, errors.New("the base URL needs a host")
	case u.User != nil:
		return e, errors.New("put the key in the API key field, not the URL")
	case u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#"):
		return e, errors.New("the base URL can't have a query or fragment")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return e, fmt.Errorf("port %s isn't a port", port)
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" {
		host = "${STARGATE_HOST:-" + host + "}"
	}
	return model.BackendEndpoint{Schema: "OpenAI", Prefix: strings.TrimSuffix(u.EscapedPath(), "/"), Host: host, Port: port, TLS: u.Scheme == "https"}, nil
}

var envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expand replaces aigw's ${VAR:-default}s: by getenv's value, or the default
// when that's empty.
func expand(s string, getenv func(string) string) string {
	return envRefRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRefRE.FindStringSubmatch(m)
		if v := getenv(sub[1]); v != "" {
			return v
		}
		return sub[2]
	})
}

func baseURL(e model.BackendEndpoint, getenv func(string) string) string {
	scheme, def := "http", "80"
	if e.TLS {
		scheme, def = "https", "443"
	}
	host, port := expand(e.Host, getenv), expand(e.Port, getenv)
	if port != def {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host + expand(e.Prefix, getenv)
}

// BaseURL is the endpoint as a base URL, ${VAR:-default}s at their defaults:
// what the console shows and edits.
func BaseURL(e model.BackendEndpoint) string {
	return baseURL(e, func(string) string { return "" })
}

// ResolvedBaseURL is the base URL with getenv's values, as aigw would see it.
func ResolvedBaseURL(e model.BackendEndpoint, getenv func(string) string) string {
	return baseURL(e, getenv)
}

var regionRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateBackend checks a backend before it's saved. was is the backend
// being edited, nil for a new one: a seeded cloud backend (bedrock-eu) can be
// edited, but no new one can be made.
func ValidateBackend(b model.Backend, was *model.Backend) error {
	switch {
	case !routeNameRE.MatchString(b.Name):
		return errors.New("name must be lowercase letters, digits, dots and dashes")
	case len(b.Name) > 59:
		return errors.New("name must be at most 59 characters") // its Secret is <name>-key
	case b.Provider == "Anthropic" && len(b.Name) > 63-len(NativeSuffix+"-key"):
		// Its native twin's policy is <name>-native-key.
		return fmt.Errorf("an Anthropic backend's name must be at most %d characters", 63-len(NativeSuffix+"-key"))
	case was == nil && strings.HasSuffix(b.Name, NativeSuffix):
		return fmt.Errorf("names ending in %s are kept for Anthropic backends' native twins", NativeSuffix)
	case strings.TrimSpace(b.Provider) == "":
		return errors.New("say which provider it is")
	case !regionRE.MatchString(b.Region):
		return errors.New("region must be lowercase letters, digits and dashes (us-east, eu-central, local)")
	case b.Endpoint == nil:
		return errors.New("a base URL is required")
	case len(b.Models) == 0:
		return errors.New("list at least one model it serves")
	}
	if slices.Contains(CloudProviders, b.Provider) && (was == nil || was.Provider != b.Provider) {
		return fmt.Errorf("%s needs cloud credentials, which the console can't set up yet", b.Provider)
	}
	seen := map[string]bool{}
	for _, m := range b.Models {
		switch {
		case strings.TrimSpace(m) == "":
			return errors.New("a model name can't be blank")
		case strings.ContainsAny(m, " \t\n"):
			return fmt.Errorf("model %q can't contain spaces", m)
		case strings.Contains(m, "*"):
			return fmt.Errorf("%q: a backend serves exact model names", m)
		case seen[m]:
			return fmt.Errorf("%s is listed twice", m)
		}
		seen[m] = true
	}
	return nil
}

// ConnectionTest is one test of a provider: the models its /models lists,
// or why it couldn't list them, in the provider's words. Never the key.
type ConnectionTest struct {
	OK     bool     `json:"ok"`
	Status int      `json:"status,omitempty"` // the provider's HTTP status; 0 when it didn't answer
	Models []string `json:"models"`
	Error  string   `json:"error,omitempty"`
	MS     int      `json:"ms"`
	At     int64    `json:"at"` // epoch ms
}

// Message is the test in a line, as the backend keeps it.
func (c ConnectionTest) Message() string {
	if c.OK {
		return fmt.Sprintf("%d %s: %s", len(c.Models), plural(len(c.Models), "model", "models"), strings.Join(c.Models, ", "))
	}
	return c.Error
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// TestConnection lists the provider's models at baseURL + "/models" with the
// key (none when key is ""). Anthropic takes it as x-api-key; the rest as a
// bearer token, which is how the gateway sends it.
func TestConnection(ctx context.Context, c *http.Client, provider, baseURL, key string) ConnectionTest {
	start := time.Now()
	out := ConnectionTest{Models: []string{}, At: start.UnixMilli()}
	done := func() ConnectionTest {
		out.MS = int(time.Since(start).Milliseconds())
		out.Error = Scrub(out.Error, key)
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		out.Error = err.Error()
		return done()
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		if provider == "Anthropic" {
			req.Header.Set("X-Api-Key", key)
			req.Header.Set("Anthropic-Version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	res, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is the caller's; say what went wrong
		}
		out.Error = fmt.Sprintf("couldn't reach %s: %v", req.URL.Host, err)
		return done()
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	out.Status = res.StatusCode
	if res.StatusCode != http.StatusOK {
		text := strings.TrimSpace(string(body))
		if len(text) > 2000 {
			text = text[:2000] + "…"
		}
		out.Error = fmt.Sprintf("%s: %s", res.Status, text)
		return done()
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		out.Error = "the provider answered 200, but not with a list of models: " + err.Error()
		return done()
	}
	for _, m := range list.Data {
		if m.ID != "" {
			out.Models = append(out.Models, m.ID)
		}
	}
	slices.Sort(out.Models)
	out.OK = true
	return done()
}

var maskedRE = regexp.MustCompile(`\S*\*{3,}\S*`)

// Scrub takes a provider key out of a message the provider wrote: the key
// itself, any run of 6 or more of its characters past the prefix, and masked
// echoes of it (OpenAI's "sk-proj-****abcd"), so a refusal can be shown
// verbatim otherwise.
func Scrub(msg, key string) string {
	if key == "" || msg == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, key, KeyPrefix(key)+"…")
	msg = maskedRE.ReplaceAllString(msg, "[masked key]")
	if len(key) <= 256 {
		from := len(KeyPrefix(key))
		for n := len(key) - from; n >= 6; n-- {
			for i := from; i+n <= len(key); i++ {
				if strings.Contains(msg, key[i:i+n]) {
					msg = strings.ReplaceAll(msg, key[i:i+n], "…")
				}
			}
		}
	}
	return msg
}
