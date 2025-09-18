package findings

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	"github.com/aws/aws-sdk-go-v2/service/securityhub/types"
)

// fakeClient implements SecurityHubClient for testing.
type fakeClient struct {
	calls   int
	fail    bool
	failedN int32
}

func (f *fakeClient) BatchImportFindings(ctx context.Context, params *securityhub.BatchImportFindingsInput, optFns ...func(*securityhub.Options)) (*securityhub.BatchImportFindingsOutput, error) {
	f.calls++
	cnt := int32(0)
	if params != nil {
		cnt = int32(len(params.Findings))
	}
	out := &securityhub.BatchImportFindingsOutput{
		SuccessCount: &cnt,
		FailedCount:  new(int32),
	}
	if f.fail {
		*out.FailedCount = f.failedN
		*out.SuccessCount = 0
	}
	return out, nil
}

func makeFindings(n int) []types.AwsSecurityFinding {
	fs := make([]types.AwsSecurityFinding, n)
	return fs
}

func TestPublishFinding_BatchesOf100(t *testing.T) {
	fcli := &fakeClient{}
	p := New(fcli)
	findings := makeFindings(250)

	out, err := p.PublishFinding(findings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatalf("nil output")
	}
	if fcli.calls != 3 {
		t.Fatalf("expected 3 batch calls, got %d", fcli.calls)
	}
	if out.SuccessCount != 250 {
		t.Fatalf("expected success count 250, got %d", out.SuccessCount)
	}
	if out.FailedCount != 0 {
		t.Fatalf("expected failed count 0, got %d", out.FailedCount)
	}
}

func TestPublishFinding_EmptySlice(t *testing.T) {
	p := New(&fakeClient{})
	out, err := p.PublishFinding(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatalf("nil output")
	}
	if out.SuccessCount != 0 || out.FailedCount != 0 {
		t.Fatalf("expected zero counts, got success=%d failed=%d", out.SuccessCount, out.FailedCount)
	}
}

func TestPublishFinding_FailedBatch(t *testing.T) {
	fcli := &fakeClient{fail: true, failedN: 10}
	p := New(fcli)
	findings := makeFindings(150)

	out, err := p.PublishFinding(findings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatalf("nil output")
	}
	if fcli.calls != 2 {
		t.Fatalf("expected 2 batch calls, got %d", fcli.calls)
	}
	// Each batch of 100 fails 10, so total failed = 20
	if out.FailedCount != 20 {
		t.Fatalf("expected failed count 20, got %d", out.FailedCount)
	}
	if out.SuccessCount != 0 {
		t.Fatalf("expected success count 0, got %d", out.SuccessCount)
	}
}
