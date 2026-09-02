package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ECRConfig is what an AWSRegistry needs to build a Provider - plain values,
// not the CRD type, keeping this package free of a Kubernetes API import.
type ECRConfig struct {
	Region     string
	RegistryID string
	RoleARN    string
}

// NewECRProvider resolves credentials through the AWS SDK's default chain -
// environment, an instance profile, IRSA or EKS Pod Identity all work
// without this needing to know which one is in play - then optionally
// assumes RoleARN on top for a cross-account registry.
func NewECRProvider(ctx context.Context, cfg ECRConfig) (Provider, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	if cfg.RoleARN != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		awsCfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsClient, cfg.RoleARN))
	}
	return &ecrProvider{client: ecr.NewFromConfig(awsCfg), registryID: cfg.RegistryID}, nil
}

type ecrProvider struct {
	client     *ecr.Client
	registryID string
}

func (p *ecrProvider) List(ctx context.Context) ([]RemoteImage, error) {
	repositories, err := p.repositories(ctx)
	if err != nil {
		return nil, err
	}

	var images []RemoteImage
	for _, repository := range repositories {
		tagged, err := p.imagesIn(ctx, repository)
		if err != nil {
			return nil, err
		}
		images = append(images, tagged...)
	}
	return images, nil
}

func (p *ecrProvider) repositories(ctx context.Context) ([]ecrtypes.Repository, error) {
	var registryID *string
	if p.registryID != "" {
		registryID = &p.registryID
	}

	var repositories []ecrtypes.Repository
	paginator := ecr.NewDescribeRepositoriesPaginator(p.client, &ecr.DescribeRepositoriesInput{RegistryId: registryID})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe ECR repositories: %w", err)
		}
		repositories = append(repositories, page.Repositories...)
	}
	return repositories, nil
}

func (p *ecrProvider) imagesIn(ctx context.Context, repository ecrtypes.Repository) ([]RemoteImage, error) {
	var images []RemoteImage
	paginator := ecr.NewDescribeImagesPaginator(p.client, &ecr.DescribeImagesInput{
		RegistryId:     repository.RegistryId,
		RepositoryName: repository.RepositoryName,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe images in %s: %w", aws.ToString(repository.RepositoryName), err)
		}
		for _, detail := range page.ImageDetails {
			images = append(images, tagsOf(repository, detail)...)
		}
	}
	return images, nil
}

// tagsOf expands one image detail into one RemoteImage per tag: ECR reports
// an image once with every tag pointing at it, but the tag is what a
// TagSelector and a WorkspaceImage name both need to match against.
func tagsOf(repository ecrtypes.Repository, detail ecrtypes.ImageDetail) []RemoteImage {
	host := aws.ToString(repository.RepositoryUri)
	var pushedAt time.Time
	if detail.ImagePushedAt != nil {
		pushedAt = *detail.ImagePushedAt
	}
	images := make([]RemoteImage, 0, len(detail.ImageTags))
	for _, tag := range detail.ImageTags {
		images = append(images, RemoteImage{
			Repository: aws.ToString(repository.RepositoryName),
			Tag:        tag,
			Reference:  host + ":" + tag,
			PushedAt:   pushedAt,
		})
	}
	return images
}
