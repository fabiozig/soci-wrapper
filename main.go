package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"errors"
	"path"
	"soci-wrapper/utils/fs"
	"soci-wrapper/utils/log"
	registryutils "soci-wrapper/utils/registry"

	"github.com/containerd/containerd/images"
	"oras.land/oras-go/v2/content/oci"

	"github.com/awslabs/soci-snapshotter/soci"
	"github.com/awslabs/soci-snapshotter/soci/store"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/platforms"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const artifactsStoreName = "store"
const artifactsDbName = "artifacts.db"

// Returns ecr registry url from an image action event
func buildEcrRegistryUrl(region string, account string) string {
	var awsDomain = ".amazonaws.com"
	if strings.HasPrefix(region, "cn") {
		awsDomain = ".amazonaws.com.cn"
	}
	return account + ".dkr.ecr." + region + awsDomain
}

// Create a temp directory in /tmp
// The directory is prefixed by the Lambda's request id
func createTempDir(ctx context.Context) (string, error) {
	// free space in bytes
	freeSpace := fs.CalculateFreeSpace("/tmp")
	log.Info(ctx, fmt.Sprintf("There are %d bytes of free space in /tmp directory", freeSpace))
	log.Info(ctx, "Creating a directory to store images and SOCI artifacts")
	tempDir, err := os.MkdirTemp("/tmp", "TODO") // The temp dir name is prefixed by the request id
	return tempDir, err
}

// Clean up the data written by the Lambda
func cleanUp(ctx context.Context, dataDir string) {
	log.Info(ctx, fmt.Sprintf("Removing all files in %s", dataDir))
	if err := os.RemoveAll(dataDir); err != nil {
		log.Error(ctx, "Clean up error", err)
	}
}

// Init containerd store
func initContainerdStore(dataDir string) (content.Store, error) {
	containerdStore, err := local.NewStore(path.Join(dataDir, artifactsStoreName))
	return containerdStore, err
}

// Init OCI artifact store
func initOciStore(ctx context.Context, dataDir string) (*oci.Store, error) {
	return oci.NewWithContext(ctx, path.Join(dataDir, artifactsStoreName))
}

// Init SOCI artifact store
func initSociStore(ctx context.Context, dataDir string) (*store.SociStore, error) {
	// Note: We are wrapping an *oci.Store in a store.SociStore because soci.WriteSociIndex
	// expects a store.Store, an interface that extends the oci.Store to provide support
	// for garbage collection.
	ociStore, err := oci.NewWithContext(ctx, path.Join(dataDir, artifactsStoreName))
	return &store.SociStore{ociStore}, err
}

// Init a new instance of SOCI artifacts DB
func initSociArtifactsDb(dataDir string) (*soci.ArtifactsDb, error) {
	artifactsDbPath := path.Join(dataDir, artifactsDbName)
	artifactsDb, err := soci.NewDB(artifactsDbPath)
	if err != nil {
		return nil, err
	}
	return artifactsDb, nil
}

// Build soci index for an aimage and returns its ocispec.Descriptor
func buildIndex(ctx context.Context, dataDir string, sociStore *store.SociStore, image images.Image) (*ocispec.Descriptor, error) {
	log.Info(ctx, "Building SOCI index")
	platform := platforms.DefaultSpec() // TODO: make this a user option

	artifactsDb, err := initSociArtifactsDb(dataDir)
	if err != nil {
		return nil, err
	}

	containerdStore, err := initContainerdStore(dataDir)
	if err != nil {
		return nil, err
	}

	builder, err := soci.NewIndexBuilder(containerdStore, sociStore, artifactsDb, soci.WithMinLayerSize(0), soci.WithPlatform(platform))
	if err != nil {
		return nil, err
	}
	// Build the SOCI index
	_, err = builder.Build(ctx, image)
	if err != nil {
		return nil, err
	}

	// Get SOCI indices for the image from the OCI store
	indexDescriptorInfos, _, err := soci.GetIndexDescriptorCollection(ctx, containerdStore, artifactsDb, image, []ocispec.Platform{platform})
	if err != nil {
		return nil, err
	}
	if len(indexDescriptorInfos) == 0 {
		return nil, errors.New("No SOCI indices found in OCI store")
	}
	sort.Slice(indexDescriptorInfos, func(i, j int) bool {
		return indexDescriptorInfos[i].CreatedAt.Before(indexDescriptorInfos[j].CreatedAt)
	})

	return &indexDescriptorInfos[len(indexDescriptorInfos)-1].Descriptor, nil
}

// Log and return the lambda handler error
func lambdaError(ctx context.Context, msg string, err error) (string, error) {
	log.Error(ctx, msg, err)
	return msg, err
}

func process(ctx context.Context, repo string, digest string, region string, account string) (string, error) {
	registryUrl := buildEcrRegistryUrl(region, account)
	ctx = context.WithValue(ctx, "RegistryURL", registryUrl)

	registry, err := registryutils.Init(ctx, registryUrl)
	if err != nil {
		return lambdaError(ctx, "Remote registry initialization error", err)
	}

	err = registry.ValidateImageManifest(ctx, repo, digest)
	if err != nil {
		log.Warn(ctx, fmt.Sprintf("Image manifest validation error: %v", err))
		// Returning a non error to skip retries
		return "Exited early due to manifest validation error", nil
	}

	// Directory in lambda storage to store images and SOCI artifacts
	dataDir, err := createTempDir(ctx)
	log.Info(ctx, fmt.Sprintf("The path to the dataDir: %s", dataDir))
	if err != nil {
		return lambdaError(ctx, "Directory create error", err)
	}
	defer cleanUp(ctx, dataDir)

	sociStore, err := initSociStore(ctx, dataDir)
	if err != nil {
		return lambdaError(ctx, "OCI storage initialization error", err)
	}

	desc, err := registry.Pull(ctx, repo, sociStore, digest)
	if err != nil {
		return lambdaError(ctx, "Image pull error", err)
	}

	image := images.Image{
		Name:   repo + "@" + digest,
		Target: *desc,
	}

	indexDescriptor, err := buildIndex(ctx, dataDir, sociStore, image)
	if err != nil {
		return lambdaError(ctx, "SOCI index build error", err)
	}
	ctx = context.WithValue(ctx, "SOCIIndexDigest", indexDescriptor.Digest.String())

	err = registry.Push(ctx, sociStore, *indexDescriptor, repo)
	if err != nil {
		return lambdaError(ctx, "SOCI index push error", err)
	}

	log.Info(ctx, "Successfully built and pushed SOCI index")
	return "Successfully built and pushed SOCI index", nil
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: soci-wrapper REPOSITORY_NAME IMAGE_DIGEST AWS_REGION AWS_ACCOUNT")
		os.Exit(1)
	}
	repo := os.Args[1]
	digest := os.Args[2]
	region := os.Args[3]
	account := os.Args[4]
	process(context.TODO(), repo, digest, region, account)
}
