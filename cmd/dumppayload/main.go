package main

import (
	"encoding/json"
	"fmt"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/templates"
	"github.com/slack-go/slack"
)

func main() {
	a := alert.New(alert.KindPod, "dev-banyan", "test-pod-abc", "CrashLoopBackOff", alert.SeverityCritical)
	a.NodeName = "node-1"
	a.Cluster = "dev-lotusmgmt-o-eks"
	a.Summary = "container test in pod dev-banyan/test-pod-abc entered CrashLoopBackOff"
	a.StartsAt = time.Now()
	a.Details["Pod Status"] = "NAME\tREADY\tSTATUS\nfoo  0/1  CrashLoopBackOff"
	a.Details["Container State"] = "test:\n  Ready: False\n  Restart Count: 5"

	blocks := templates.Build(a)
	msg := &slack.WebhookMessage{
		Username:    "alertkube",
		IconEmoji:   ":kubernetes:",
		Blocks:      &slack.Blocks{BlockSet: blocks},
		Attachments: []slack.Attachment{{Color: a.Severity.Color(), Footer: fmt.Sprintf("%s | %s | fp=%s", a.Cluster, a.Kind, a.Fingerprint)}},
	}
	b, _ := json.Marshal(msg)
	fmt.Println(string(b))
}
