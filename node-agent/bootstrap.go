package nodeagent

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// tokenPattern is the form of a bootstrap token, <id>.<secret>.
var tokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// bootstrapToken checks the flags that make the agent keep its own client
// certificate against the rules of ADR 0007 and returns the token the file
// holds, empty when no file is given. It is called when --bootstrap-token-file
// or --cert-dir is given. Errors name flags and never the token.
func bootstrapToken(tokenFile, certDir string, files api.TLSFiles) (string, error) {
	var bootstrapFlags, operatorFlags []string
	for _, f := range []struct{ flag, value string }{{"--bootstrap-token-file", tokenFile}, {"--cert-dir", certDir}} {
		if f.value != "" {
			bootstrapFlags = append(bootstrapFlags, f.flag)
		}
	}
	for _, f := range []struct{ flag, value string }{{"--tls-cert", files.Cert}, {"--tls-key", files.Key}} {
		if f.value != "" {
			operatorFlags = append(operatorFlags, f.flag)
		}
	}
	if len(operatorFlags) > 0 {
		return "", fmt.Errorf("%s and %s exclude each other: the agent takes its certificate from the bootstrap flags or from the operator's files",
			strings.Join(bootstrapFlags, ", "), strings.Join(operatorFlags, ", "))
	}
	if certDir == "" {
		return "", fmt.Errorf("--bootstrap-token-file needs --cert-dir")
	}
	if files.CA == "" {
		return "", fmt.Errorf("%s needs --tls-ca", strings.Join(bootstrapFlags, ", "))
	}
	if tokenFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("bootstrap token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if !tokenPattern.MatchString(token) {
		return "", fmt.Errorf("bootstrap token file %s: want one token matching %s", tokenFile, tokenPattern)
	}
	return token, nil
}
