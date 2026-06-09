package collectors

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// logTailLines bounds the number of log lines fetched for an alert.
const logTailLines int64 = 50

// PreviousContainerLogs fetches up to logTailLines lines of the prior
// container instance and runs them through a best-effort secret redactor
// before returning. The output is always destined for an external sink
// so any high-confidence secret pattern is masked.
func PreviousContainerLogs(ctx context.Context, c kubernetes.Interface, pod *v1.Pod, container string) (string, error) {
	opts := &v1.PodLogOptions{
		Container:  container,
		Previous:   true,
		Timestamps: true,
		TailLines:  ptr.To(logTailLines),
	}
	rc, err := c.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream logs: %w", err)
	}
	defer rc.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(rc); err != nil {
		return "", err
	}
	return RedactSecrets(buf.String()), nil
}

// secretPatterns matches well-known credential shapes that should never
// be forwarded to chat / paging integrations. Each match is replaced with
// `[REDACTED]`. Additional patterns can be added without changing callers.
// secretPattern pairs a regex with its replacement so callers can either
// drop the whole match or preserve the key portion of a key=value pair.
type secretPattern struct {
	re   *regexp.Regexp
	repl string
}

var secretPatterns = []secretPattern{
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)(aws_secret_access_key)(\s*[=:]\s*)([^\s&"']+)`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`ghp_[0-9A-Za-z]{30,}`), "[REDACTED]"},
	{regexp.MustCompile(`gho_[0-9A-Za-z]{30,}`), "[REDACTED]"},
	{regexp.MustCompile(`xox[abpr]-[0-9A-Za-z-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)(bearer)(\s+)([A-Za-z0-9._\-]+)`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)(\s*[=:]\s*)(?:\[REDACTED\]|[^\s&"']+)`), "${1}${2}[REDACTED]"},
}

// RedactSecrets masks credential-shaped substrings.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	// URL query-string tokens first — preserve surrounding params instead of
	// being swallowed by the broad key=value pattern below.
	if strings.Contains(s, "://") {
		s = urlTokenPattern.ReplaceAllString(s, "$1[REDACTED]")
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// urlTokenPattern catches query-string credentials such as
// `?token=...` or `&signature=...`.
var urlTokenPattern = regexp.MustCompile(`([?&](?:token|signature|sig|api[_-]?key|access[_-]?token)=)[^&\s]+`)
