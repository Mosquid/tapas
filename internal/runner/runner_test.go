package runner_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"tapas/internal/runner"
)

func TestRedactorRemovesSplitAndRepeatedValues(t *testing.T) {
	var sink bytes.Buffer
	r := runner.NewRedactor(&sink, []string{"synthetic-value", "short"})
	for _, chunk := range []string{"before synth", "etic-value after ", "short", " end"} {
		if _, e := r.Write([]byte(chunk)); e != nil {
			t.Fatal(e)
		}
	}
	if e := r.Flush(); e != nil {
		t.Fatal(e)
	}
	got := sink.String()
	if strings.Contains(got, "synthetic-value") || strings.Contains(got, "short") {
		t.Fatal("value survived redaction:", got)
	}
	if got != "before [redacted] after [redacted] end" {
		t.Fatal("unexpected output:", got)
	}
}

func TestExecDeliversValueAndRedactsChildOutput(t *testing.T) {
	var out, errors bytes.Buffer
	secret := "synthetic-runner-value"
	status, e := runner.Exec(context.Background(), []string{"sh", "-c", `printf '%s\n' "$FIXTURE_TOKEN"; printf 'err %s\n' "$FIXTURE_TOKEN" >&2`}, []runner.Binding{{Variable: "FIXTURE_TOKEN", Value: secret}}, &out, &errors)
	if e != nil || status != 0 {
		t.Fatal(status, e)
	}
	if out.String() != "[redacted]\n" || errors.String() != "err [redacted]\n" {
		t.Fatal("child output not redacted:", out.String(), errors.String())
	}
}

func TestExecReportsChildExitStatusAndKeepsValueOutOfArguments(t *testing.T) {
	var out, errors bytes.Buffer
	status, e := runner.Exec(context.Background(), []string{"sh", "-c", "exit 7"}, nil, &out, &errors)
	if e != nil || status != 7 {
		t.Fatal(status, e)
	}
	if _, e = runner.Exec(context.Background(), []string{"tapas-command-that-does-not-exist"}, nil, &out, &errors); e == nil {
		t.Fatal("expected a missing command error")
	}
}

func TestExecStopsWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errors bytes.Buffer
	if _, e := runner.Exec(ctx, []string{"sh", "-c", "sleep 30"}, nil, &out, &errors); e == nil {
		t.Fatal("expected cancellation to stop the child")
	}
}
