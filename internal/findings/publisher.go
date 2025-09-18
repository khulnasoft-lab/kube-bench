package findings

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	"github.com/aws/aws-sdk-go-v2/service/securityhub/types"
	"github.com/pkg/errors"
)

// SecurityHubClient defines the subset of securityhub.Client methods used by Publisher.
type SecurityHubClient interface {
	BatchImportFindings(ctx context.Context, params *securityhub.BatchImportFindingsInput, optFns ...func(*securityhub.Options)) (*securityhub.BatchImportFindingsOutput, error)
}

// A Publisher represents an object that publishes finds to AWS Security Hub.
type Publisher struct {
	client SecurityHubClient // AWS Security Hub Service Client
}

// A PublisherOutput represents an object that contains information about the service call.
type PublisherOutput struct {
	// The number of findings that failed to import.
	//
	// FailedCount is a required field
	FailedCount int32

	// The list of findings that failed to import.
	FailedFindings []types.ImportFindingsError

	// The number of findings that were successfully imported.
	//
	// SuccessCount is a required field
	SuccessCount int32
}

// New creates a new Publisher.
func New(client SecurityHubClient) *Publisher {
	return &Publisher{
		client: client,
	}
}

// PublishFinding publishes findings to AWS Security Hub Service
func (p *Publisher) PublishFinding(finding []types.AwsSecurityFinding) (*PublisherOutput, error) {
	o := PublisherOutput{}
	if len(finding) == 0 {
		return &o, nil
	}

	var errs error

	// Split the slice into batches of up to 100 findings, per Security Hub limits.
	const batchSize = 100
	for start := 0; start < len(finding); start += batchSize {
		end := start + batchSize
		if end > len(finding) {
			end = len(finding)
		}
		input := securityhub.BatchImportFindingsInput{
			Findings: finding[start:end],
		}
		r, err := p.client.BatchImportFindings(context.Background(), &input)
		if err != nil {
			errs = errors.Wrap(err, "finding publish failed")
			continue
		}
		if r != nil {
			if r.FailedCount != nil && *r.FailedCount != 0 {
				o.FailedCount += *r.FailedCount
			}
			if r.SuccessCount != nil && *r.SuccessCount != 0 {
				o.SuccessCount += *r.SuccessCount
			}
			if len(r.FailedFindings) > 0 {
				o.FailedFindings = append(o.FailedFindings, r.FailedFindings...)
			}
		}
	}
	return &o, errs
}
